package patcher

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatalf("创建测试目录失败: %v", err)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	return p
}

func randomBytes(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Intn(256))
	}
	return b
}

func TestBuildDeltaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	base := randomBytes(200<<10, 42)

	insert := append([]byte(nil), base[:100<<10]...)
	insert = append(insert, []byte("INSERTED-BLOCK")...)
	insert = append(insert, base[100<<10:]...)

	removed := append([]byte(nil), base[:80<<10]...)
	removed = append(removed, base[120<<10:]...)

	cases := []struct {
		name string
		old  []byte
		new  []byte
	}{
		{"identical", base, append([]byte(nil), base...)},
		{"both-empty", nil, nil},
		{"empty-to-data", nil, []byte("hello world")},
		{"data-to-empty", []byte("hello world"), nil},
		{"append-tail", base, append(append([]byte(nil), base...), []byte("TAIL-DATA")...)},
		{"prepend-head", base, append([]byte("HEAD-DATA"), base...)},
		{"insert-middle", base, insert},
		{"delete-middle", base, removed},
		{"completely-different", randomBytes(100<<10, 1), randomBytes(200<<10, 2)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldPath := writeTestFile(t, dir, tc.name+"-old.bin", tc.old)
			newPath := writeTestFile(t, dir, tc.name+"-new.bin", tc.new)

			res, err := buildDelta(oldPath, newPath)
			if err != nil {
				t.Fatalf("buildDelta 失败: %v", err)
			}
			if res.OldHash != HashBytes(tc.old) {
				t.Fatalf("旧文件哈希不匹配")
			}
			if res.NewHash != HashBytes(tc.new) {
				t.Fatalf("新文件哈希不匹配")
			}

			var out bytes.Buffer
			if err := applyDelta(oldPath, res.Ops, &out); err != nil {
				t.Fatalf("applyDelta 失败: %v", err)
			}
			if !bytes.Equal(out.Bytes(), tc.new) {
				t.Fatalf("往返结果不一致: got %d bytes, want %d", out.Len(), len(tc.new))
			}
		})
	}
}

func TestDeltaIdenticalUsesMergedCopy(t *testing.T) {
	dir := t.TempDir()
	data := randomBytes(2<<20, 7)
	oldPath := writeTestFile(t, dir, "old.bin", data)
	newPath := writeTestFile(t, dir, "new.bin", data)

	res, err := buildDelta(oldPath, newPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 1 {
		t.Fatalf("完全相同的文件应合并为 1 个 COPY，实际 %d 个操作", len(res.Ops))
	}
	if res.Ops[0].Kind != OpCopy || res.Ops[0].OldOffset != 0 || res.Ops[0].Length != uint64(len(data)) {
		t.Fatalf("COPY 操作不符合预期: %+v", res.Ops[0])
	}

	blob, err := (&Patch{Mode: ModeFile, Entries: []Entry{{
		Path: "x.bin", Action: ActionUpdate, OldHash: res.OldHash, NewHash: res.NewHash,
		OldSize: res.OldSize, NewSize: res.NewSize, Ops: res.Ops,
	}}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) > 1024 {
		t.Fatalf("相同文件的补丁应非常小，实际 %d 字节", len(blob))
	}
}

func TestApplyDeltaRejectsOutOfRangeCopy(t *testing.T) {
	dir := t.TempDir()
	oldPath := writeTestFile(t, dir, "old.bin", []byte("0123456789"))
	var out bytes.Buffer
	err := applyDelta(oldPath, []DeltaOp{{Kind: OpCopy, OldOffset: 5, Length: 100}}, &out)
	if err == nil {
		t.Fatal("越界 COPY 应返回错误")
	}
}
