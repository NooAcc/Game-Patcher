package patcher

import (
	"bytes"
	"compress/flate"
	"io"
	"testing"
)

// TestCompressLiteralRoundTrip 校验构建端编码器产出的字面量可被解码端还原。
func TestCompressLiteralRoundTrip(t *testing.T) {
	cases := map[string][]byte{
		"compressible-text": []byte(bytes.Repeat([]byte("hello world, this is a compressible payload. "), 400)),
		"compressible-zero": make([]byte, 64<<10),
		"incompressible":    randomBytes(64<<10, 11),
		"single-byte":       []byte("x"),
		"empty":             {},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			comp, data := compressLiteral(raw)
			switch comp {
			case CompRaw:
				if !bytes.Equal(data, raw) {
					t.Fatalf("CompRaw 载荷应与原文一致")
				}
			case CompFlate:
				if len(data) >= len(raw) {
					t.Fatalf("CompFlate 载荷 %d 不应大于原文 %d", len(data), len(raw))
				}
				got := decodeFlateForTest(t, data)
				if !bytes.Equal(got, raw) {
					t.Fatalf("解码结果与原文不一致: got %d bytes, want %d", len(got), len(raw))
				}
			default:
				t.Fatalf("未知压缩方式: %d", comp)
			}
		})
	}
}

// TestCompressLiteralUsesFlateForCompressible 确认可压缩数据确实走了压缩分支。
func TestCompressLiteralUsesFlateForCompressible(t *testing.T) {
	raw := bytes.Repeat([]byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"), 2000)
	comp, _ := compressLiteral(raw)
	if comp != CompFlate {
		t.Fatalf("高度可压缩的数据应选择 CompFlate，实际 %d", comp)
	}
}

// TestCompressLiteralFallsBackToRaw 确认不可压缩数据回退为原文存储。
func TestCompressLiteralFallsBackToRaw(t *testing.T) {
	raw := randomBytes(64<<10, 3)
	comp, data := compressLiteral(raw)
	if comp != CompRaw {
		t.Fatalf("不可压缩数据应回退为 CompRaw，实际 %d", comp)
	}
	if !bytes.Equal(data, raw) {
		t.Fatal("回退载荷应与原文一致")
	}
}

func decodeFlateForTest(t *testing.T, data []byte) []byte {
	t.Helper()
	r := flate.NewReader(bytes.NewReader(data))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭解压器失败: %v", err)
	}
	return out
}

type codecChunk struct {
	comp CompMethod
	data []byte
	raw  []byte
}

func buildCodecChunks(t *testing.T, n int) []codecChunk {
	t.Helper()
	chunks := make([]codecChunk, 0, n)
	for i := 0; i < n; i++ {
		var raw []byte
		switch i % 3 {
		case 0:
			raw = bytes.Repeat([]byte("compressible-payload-"), 100+i)
		case 1:
			raw = randomBytes(1024+i, int64(i))
		default:
			raw = nil
		}
		comp, data := compressLiteral(raw)
		chunks = append(chunks, codecChunk{comp: comp, data: data, raw: raw})
	}
	return chunks
}

func applyCodecChunks(t *testing.T, dec *literalDecoder, chunks []codecChunk) []byte {
	t.Helper()
	var out bytes.Buffer
	ctx := opContext{literal: dec}
	for i := range chunks {
		c := &chunks[i]
		if err := copyLiteral(ctx, c.comp, c.data, uint64(len(c.raw)), &out); err != nil {
			t.Fatalf("第 %d 块解码失败: %v", i, err)
		}
	}
	return out.Bytes()
}

// TestLiteralDecoderReuseMatchesOneShot 是本次优化的核心回归：
// 复用一个解压器连续解码数百个块，结果必须与"每块新建解压器"完全一致。
// 该测试固化 flate.Resetter 在流读尽后可安全 Reset 的行为。
func TestLiteralDecoderReuseMatchesOneShot(t *testing.T) {
	chunks := buildCodecChunks(t, 500)

	var want bytes.Buffer
	for i := range chunks {
		want.Write(chunks[i].raw)
	}

	oneShot := applyCodecChunks(t, nil, chunks)
	if !bytes.Equal(oneShot, want.Bytes()) {
		t.Fatalf("一次性路径结果不正确: got %d bytes, want %d", len(oneShot), want.Len())
	}

	dec := &literalDecoder{}
	reused := applyCodecChunks(t, dec, chunks)
	dec.close()
	if !bytes.Equal(reused, want.Bytes()) {
		t.Fatalf("复用路径结果不正确: got %d bytes, want %d", len(reused), want.Len())
	}
	if !bytes.Equal(reused, oneShot) {
		t.Fatal("复用路径与一次性路径结果不一致")
	}
}

// TestLiteralDecoderReuseAcrossApplyOps 走完整的 applyOps 入口，
// 确认复用器与 OpPoolRef / OpLiteral / OpCopy 混合使用时行为正确。
func TestLiteralDecoderReuseAcrossApplyOps(t *testing.T) {
	dir := tempWorkDir(t)
	// 旧文件：作为 OpCopy 的来源
	oldData := []byte("0123456789ABCDEFGHIJ")
	oldPath := writeTestFile(t, dir, "old.bin", oldData)

	// 构造若干块池条目（可压缩与不可压缩各若干）
	var pool []Blob
	var rawParts [][]byte
	for i := 0; i < 40; i++ {
		var raw []byte
		if i%2 == 0 {
			raw = bytes.Repeat([]byte("pool-block-payload."), 50+i)
		} else {
			raw = randomBytes(512+i, int64(1000+i))
		}
		comp, data := compressLiteral(raw)
		pool = append(pool, Blob{Hash: HashBytes(raw), Comp: comp, RawLen: uint64(len(raw)), Data: data})
		rawParts = append(rawParts, raw)
	}

	var want bytes.Buffer
	var ops []DeltaOp
	ops = append(ops, DeltaOp{Kind: OpCopy, OldOffset: 0, Length: 4})
	want.Write(oldData[:4])
	for i := range rawParts {
		ops = append(ops, DeltaOp{Kind: OpPoolRef, PoolIndex: uint32(i), Length: uint64(len(rawParts[i]))})
		want.Write(rawParts[i])
	}

	dec := &literalDecoder{}
	ctx := opContext{gameDir: dir, pool: pool, literal: dec}
	var out bytes.Buffer
	if err := applyOps(ctx, oldPath, ops, &out); err != nil {
		t.Fatalf("applyOps 失败: %v", err)
	}
	dec.close()
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Fatalf("applyOps 复用解压器结果不一致: got %d bytes, want %d", out.Len(), want.Len())
	}
}

// TestLiteralDecoderReuseAfterError 确认某块解码失败后不会污染后续块。
func TestLiteralDecoderReuseAfterError(t *testing.T) {
	dec := &literalDecoder{}
	defer dec.close()

	good, goodData := compressLiteral(bytes.Repeat([]byte("good-payload-"), 100))

	// 先制造一次失败：声明长度比实际大
	var out bytes.Buffer
	ctx := opContext{literal: dec}
	if err := copyLiteral(ctx, good, goodData, uint64(len(goodData))+100, &out); err == nil {
		t.Fatal("长度不符时应返回错误")
	}

	// 后续正常块必须仍然可解
	raw := bytes.Repeat([]byte("after-error-payload-"), 80)
	comp, data := compressLiteral(raw)
	out.Reset()
	if err := copyLiteral(ctx, comp, data, uint64(len(raw)), &out); err != nil {
		t.Fatalf("错误之后复用解压器失败: %v", err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Fatal("错误之后的解码结果不正确")
	}
}
