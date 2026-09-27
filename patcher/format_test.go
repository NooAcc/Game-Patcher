package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func samplePatch() *Patch {
	return &Patch{
		Mode: ModeTree,
		Entries: []Entry{
			{
				Path: "dir/a.bin", Action: ActionUpdate,
				OldHash: HashBytes([]byte("old")), NewHash: HashBytes([]byte("new")),
				OldSize: 3, NewSize: 3,
				Ops: []DeltaOp{
					{Kind: OpCopy, OldOffset: 0, Length: 2},
					{Kind: OpLiteral, Length: 3, Comp: CompRaw, Data: []byte("abc")},
				},
			},
			{Path: "b.bin", Action: ActionDelete, OldHash: HashBytes([]byte("gone")), OldSize: 4},
		},
	}
}

func TestPatchEncodeDecodeRoundTrip(t *testing.T) {
	p := samplePatch()
	blob, err := p.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePatch(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != p.Mode || len(got.Entries) != len(p.Entries) {
		t.Fatalf("解码结果不一致: %+v", got)
	}
	for i := range p.Entries {
		want, have := p.Entries[i], got.Entries[i]
		if want.Path != have.Path || want.Action != have.Action || want.OldHash != have.OldHash || want.NewHash != have.NewHash {
			t.Fatalf("条目 %d 不一致", i)
		}
		if len(want.Ops) != len(have.Ops) {
			t.Fatalf("条目 %d 操作数不一致", i)
		}
		for j := range want.Ops {
			wo, ho := want.Ops[j], have.Ops[j]
			if wo.Kind != ho.Kind || wo.OldOffset != ho.OldOffset || wo.Length != ho.Length || wo.Comp != ho.Comp || !bytes.Equal(wo.Data, ho.Data) {
				t.Fatalf("条目 %d 操作 %d 不一致", i, j)
			}
		}
	}
}

func TestDecodePatchRejectsTruncatedData(t *testing.T) {
	blob, err := samplePatch().Encode()
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(blob); n++ {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("截断到 %d 字节时发生 panic: %v", n, r)
				}
			}()
			if _, err := DecodePatch(blob[:n]); err == nil {
				t.Fatalf("截断到 %d 字节应返回错误", n)
			}
		}()
	}
}

func TestDecodePatchRejectsBadMagicAndVersion(t *testing.T) {
	if _, err := DecodePatch([]byte("NOT-A-PATCH-AT-ALL")); err == nil {
		t.Fatal("错误魔数应被拒绝")
	}
	blob, err := samplePatch().Encode()
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), blob...)
	bad[len(patchMagic)] = 99
	if _, err := DecodePatch(bad); err == nil {
		t.Fatal("错误版本应被拒绝")
	}
}

func TestEncodeRejectsUnsafePath(t *testing.T) {
	p := &Patch{Mode: ModeFile, Entries: []Entry{{Path: "../evil.txt", Action: ActionUpdate}}}
	if _, err := p.Encode(); err == nil {
		t.Fatal("路径穿越应被拒绝")
	}
}

func TestDecodePatchRejectsHugeCounts(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(patchMagic)
	b.WriteByte(patchVersion)
	b.WriteByte(byte(ModeTree))
	putU32(&b, 0xFFFFFFFF)
	if _, err := DecodePatch(b.Bytes()); err == nil {
		t.Fatal("超大条目数应被拒绝")
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
	root := t.TempDir()
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
	root := t.TempDir()
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

	outside := filepath.Join(t.TempDir(), "x")
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
