package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleRelease() *Release {
	pool := []Blob{{Hash: HashBytes([]byte("abc")), Comp: CompRaw, RawLen: 3, Data: []byte("abc")}}
	return &Release{
		PatchVersion: 1,
		Payload: NewChunkPayload(
			[]string{"v1", "v2"},
			[]ChainStep{{
				SourceIndex: 1,
				Entries: []Entry{
					{
						Path: "dir/a.bin", Action: ActionUpdate,
						OldHash: HashBytes([]byte("old")), NewHash: HashBytes([]byte("new")),
						OldSize: 8, NewSize: 5,
						Ops: []DeltaOp{
							{Kind: OpCopy, OldOffset: 0, Length: 2},
							{Kind: OpCopyFrom, SrcPath: "other.bin", OldOffset: 1, Length: 2},
							{Kind: OpPoolRef, PoolIndex: 0, Length: 3},
						},
					},
					{Path: "b.bin", Action: ActionDelete, OldHash: HashBytes([]byte("gone")), OldSize: 4},
				},
			}},
			pool,
		),
	}
}

func encodeReleaseBytes(t *testing.T, rel *Release) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := EncodeRelease(&buf, rel); err != nil {
		t.Fatalf("EncodeRelease 失败: %v", err)
	}
	return buf.Bytes()
}

func TestReleaseEncodeDecodeRoundTrip(t *testing.T) {
	rel := sampleRelease()
	got, err := DecodeRelease(encodeReleaseBytes(t, rel))
	if err != nil {
		t.Fatal(err)
	}
	if got.PatchVersion != rel.PatchVersion {
		t.Fatalf("补丁版本号不一致: %d != %d", got.PatchVersion, rel.PatchVersion)
	}
	gotPayload, src := got.Payload, rel.Payload
	if gotPayload == nil {
		t.Fatal("差异数据丢失")
	}
	if len(gotPayload.Labels) != len(src.Labels) || len(gotPayload.Steps) != len(src.Steps) {
		t.Fatalf("链结构不一致: %+v", gotPayload)
	}
	if len(gotPayload.Pool) != len(src.Pool) {
		t.Fatalf("块池不一致: %d != %d", len(gotPayload.Pool), len(src.Pool))
	}
	for i := range src.Steps {
		we, he := src.Steps[i].Entries, gotPayload.Steps[i].Entries
		if gotPayload.Steps[i].SourceIndex != src.Steps[i].SourceIndex || len(we) != len(he) {
			t.Fatalf("第 %d 段不一致", i+1)
		}
		for j := range we {
			a, b := we[j], he[j]
			if a.Path != b.Path || a.Action != b.Action || a.OldHash != b.OldHash || a.NewHash != b.NewHash {
				t.Fatalf("条目 %d 不一致", j)
			}
			if len(a.Ops) != len(b.Ops) {
				t.Fatalf("条目 %d 操作数不一致", j)
			}
			for k := range a.Ops {
				oa, ob := a.Ops[k], b.Ops[k]
				if oa.Kind != ob.Kind || oa.OldOffset != ob.OldOffset || oa.Length != ob.Length ||
					oa.Comp != ob.Comp || !bytes.Equal(oa.Data, ob.Data) {
					t.Fatalf("条目 %d 操作 %d 不一致", j, k)
				}
			}
		}
	}
}

func TestDecodeReleaseRejectsTruncatedData(t *testing.T) {
	blob := encodeReleaseBytes(t, sampleRelease())
	for n := 0; n < len(blob); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("截断到 %d 字节时发生 panic: %v", n, r)
				}
			}()
			if _, err := DecodeRelease(blob[:n]); err == nil {
				t.Fatalf("截断到 %d 字节应返回错误", n)
			}
		}()
	}
}

func TestDecodeReleaseRejectsBadMagicVersionAndKind(t *testing.T) {
	if _, err := DecodeRelease([]byte("NOT-A-PATCH-AT-ALL")); err == nil {
		t.Fatal("错误魔数应被拒绝")
	}
	blob := encodeReleaseBytes(t, sampleRelease())

	badVer := append([]byte(nil), blob...)
	badVer[len(patchMagic)] = 99
	if _, err := DecodeRelease(badVer); err == nil {
		t.Fatal("错误格式版本应被拒绝")
	}

	badKind := append([]byte(nil), blob...)
	badKind[len(patchMagic)+1] = 99
	if _, err := DecodeRelease(badKind); err == nil {
		t.Fatal("不支持的差异后端应被拒绝")
	}
}

