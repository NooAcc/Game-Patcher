package unity

import (
	"fmt"
	"io"
	"os"

	"github.com/pierrec/lz4/v4"
)

// UnityFS 容器格式常量。
const (
	signature = "UnityFS\x00"

	// 压缩类型（header.flags / block.flags 的低 6 位）。
	compNone  = 0
	compLZMA  = 1
	compLZ4   = 2
	compLZ4HC = 3

	// minVersion 之后 blocksInfo 以 16 字节哈希开头。
	minVersion = 7

	headerProbeLen = 128
)

// blockInfo 描述一个压缩块。
type blockInfo struct {
	uncompressedSize uint32
	compressedSize   uint32
	flags            uint16
	dataOffset       int64 // 压缩数据在文件中的绝对偏移
}

// nodeInfo 描述容器内的一个文件节点（逻辑流中的一段）。
type nodeInfo struct {
	offset int64
	size   int64
	flags  uint32
	path   string
}

// container 是一个已解析的 UnityFS 容器。
type container struct {
	f    *os.File
	size int64

	version       uint32
	unityVersion  string
	unityRevision string
	flags         uint32
	blocksInfoOff int64
	blockDataOff  int64
	infoHash      [16]byte

	blocks []blockInfo
	nodes  []nodeInfo
}

func align16(n int64) int64 { return (n + 15) / 16 * 16 }

func compressionOf(flags uint32) int { return int(flags & 0x3F) }

// supportedCompression 报告该压缩类型是否可解码（本适配器不支持 LZMA）。
func supportedCompression(t int) bool {
	switch t {
	case compNone, compLZ4, compLZ4HC:
		return true
	default:
		return false
	}
}

func readCString(r io.Reader) (string, error) {
	buf := make([]byte, 0, 32)
	var b [1]byte
	for {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		if b[0] == 0 {
			return string(buf), nil
		}
		buf = append(buf, b[0])
		if len(buf) > 4096 {
			return "", fmt.Errorf("unity: 字符串超长")
		}
	}
}

// probe 只读文件头部少量字节，判断是否为受支持的 UnityFS 容器。
// 它用于 Match：必须足够便宜。
func probe(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	head := make([]byte, headerProbeLen)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false, err
	}
	head = head[:n]
	if len(head) < 8 || string(head[:8]) != signature {
		return false, nil
	}
	reader := newSliceReader(head[8:])
	version, err := reader.u32()
	if err != nil {
		return false, nil
	}
	if version < minVersion {
		return false, nil
	}
	if _, err := reader.cstring(); err != nil {
		return false, nil
	}
	if _, err := reader.cstring(); err != nil {
		return false, nil
	}
	if err := reader.skip(8 + 4 + 4); err != nil {
		return false, nil
	}
	flags, err := reader.u32()
	if err != nil {
		return false, nil
	}
	if (flags & 0x80) != 0 { // blocksInfo 位于文件尾部：暂不支持
		return false, nil
	}
	return supportedCompression(compressionOf(flags)), nil
}

// openContainer 打开并完整解析一个 UnityFS 容器。
func openContainer(path string) (*container, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	c := &container{f: f, size: fi.Size()}
	if err := c.parse(); err != nil {
		f.Close()
		return nil, err
	}
	return c, nil
}

func (c *container) Close() error {
	if c.f == nil {
		return nil
	}
	err := c.f.Close()
	c.f = nil
	return err
}

