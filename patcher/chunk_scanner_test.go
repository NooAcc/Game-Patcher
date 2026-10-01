package patcher

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"math/rand"
	"testing"
)

// referenceScan 是优化前的逐字节实现，作为分块边界的等价性基准。
func referenceScan(r io.Reader) ([][]byte, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	var (
		out [][]byte
		buf []byte
		h   uint64
	)
	for {
		b, err := br.ReadByte()
		if err != nil {
			if err == io.EOF {
				if len(buf) == 0 {
					return out, io.EOF
				}
				out = append(out, append([]byte(nil), buf...))
				return out, io.EOF
			}
			return out, err
		}
		buf = append(buf, b)
		h = (h << 1) + gearTable[b]
		n := len(buf)
		if n < cdcMinSize {
			continue
		}
		mask := uint64(cdcMaskLarge)
		if n < cdcTargetSize {
			mask = cdcMaskSmall
		}
		if h&mask == 0 || n >= cdcMaxSize {
			out = append(out, append([]byte(nil), buf...))
			buf = buf[:0]
			h = 0
		}
	}
}

// drainScanner 拉取全部分块并拷贝（next 返回的切片在下一次调用后失效）。
func drainScanner(r io.Reader) ([][]byte, error) {
	sc := newChunkScanner(r)
	var out [][]byte
	for {
		ch, err := sc.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, append([]byte(nil), ch...))
	}
}

func makeTestData(kind string, n int) []byte {
	b := make([]byte, n)
	switch kind {
	case "zeros":
		// 全零：gear hash 很难命中 mask，主要走 maxSize 强制切分路径。
	case "sequential":
		for i := range b {
			b[i] = byte(i)
		}
	default:
		rng := rand.New(rand.NewSource(int64(n) + 1))
		rng.Read(b)
	}
	return b
}

// limitedReader 每次 Read 最多返回 max 字节，用于制造短读与跨块拼接。
type limitedReader struct {
	data []byte
	pos  int
	max  int
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := len(p)
	if r.max > 0 && n > r.max {
		n = r.max
	}
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// eofWithDataReader 在最后一次 Read 中同时返回数据与 io.EOF（io.Reader 允许的少见形态）。
type eofWithDataReader struct {
	data []byte
	pos  int
	max  int
}

func (r *eofWithDataReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := len(p)
	if r.max > 0 && n > r.max {
		n = r.max
	}
	if n > len(r.data)-r.pos {
		n = len(r.data) - r.pos
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	if r.pos >= len(r.data) {
		return n, io.EOF
	}
	return n, nil
}

func chunksEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestChunkScannerMatchesReference 逐块比对批式扫描器与逐字节基准。
// 覆盖多种数据形态、多种长度（含跨 1 MiB 读缓冲边界）与多种短读粒度。
func TestChunkScannerMatchesReference(t *testing.T) {
	sizes := []int{
		0, 1, 2, 100,
		cdcMinSize - 1, cdcMinSize, cdcMinSize + 1,
		cdcTargetSize, cdcMaxSize - 1, cdcMaxSize, cdcMaxSize + 1,
		(1 << 20) - 1, 1 << 20, (1 << 20) + 1,
		(1 << 20) + 12345, (3 << 20) + 7,
	}
	kinds := []string{"random", "zeros", "sequential"}
	limits := []int{0, 1, 7, 1024, 1 << 20}

	for _, size := range sizes {
		for _, kind := range kinds {
			data := makeTestData(kind, size)
			want, _ := referenceScan(bytes.NewReader(data))
			for _, limit := range limits {
				var r io.Reader = &limitedReader{data: data, max: limit}
				got, err := drainScanner(r)
				if err != nil {
					t.Fatalf("size=%d kind=%s limit=%d: 扫描失败: %v", size, kind, limit, err)
				}
				if !chunksEqual(got, want) {
					t.Fatalf("size=%d kind=%s limit=%d: 分块不一致（得到 %d 块 / 期望 %d 块）",
						size, kind, limit, len(got), len(want))
				}
				// 不变式：分块拼接必须逐字节等于输入。
				if joined := bytes.Join(got, nil); !bytes.Equal(joined, data) {
					t.Fatalf("size=%d kind=%s limit=%d: 拼接结果与输入不一致", size, kind, limit)
				}
			}
		}
	}
}

// TestChunkScannerHandlesDataWithEOF 覆盖"数据与 io.EOF 同一次返回"的合法情形。
func TestChunkScannerHandlesDataWithEOF(t *testing.T) {
	for _, size := range []int{1, cdcMinSize, cdcMinSize + 5, (1 << 20) + 999, (2 << 20) + 3} {
		data := makeTestData("random", size)
		want, _ := referenceScan(bytes.NewReader(data))
		for _, limit := range []int{0, 13, 4096} {
			got, err := drainScanner(&eofWithDataReader{data: data, max: limit})
			if err != nil {
				t.Fatalf("size=%d limit=%d: 扫描失败: %v", size, limit, err)
			}
			if !chunksEqual(got, want) {
				t.Fatalf("size=%d limit=%d: 分块不一致（得到 %d 块 / 期望 %d 块）",
					size, limit, len(got), len(want))
			}
			if joined := bytes.Join(got, nil); !bytes.Equal(joined, data) {
				t.Fatalf("size=%d limit=%d: 拼接结果与输入不一致", size, limit)
			}
		}
	}
}

func TestChunkScannerEmptyReturnsEOF(t *testing.T) {
	sc := newChunkScanner(bytes.NewReader(nil))
	if _, err := sc.next(); err != io.EOF {
		t.Fatalf("空输入应返回 io.EOF，实际 %v", err)
	}
}

// zeroReader 总是返回 (0, nil)，用于验证忙循环保护。
type zeroReader struct{ calls int }

func (r *zeroReader) Read(p []byte) (int, error) {
	r.calls++
	return 0, nil
}

func TestChunkScannerNoProgressGuard(t *testing.T) {
	zr := &zeroReader{}
	sc := newChunkScanner(zr)
	if _, err := sc.next(); !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("应返回 io.ErrNoProgress，实际 %v", err)
	}
	if zr.calls > maxZeroReads+1 {
		t.Fatalf("忙循环保护未及时生效，Read 调用 %d 次", zr.calls)
	}
}

func BenchmarkChunkScanner(b *testing.B) {
	data := makeTestData("random", 64<<20)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sc := newChunkScanner(bytes.NewReader(data))
		for {
			if _, err := sc.next(); err == io.EOF {
				break
			} else if err != nil {
				b.Fatal(err)
			}
		}
	}
}
