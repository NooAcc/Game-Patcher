package unity

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// templateMeta 是重编码所需的容器模板信息。
//
// 逻辑流本身（解压后的内容）由补丁的 Ops 重建，但 UnityFS 还需要容器元数据：
// 版本号、引擎版本字符串、flags、节点表（文件名与偏移），以及块大小。这些信息
// 在应用端无法从逻辑流推导，因此随补丁保存。
type templateMeta struct {
	version       uint32
	unityVersion  string
	unityRevision string
	flags         uint32
	hash          [16]byte
	blockSize     int
	streamSize    int64
	nodes         []nodeInfo
}

func (c *container) template() *templateMeta {
	blockSize := 0
	for i := range c.blocks {
		if int(c.blocks[i].uncompressedSize) > blockSize {
			blockSize = int(c.blocks[i].uncompressedSize)
		}
	}
	return &templateMeta{
		version:       c.version,
		unityVersion:  c.unityVersion,
		unityRevision: c.unityRevision,
		flags:         c.flags,
		hash:          c.infoHash,
		blockSize:     blockSize,
		streamSize:    c.streamSize(),
		nodes:         c.nodes,
	}
}

// encodeMeta 序列化模板信息（小端，仅本适配器使用）。
func encodeMeta(m *templateMeta) []byte {
	var b bytes.Buffer
	wr32 := func(v uint32) { var t [4]byte; binary.LittleEndian.PutUint32(t[:], v); b.Write(t[:]) }
	wr64 := func(v int64) { var t [8]byte; binary.LittleEndian.PutUint64(t[:], uint64(v)); b.Write(t[:]) }
	wrstr := func(s string) { wr32(uint32(len(s))); b.WriteString(s) }

	wr32(m.version)
	wrstr(m.unityVersion)
	wrstr(m.unityRevision)
	wr32(m.flags)
	b.Write(m.hash[:])
	wr32(uint32(m.blockSize))
	wr64(m.streamSize)
	wr32(uint32(len(m.nodes)))
	for i := range m.nodes {
		n := &m.nodes[i]
		wr64(n.offset)
		wr64(n.size)
		wr32(n.flags)
		wrstr(n.path)
	}
	return b.Bytes()
}

func decodeMeta(data []byte) (*templateMeta, error) {
	r := &metaReader{b: data}
	m := &templateMeta{}
	var err error
	if m.version, err = r.u32(); err != nil {
		return nil, err
	}
	if m.unityVersion, err = r.str(); err != nil {
		return nil, err
	}
	if m.unityRevision, err = r.str(); err != nil {
		return nil, err
	}
	if m.flags, err = r.u32(); err != nil {
		return nil, err
	}
	hash, err := r.bytes(16)
	if err != nil {
		return nil, err
	}
	copy(m.hash[:], hash)
	bs, err := r.u32()
	if err != nil {
		return nil, err
	}
	if m.streamSize, err = r.i64(); err != nil {
		return nil, err
	}
	count, err := r.u32()
	if err != nil {
		return nil, err
	}
	if int64(count) > int64(len(data)) {
		return nil, fmt.Errorf("unity: 模板节点数量异常: %d", count)
	}
	m.blockSize = int(bs)
	m.nodes = make([]nodeInfo, 0, count)
	for i := uint32(0); i < count; i++ {
		var n nodeInfo
		if n.offset, err = r.i64(); err != nil {
			return nil, err
		}
		if n.size, err = r.i64(); err != nil {
			return nil, err
		}
		if n.flags, err = r.u32(); err != nil {
			return nil, err
		}
		if n.path, err = r.str(); err != nil {
			return nil, err
		}
		m.nodes = append(m.nodes, n)
	}
	if r.off != len(data) {
		return nil, fmt.Errorf("unity: 模板信息尾部有多余字节")
	}
	if m.blockSize <= 0 {
		return nil, fmt.Errorf("unity: 模板块大小非法: %d", m.blockSize)
	}
	if m.streamSize <= 0 {
		return nil, fmt.Errorf("unity: 模板流长度非法: %d", m.streamSize)
	}
	return m, nil
}

type metaReader struct {
	b   []byte
	off int
}

func (r *metaReader) bytes(n int) ([]byte, error) {
	if n < 0 || r.off+n > len(r.b) {
		return nil, fmt.Errorf("unity: 模板信息越界")
	}
	out := r.b[r.off : r.off+n]
	r.off += n
	return out, nil
}

func (r *metaReader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (r *metaReader) i64() (int64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b)), nil
}

