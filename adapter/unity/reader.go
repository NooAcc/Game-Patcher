package unity

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"

	"github.com/pierrec/lz4/v4"
)

// ── 大端读取辅助 ────────────────────────────────────────────────

type sliceReader struct {
	b   []byte
	off int
}

func newSliceReader(b []byte) *sliceReader { return &sliceReader{b: b} }

func (r *sliceReader) remaining() int { return len(r.b) - r.off }

func (r *sliceReader) bytes(n int) ([]byte, error) {
	if n < 0 || n > r.remaining() {
		return nil, fmt.Errorf("读取 %d 字节越界（剩余 %d）", n, r.remaining())
	}
	out := r.b[r.off : r.off+n]
	r.off += n
	return out, nil
}

func (r *sliceReader) skip(n int) error {
	_, err := r.bytes(n)
	return err
}

func (r *sliceReader) u16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *sliceReader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *sliceReader) u64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (r *sliceReader) i64() (int64, error) {
	v, err := r.u64()
	return int64(v), err
}

func (r *sliceReader) cstring() (string, error) {
	for i := r.off; i < len(r.b); i++ {
		if r.b[i] == 0 {
			s := string(r.b[r.off:i])
			r.off = i + 1
			return s, nil
		}
	}
	return "", io.ErrUnexpectedEOF
}

// fileReader 顺序读取文件并记录位置（容器头部解析用）。
type fileReader struct {
	f   *os.File
	pos int64
}

func newFileReader(f *os.File) *fileReader { return &fileReader{f: f} }

func (r *fileReader) bytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("读取长度为负")
	}
	buf := make([]byte, n)
	if _, err := r.f.ReadAt(buf, r.pos); err != nil {
		return nil, err
	}
	r.pos += int64(n)
	return buf, nil
}

func (r *fileReader) u16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *fileReader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *fileReader) u64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (r *fileReader) i64() (int64, error) {
	v, err := r.u64()
	return int64(v), err
}

func (r *fileReader) cstring() (string, error) {
	buf := make([]byte, 0, 32)
	var b [1]byte
	for {
		if _, err := r.f.ReadAt(b[:], r.pos); err != nil {
			return "", err
		}
		r.pos++
		if b[0] == 0 {
			return string(buf), nil
		}
		buf = append(buf, b[0])
		if len(buf) > 4096 {
			return "", fmt.Errorf("unity: 字符串超长")
		}
	}
}

// ── 解码后的逻辑内容视图 ────────────────────────────────────────

const blockCacheEntries = 8

// blockReader 把 UnityFS 的压缩块按需解压成连续的逻辑流。
// 它实现 adapter.DecodedFile：随机读取 + 总长度 + Close。
type blockReader struct {
	c   *container
	cum []int64 // cum[i] 为第 i 块在逻辑流中的起始偏移，长度 len(blocks)+1

	mu    sync.Mutex
	cache map[int][]byte
	order []int
}

func (c *container) decoded() *blockReader {
	cum := make([]int64, len(c.blocks)+1)
	for i := range c.blocks {
		cum[i+1] = cum[i] + int64(c.blocks[i].uncompressedSize)
	}
	return &blockReader{c: c, cum: cum, cache: make(map[int][]byte, blockCacheEntries)}
}

func (r *blockReader) Size() int64 { return r.cum[len(r.cum)-1] }

func (r *blockReader) Close() error { return r.c.Close() }

// ReadAt 实现 io.ReaderAt。
func (r *blockReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("负偏移")
	}
	size := r.Size()
	if off >= size {
		return 0, io.EOF
	}
	want := int64(len(p))
	if off+want > size {
		want = size - off
	}
	done := 0
	for done < int(want) {
		pos := off + int64(done)
		idx := sort.Search(len(r.cum)-1, func(i int) bool { return r.cum[i+1] > pos })
		if idx >= len(r.c.blocks) {
			break
		}
		blk, err := r.block(idx)
		if err != nil {
			return done, err
		}
		inner := pos - r.cum[idx]
		n := copy(p[done:want], blk[inner:])
		if n == 0 {
			return done, io.ErrUnexpectedEOF
		}
		done += n
	}
	if int64(done) < int64(len(p)) {
		return done, io.EOF
	}
	return done, nil
}

// block 取出第 idx 个块的内容（带小型 LRU 缓存）。
func (r *blockReader) block(idx int) ([]byte, error) {
	r.mu.Lock()
	if b, ok := r.cache[idx]; ok {
		r.mu.Unlock()
		return b, nil
	}
	r.mu.Unlock()

	b, err := r.c.readBlock(idx)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if _, ok := r.cache[idx]; !ok {
		if len(r.order) >= blockCacheEntries {
			delete(r.cache, r.order[0])
			r.order = r.order[1:]
		}
		r.cache[idx] = b
		r.order = append(r.order, idx)
	}
	r.mu.Unlock()
	return b, nil
}

// readBlock 读取并解压第 i 个压缩块。
func (c *container) readBlock(i int) ([]byte, error) {
	if i < 0 || i >= len(c.blocks) {
		return nil, fmt.Errorf("块下标越界: %d", i)
	}
	b := &c.blocks[i]
	if b.compressedSize == 0 {
		return []byte{}, nil
	}
	raw := make([]byte, b.compressedSize)
	if _, err := c.f.ReadAt(raw, b.dataOffset); err != nil {
		return nil, err
	}
	return decompressBlock(raw, int(b.uncompressedSize), int(b.flags&0x3F))
}

// compressBlock 用 LZ4 压缩一个块；压缩无收益时返回原文与 compNone。
//
// 优先使用高压缩率模式，使重建后的容器体积尽量接近官方产物（官方数据块使用
// LZ4 高压缩率），高压缩率失败或不可用时回退快速模式。两者输出的都是标准 LZ4
// 块格式，因此统一标记为 compLZ4。
func compressBlock(raw []byte) (data []byte, comp int) {
	if len(raw) == 0 {
		return nil, compNone
	}
	dst := make([]byte, lz4.CompressBlockBound(len(raw)))
	if n, err := lz4.CompressBlockHC(raw, dst, lz4.Level6, nil, nil); err == nil && n > 0 && n < len(raw) {
		return dst[:n], compLZ4
	}
	var c lz4.Compressor
	if n, err := c.CompressBlock(raw, dst); err == nil && n > 0 && n < len(raw) {
		return dst[:n], compLZ4
	}
	return raw, compNone
}
