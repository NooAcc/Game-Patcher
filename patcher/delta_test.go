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

func TestBuildChunkDeltaRoundTrip(t *testing.T) {
	dir := tempWorkDir(t)
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

			pool := newBlobPool()
			res, err := buildChunkFileEntry(oldPath, newPath, tc.name+"-new.bin", newCrossIndex(), pool)
			if err != nil {
				t.Fatalf("buildChunkFileEntry 失败: %v", err)
			}
			if res.OldHash != HashBytes(tc.old) {
				t.Fatalf("旧文件哈希不匹配")
			}
			if res.NewHash != HashBytes(tc.new) {
				t.Fatalf("新文件哈希不匹配")
			}

			var out bytes.Buffer
			ctx := opContext{gameDir: dir, pool: pool.blobs}
			if err := applyOps(ctx, oldPath, res.Ops, &out); err != nil {
				t.Fatalf("applyOps 失败: %v", err)
			}
			if !bytes.Equal(out.Bytes(), tc.new) {
				t.Fatalf("往返结果不一致: got %d bytes, want %d", out.Len(), len(tc.new))
			}
		})
	}
}

func TestChunkDeltaIdenticalUsesMergedCopy(t *testing.T) {
	dir := tempWorkDir(t)
	data := randomBytes(2<<20, 7)
	oldPath := writeTestFile(t, dir, "old.bin", data)
	newPath := writeTestFile(t, dir, "new.bin", data)

	pool := newBlobPool()
	res, err := buildChunkFileEntry(oldPath, newPath, "x.bin", newCrossIndex(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 1 {
		t.Fatalf("完全相同的文件应合并为 1 个 COPY，实际 %d 个操作", len(res.Ops))
	}
	if res.Ops[0].Kind != OpCopy || res.Ops[0].OldOffset != 0 || res.Ops[0].Length != uint64(len(data)) {
		t.Fatalf("COPY 操作不符合预期: %+v", res.Ops[0])
	}
	if len(pool.blobs) != 0 {
		t.Fatalf("完全相同的内容不应产生字面量块，实际 %d 块", len(pool.blobs))
	}

	rel := &Release{PatchVersion: 1, Payload: NewChunkPayload(
		[]string{"old", "new"},
		[]ChainStep{{SourceIndex: 1, Entries: []Entry{{
			Path: "x.bin", Action: ActionUpdate, OldHash: res.OldHash, NewHash: res.NewHash,
			OldSize: res.OldSize, NewSize: res.NewSize, Ops: res.Ops,
		}}}},
		pool.blobs,
	)}
	var blob bytes.Buffer
	if err := EncodeRelease(&blob, rel); err != nil {
		t.Fatal(err)
	}
	if blob.Len() > 1024 {
		t.Fatalf("相同文件的补丁应非常小，实际 %d 字节", blob.Len())
	}
}

func TestApplyOpsRejectsOutOfRangeCopy(t *testing.T) {
	dir := tempWorkDir(t)
	oldPath := writeTestFile(t, dir, "old.bin", []byte("0123456789"))
	var out bytes.Buffer
	err := applyOps(opContext{}, oldPath, []DeltaOp{{Kind: OpCopy, OldOffset: 5, Length: 100}}, &out)
	if err == nil {
		t.Fatal("越界 COPY 应返回错误")
	}
}

func TestApplyOpsRejectsBadPoolRef(t *testing.T) {
	dir := tempWorkDir(t)
	var out bytes.Buffer
	err := applyOps(opContext{gameDir: dir}, "", []DeltaOp{{Kind: OpPoolRef, PoolIndex: 3, Length: 4}}, &out)
	if err == nil {
		t.Fatal("越界块池引用应返回错误")
	}
}

func TestApplyOpsRejectsCrossFileWithoutContext(t *testing.T) {
	var out bytes.Buffer
	err := applyOps(opContext{}, "", []DeltaOp{{Kind: OpCopyFrom, SrcPath: "a.bin", Length: 4}}, &out)
	if err == nil {
		t.Fatal("缺少游戏目录上下文时跨文件复制应返回错误")
	}
}