func (c *container) parse() error {
	r := newFileReader(c.f)
	sig, err := r.bytes(8)
	if err != nil {
		return err
	}
	if string(sig) != signature {
		return fmt.Errorf("unity: 不是 UnityFS 容器")
	}
	if c.version, err = r.u32(); err != nil {
		return err
	}
	if c.version < minVersion {
		return fmt.Errorf("unity: 不支持 UnityFS v%d（需要 v%d 及以上）", c.version, minVersion)
	}
	if c.unityVersion, err = r.cstring(); err != nil {
		return err
	}
	if c.unityRevision, err = r.cstring(); err != nil {
		return err
	}
	declared, err := r.i64()
	if err != nil {
		return err
	}
	cbis, err := r.u32()
	if err != nil {
		return err
	}
	ubis, err := r.u32()
	if err != nil {
		return err
	}
	if c.flags, err = r.u32(); err != nil {
		return err
	}
	if (c.flags & 0x80) != 0 {
		return fmt.Errorf("unity: blocksInfo 位于文件尾部，暂不支持")
	}
	headerLen := r.pos
	if declared != c.size {
		return fmt.Errorf("unity: 声明大小 %d 与实际大小 %d 不一致", declared, c.size)
	}
	if !supportedCompression(compressionOf(c.flags)) {
		return fmt.Errorf("unity: 不支持的 blocksInfo 压缩类型 %d", compressionOf(c.flags))
	}

	c.blocksInfoOff = align16(headerLen)
	if c.blocksInfoOff < 0 || c.blocksInfoOff+int64(cbis) > c.size {
		return fmt.Errorf("unity: blocksInfo 越界")
	}
	raw := make([]byte, cbis)
	if _, err := c.f.ReadAt(raw, c.blocksInfoOff); err != nil {
		return err
	}
	info, err := decompressBlock(raw, int(ubis), compressionOf(c.flags))
	if err != nil {
		return fmt.Errorf("unity: 解压 blocksInfo 失败: %w", err)
	}
	if err := c.parseBlocksInfo(info); err != nil {
		return err
	}

	c.blockDataOff = align16(c.blocksInfoOff + int64(cbis))
	off := c.blockDataOff
	for i := range c.blocks {
		c.blocks[i].dataOffset = off
		off += int64(c.blocks[i].compressedSize)
	}
	if off > c.size {
		return fmt.Errorf("unity: 压缩块数据越界（%d > %d）", off, c.size)
	}
	return nil
}

func (c *container) parseBlocksInfo(info []byte) error {
	r := newSliceReader(info)
	// v7+ 的 blocksInfo 以 16 字节哈希开头。
	if _, err := r.bytes(16); err != nil {
		return fmt.Errorf("unity: blocksInfo 头部: %w", err)
	}
	copy(c.infoHash[:], info[:16])

	blockCount, err := r.u32()
	if err != nil {
		return fmt.Errorf("unity: 块数量: %w", err)
	}
	if blockCount == 0 || int64(blockCount) > int64(len(info)) {
		return fmt.Errorf("unity: 块数量异常: %d", blockCount)
	}
	c.blocks = make([]blockInfo, 0, blockCount)
	var totalUncompressed int64
	for i := uint32(0); i < blockCount; i++ {
		u, err := r.u32()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 块: %w", i, err)
		}
		comp, err := r.u32()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 块: %w", i, err)
		}
		fl, err := r.u16()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 块: %w", i, err)
		}
		if !supportedCompression(int(fl & 0x3F)) {
			return fmt.Errorf("unity: 第 %d 块使用了不支持的压缩类型 %d", i, fl&0x3F)
		}
		c.blocks = append(c.blocks, blockInfo{uncompressedSize: u, compressedSize: comp, flags: fl})
		totalUncompressed += int64(u)
	}

	nodeCount, err := r.u32()
	if err != nil {
		return fmt.Errorf("unity: 节点数量: %w", err)
	}
	if int64(nodeCount) > int64(len(info)) {
		return fmt.Errorf("unity: 节点数量异常: %d", nodeCount)
	}
	c.nodes = make([]nodeInfo, 0, nodeCount)
	for i := uint32(0); i < nodeCount; i++ {
		off, err := r.i64()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 节点: %w", i, err)
		}
		size, err := r.i64()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 节点: %w", i, err)
		}
		fl, err := r.u32()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 节点: %w", i, err)
		}
		path, err := r.cstring()
		if err != nil {
			return fmt.Errorf("unity: 第 %d 节点路径: %w", i, err)
		}
		if off < 0 || size < 0 || off+size > totalUncompressed {
			return fmt.Errorf("unity: 节点 %s 越界（offset=%d size=%d stream=%d）", path, off, size, totalUncompressed)
		}
		c.nodes = append(c.nodes, nodeInfo{offset: off, size: size, flags: fl, path: path})
	}
	return nil
}

// streamSize 返回逻辑流（所有块解压后拼接）的总长度。
func (c *container) streamSize() int64 {
	var n int64
	for i := range c.blocks {
		n += int64(c.blocks[i].uncompressedSize)
	}
	return n
}

// decompressBlock 按压缩类型把 raw 还原为大小 expected 的数据块。
func decompressBlock(raw []byte, expected int, comp int) ([]byte, error) {
	switch comp {
	case compNone:
		if len(raw) != expected {
			return nil, fmt.Errorf("未压缩块长度不符: %d != %d", len(raw), expected)
		}
		return raw, nil
	case compLZ4, compLZ4HC:
		out := make([]byte, expected)
		n, err := lz4.UncompressBlock(raw, out)
		if err != nil {
			return nil, err
		}
		if n != expected {
			return nil, fmt.Errorf("块解压长度不符: %d != %d", n, expected)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("不支持的压缩类型 %d", comp)
	}
}
