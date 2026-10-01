package patcher

import (
	"bytes"
	"compress/flate"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/blake3"
)

// CDC（内容定义分块）参数。经真实 app.asar 探针验证：4K 目标块在 306MB
// 文件上产生约 1.37MB 补丁，优于 8K/16K。
const (
	cdcMinSize    = 2 << 10   // 2 KiB
	cdcTargetSize = 4 << 10   // 4 KiB
	cdcMaxSize    = 16 << 10  // 16 KiB
	cdcMaskSmall  = 1<<13 - 1 // 目标块之前更难切分
	cdcMaskLarge  = 1<<11 - 1 // 目标块之后更容易切分
	maxLiteralRun = 4 << 20   // 单个 LITERAL 段最大 4 MiB
)

var gearTable = buildGearTable()

func buildGearTable() [256]uint64 {
	var t [256]uint64
	x := uint64(0x9E3779B97F4A7C15)
	for i := 0; i < 256; i++ {
		x += 0x9E3779B97F4A7C15
		z := x
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		z ^= z >> 31
		t[i] = z
	}
	return t
}

// chunkScanner 以内容定义分块（CDC）方式把输入流切成块。
//
// 实现按 1 MiB 块读取，在索引内循环里更新 gear hash，避免逐字节读取的
// 方法调用开销（实测吞吐约为逐字节实现的 3.3 倍，且分块边界逐字节一致）。
type chunkScanner struct {
	r                    io.Reader
	fill                 []byte // 读取缓冲；块可能以子切片形式直接返回
	blk                  []byte // fill 中本次读入的有效区间
	pos                  int    // blk 中尚未处理的下标
	eof                  bool   // 输入已耗尽
	pending              []byte // 跨块分块的累积内容
	h                    uint64 // pending 的滚动 gear hash
	min, target, maxSize int
	maskSmall, maskLarge uint64
}

// maxZeroReads 限制连续的 (0, nil) 读，避免损坏的 reader 造成忙循环。
const maxZeroReads = 100

func newChunkScanner(r io.Reader) *chunkScanner {
	return &chunkScanner{
		r:         r,
		fill:      make([]byte, 1<<20),
		pending:   make([]byte, 0, cdcMaxSize),
		min:       cdcMinSize,
		target:    cdcTargetSize,
		maxSize:   cdcMaxSize,
		maskSmall: cdcMaskSmall,
		maskLarge: cdcMaskLarge,
	}
}

// next 返回下一个分块。返回的切片在下一次调用 next 时失效，调用方必须立即使用。
func (s *chunkScanner) next() ([]byte, error) {
	s.pending = s.pending[:0]
	s.h = 0
	zeroReads := 0
	for {
		// 当前块耗尽时补充数据；输入结束后跳出，交尾部分块。
		if s.pos >= len(s.blk) {
			if s.eof {
				break
			}
			n, err := s.r.Read(s.fill)
			if n > 0 {
				s.blk = s.fill[:n]
				s.pos = 0
				zeroReads = 0
			} else if err == nil {
				zeroReads++
				if zeroReads >= maxZeroReads {
					return nil, io.ErrNoProgress
				}
			}
			switch {
			case err == io.EOF:
				s.eof = true
			case err != nil:
				return nil, err
			}
			if s.pos >= len(s.blk) {
				// 本次没有读到数据；可能刚置位 eof，回到循环顶部收尾。
				continue
			}
		}

		blk := s.blk
		start := s.pos
		base := len(s.pending)
		h := s.h
		cut := -1
		// 注意：这里刻意用"手动自增 + 单层条件"的写法而不是
		// `for i := start; i < len(blk); i++` + `continue`。两者语义相同，但
		// 后者会让编译器放弃 blk[i] 的边界检查消除，实测该内循环吞吐从
		// ~1.7 GB/s 掉到 ~1.15 GB/s（约 46%）。改动前请先跑 BenchmarkChunkScanner。
		i := start
		for i < len(blk) {
			h = (h << 1) + gearTable[blk[i]]
			n := base + (i - start + 1)
			if n >= s.min {
				mask := s.maskLarge
				if n < s.target {
					mask = s.maskSmall
				}
				if h&mask == 0 || n >= s.maxSize {
					cut = i + 1
					break
				}
			}
			i++
		}
		if cut < 0 {
			// 本块内没有切点：整块并入 pending，跨块续算。
			s.pending = append(s.pending, blk[start:]...)
			s.h = h
			s.pos = len(blk)
			continue
		}
		s.pos = cut
		s.h = 0
		if base == 0 {
			// 分块完整落在本块内：直接返回子切片，免一次拷贝。
			return blk[start:cut], nil
		}
		s.pending = append(s.pending, blk[start:cut]...)
		return s.pending, nil
	}

	if len(s.pending) == 0 {
		return nil, io.EOF
	}
	return s.pending, nil
}

type chunkRef struct {
	offset uint64
	size   int
}

type deltaResult struct {
	OldHash [32]byte
	NewHash [32]byte
	OldSize uint64
	NewSize uint64
	Ops     []DeltaOp
}