func (r *metaReader) str() (string, error) {
	n, err := r.u32()
	if err != nil {
		return "", err
	}
	b, err := r.bytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// blockPayload 记录一个已编码块的大小与压缩方式。
type blockPayload struct {
	uncompressedSize uint32
	compressedSize   uint32
	flags            uint16
}

// encodeContainer 把逻辑流重新编码成 UnityFS 容器。
//
// 写入顺序做了两次定位：块负载先写到最终偏移，随后回填头部与 blocksInfo。
// 之所以可行，是因为 blocksInfo 采用“不压缩”存储，其长度只取决于块数量与
// 节点表，可以在写入负载之前精确算出。这样既不需要把 600MB 级负载缓存在
// 内存里，也不需要额外的临时文件。
func encodeContainer(dst io.WriteSeeker, decoded io.Reader, m *templateMeta) error {
	blockSize := m.blockSize
	blockCount := (m.streamSize + int64(blockSize) - 1) / int64(blockSize)
	if blockCount <= 0 {
		return fmt.Errorf("unity: 空逻辑流")
	}

	nodeBytes := int64(4)
	for i := range m.nodes {
		nodeBytes += 20 + int64(len(m.nodes[i].path)) + 1
	}
	infoSize := 16 + 4 + blockCount*10 + nodeBytes

	headerLen := int64(8 + 4 + len(m.unityVersion) + 1 + len(m.unityRevision) + 1 + 8 + 4 + 4 + 4)
	infoOff := align16(headerLen)
	dataOff := align16(infoOff + infoSize)

	// 1) 写出块负载，同时收集块表。
	if _, err := dst.Seek(dataOff, io.SeekStart); err != nil {
		return err
	}
	table := make([]blockPayload, 0, blockCount)
	buf := make([]byte, blockSize)
	var written int64
	var totalRaw int64
	for {
		n, err := io.ReadFull(decoded, buf)
		if n > 0 {
			data, comp := compressBlock(buf[:n])
			if _, werr := dst.Write(data); werr != nil {
				return werr
			}
			table = append(table, blockPayload{
				uncompressedSize: uint32(n),
				compressedSize:   uint32(len(data)),
				flags:            uint16(comp),
			})
			totalRaw += int64(n)
			written += int64(len(data))
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if totalRaw != m.streamSize {
		return fmt.Errorf("unity: 逻辑流长度不符（%d != %d）", totalRaw, m.streamSize)
	}
	if int64(len(table)) != blockCount {
		return fmt.Errorf("unity: 块数量不符（%d != %d）", len(table), blockCount)
	}
	archiveSize := dataOff + written

	// 2) 组装 blocksInfo（不压缩）。
	info := make([]byte, 0, infoSize)
	info = append(info, m.hash[:]...)
	info = appendU32(info, uint32(blockCount))
	for i := range table {
		info = appendU32(info, table[i].uncompressedSize)
		info = appendU32(info, table[i].compressedSize)
		info = appendU16(info, table[i].flags)
	}
	info = appendU32(info, uint32(len(m.nodes)))
	for i := range m.nodes {
		n := &m.nodes[i]
		info = appendU64(info, uint64(n.offset))
		info = appendU64(info, uint64(n.size))
		info = appendU32(info, n.flags)
		info = append(info, n.path...)
		info = append(info, 0)
	}
	if int64(len(info)) != infoSize {
		return fmt.Errorf("unity: blocksInfo 长度计算错误（%d != %d）", len(info), infoSize)
	}

	// 3) 回填头部、填充与 blocksInfo。
	flags := (m.flags &^ 0x3F) | compNone // blocksInfo 不压缩
	flags &^= 0x80                        // blocksInfo 位于文件头部
	head := make([]byte, 0, headerLen)
	head = append(head, signature...)
	head = appendU32(head, m.version)
	head = append(head, m.unityVersion...)
	head = append(head, 0)
	head = append(head, m.unityRevision...)
	head = append(head, 0)
	head = appendU64(head, uint64(archiveSize))
	head = appendU32(head, uint32(infoSize))
	head = appendU32(head, uint32(infoSize))
	head = appendU32(head, flags)

	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := dst.Write(head); err != nil {
		return err
	}
	if err := writeZeros(dst, infoOff-int64(len(head))); err != nil {
		return err
	}
	if _, err := dst.Write(info); err != nil {
		return err
	}
	return writeZeros(dst, dataOff-infoOff-int64(len(info)))
}

func writeZeros(w io.Writer, n int64) error {
	if n <= 0 {
		return nil
	}
	zeros := make([]byte, 4096)
	for n > 0 {
		chunk := int64(len(zeros))
		if chunk > n {
			chunk = n
		}
		if _, err := w.Write(zeros[:chunk]); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func appendU16(b []byte, v uint16) []byte {
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], v)
	return append(b, t[:]...)
}

func appendU32(b []byte, v uint32) []byte {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	return append(b, t[:]...)
}

func appendU64(b []byte, v uint64) []byte {
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], v)
	return append(b, t[:]...)
}