func TestEncodeReleaseRejectsUnsafePath(t *testing.T) {
	rel := &Release{PatchVersion: 1, Payload: NewChunkPayload(
		[]string{"a", "b"},
		[]ChainStep{{SourceIndex: 1, Entries: []Entry{{Path: "../evil.txt", Action: ActionUpdate}}}},
		nil,
	)}
	var buf bytes.Buffer
	if err := EncodeRelease(&buf, rel); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
}

func TestDecodeReleaseRejectsHugeCounts(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(patchMagic)
	b.WriteByte(formatVersion)
	b.WriteByte(byte(PayloadChunk))
	var v [4]byte
	b.Write(v[:])                           // patchVersion = 0
	b.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) // labelCount 超大
	if _, err := DecodeRelease(b.Bytes()); err == nil {
		t.Fatal("超大版本标签数应被拒绝")
	}
}

func TestChainSelfCheckRejectsBrokenChain(t *testing.T) {
	entry := Entry{Path: "x", Action: ActionUpdate, NewSize: 1, Ops: []DeltaOp{{Kind: OpPoolRef, PoolIndex: 0, Length: 1}}}
	pool := []Blob{{Hash: HashBytes([]byte("x")), Comp: CompRaw, RawLen: 1, Data: []byte("x")}}
	cases := []struct {
		name  string
		chain *ChunkPayload
	}{
		{"空链", NewChunkPayload([]string{"a"}, nil, nil)},
		{"标签数量不符", NewChunkPayload([]string{"a"}, []ChainStep{{SourceIndex: 1, Entries: []Entry{entry}}}, pool)},
		{"源序号跳跃", NewChunkPayload([]string{"a", "b"}, []ChainStep{{SourceIndex: 2, Entries: []Entry{entry}}}, pool)},
		{"空段", NewChunkPayload([]string{"a", "b"}, []ChainStep{{SourceIndex: 1}}, nil)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.chain.SelfCheck(); err == nil {
				t.Fatal("非法链应被拒绝")
			}
		})
	}
}

func TestValidateRelPath(t *testing.T) {
	valid := []string{"a.bin", "dir/sub/a.bin", "a b/c-d_e.txt"}
	for _, p := range valid {
		if err := validateRelPath(p); err != nil {
			t.Fatalf("%q 应合法: %v", p, err)
		}
	}
	invalid := []string{"", "/abs", "../up", "a/../../b", "C:/x", `..\win`, "a//b", "a/./b", "x\x00y"}
	for _, p := range invalid {
		if err := validateRelPath(p); err == nil {
			t.Fatalf("%q 应非法", p)
		}
	}
}

func TestSafeJoin(t *testing.T) {
	root := tempWorkDir(t)
	got, err := SafeJoin(root, "dir/a.bin")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "dir", "a.bin")
	if got != want {
		t.Fatalf("SafeJoin = %q, want %q", got, want)
	}
	if _, err := SafeJoin(root, "../escape"); err == nil {
		t.Fatal("越界路径应被拒绝")
	}
}

func TestCleanupEmptyDirsStaysInsideStop(t *testing.T) {
	root := tempWorkDir(t)
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	cleanupEmptyDirs(deep, root)
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatalf("空目录树应被清理")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("stopAt 目录不应被删除: %v", err)
	}

	outside := filepath.Join(tempWorkDir(t), "x")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	cleanupEmptyDirs(outside, root)
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("stopAt 之外的目录不应被删除: %v", err)
	}
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{512: "512 B", 2048: "2.00 KB", 2 << 20: "2.00 MB"}
	for in, want := range cases {
		if got := FormatSize(in); got != want {
			t.Fatalf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
	if !strings.Contains(FormatSize(3<<30), "GB") {
		t.Fatal("GB 格式化错误")
	}
}

func TestPauseReturnsOnClosedStdin(t *testing.T) {
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = oldStdin
		r.Close()
	})

	done := make(chan struct{})
	go func() {
		Pause()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Pause 在标准输入关闭时未返回")
	}
}