// indexChunks 为旧文件建立 分块哈希 -> 偏移 索引，同时计算整文件哈希。
func indexChunks(path string) (map[[32]byte]chunkRef, uint64, [32]byte, error) {
	var sum [32]byte
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, sum, err
	}
	defer f.Close()

	hasher := blake3.New()
	sc := newChunkScanner(io.TeeReader(f, hasher))
	index := make(map[[32]byte]chunkRef)
	var offset uint64
	for {
		ch, err := sc.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, sum, err
		}
		h := blake3.Sum256(ch)
		if _, ok := index[h]; !ok {
			index[h] = chunkRef{offset: offset, size: len(ch)}
		}
		offset += uint64(len(ch))
	}
	copy(sum[:], hasher.Sum(nil))
	return index, offset, sum, nil
}

// appendCopyOp 追加 COPY 操作，并合并旧文件与新文件都相邻的连续 COPY。
func appendCopyOp(ops *[]DeltaOp, oldOffset, length uint64) {
	if length == 0 {
		return
	}
	if n := len(*ops); n > 0 {
		last := &(*ops)[n-1]
		if last.Kind == OpCopy && last.OldOffset+last.Length == oldOffset {
			last.Length += length
			return
		}
	}
	*ops = append(*ops, DeltaOp{Kind: OpCopy, OldOffset: oldOffset, Length: length})
}

func compressLiteral(raw []byte) (CompMethod, []byte) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err == nil {
		_, werr := w.Write(raw)
		cerr := w.Close()
		if werr == nil && cerr == nil && buf.Len() < len(raw) {
			return CompFlate, buf.Bytes()
		}
	}
	return CompRaw, append([]byte(nil), raw...)
}

// opContext 为需要额外上下文（跨文件源、共享块池）的操作提供解析环境。
type opContext struct {
	gameDir string
	pool    []Blob
}

// applyOps 将 ops 应用到 oldPath，结果写入 dst。
// oldPath 为空表示没有本文件的旧内容可复制（新增文件）。
func applyOps(ctx opContext, oldPath string, ops []DeltaOp, dst io.Writer) error {
	var (
		oldFile *os.File
		oldSize uint64
	)
	if oldPath != "" {
		f, err := os.Open(oldPath)
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		oldFile = f
		oldSize = uint64(fi.Size())
	}

	for i := range ops {
		op := &ops[i]
		switch op.Kind {
		case OpCopy:
			if oldFile == nil {
				return fmt.Errorf("操作 %d 需要旧文件，但未提供", i)
			}
			if op.OldOffset > oldSize || op.Length > oldSize-op.OldOffset {
				return fmt.Errorf("COPY 越界: offset=%d length=%d oldSize=%d", op.OldOffset, op.Length, oldSize)
			}
			sr := io.NewSectionReader(oldFile, int64(op.OldOffset), int64(op.Length))
			if _, err := io.CopyN(dst, sr, int64(op.Length)); err != nil {
				return fmt.Errorf("复制旧文件区段失败: %w", err)
			}
		case OpLiteral:
			if err := copyLiteral(&DeltaOp{Kind: OpLiteral, Comp: op.Comp, Data: op.Data, Length: op.Length}, dst); err != nil {
				return fmt.Errorf("写入字面量失败: %w", err)
			}
		case OpCopyFrom:
			if err := copyFromFile(ctx, op, dst); err != nil {
				return err
			}
		case OpPoolRef:
			if int(op.PoolIndex) >= len(ctx.pool) {
				return fmt.Errorf("块池引用越界: %d（池大小 %d）", op.PoolIndex, len(ctx.pool))
			}
			blob := &ctx.pool[op.PoolIndex]
			if err := copyLiteral(&DeltaOp{Kind: OpLiteral, Comp: blob.Comp, Data: blob.Data, Length: op.Length}, dst); err != nil {
				return fmt.Errorf("写入块池数据失败: %w", err)
			}
		default:
			return fmt.Errorf("非法操作类型: %d", op.Kind)
		}
	}
	return nil
}

func copyLiteral(op *DeltaOp, dst io.Writer) error {
	r, err := literalReader(op)
	if err != nil {
		return err
	}
	n, err := io.Copy(dst, io.LimitReader(r, int64(op.Length)+1))
	if closer, ok := r.(io.Closer); ok {
		closer.Close()
	}
	if err != nil {
		return err
	}
	if uint64(n) != op.Length {
		return fmt.Errorf("长度不匹配: 期望 %d，实际 %d", op.Length, n)
	}
	return nil
}

func copyFromFile(ctx opContext, op *DeltaOp, dst io.Writer) error {
	if ctx.gameDir == "" {
		return fmt.Errorf("跨文件复制缺少游戏目录上下文")
	}
	src, err := SafeJoin(ctx.gameDir, op.SrcPath)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开跨文件复制源 %s 失败: %w", op.SrcPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := uint64(fi.Size())
	if op.OldOffset > size || op.Length > size-op.OldOffset {
		return fmt.Errorf("跨文件 COPY 越界: %s offset=%d length=%d size=%d", op.SrcPath, op.OldOffset, op.Length, size)
	}
	sr := io.NewSectionReader(f, int64(op.OldOffset), int64(op.Length))
	if _, err := io.CopyN(dst, sr, int64(op.Length)); err != nil {
		return fmt.Errorf("跨文件复制 %s 失败: %w", op.SrcPath, err)
	}
	return nil
}

func literalReader(op *DeltaOp) (io.Reader, error) {
	switch op.Comp {
	case CompRaw:
		return bytes.NewReader(op.Data), nil
	case CompFlate:
		return flate.NewReader(bytes.NewReader(op.Data)), nil
	default:
		return nil, fmt.Errorf("非法压缩方式: %d", op.Comp)
	}
}
