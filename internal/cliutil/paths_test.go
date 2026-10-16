package cliutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanPathStripsQuotesAndSpace(t *testing.T) {
	cases := map[string]string{
		`  "D:\Games\My Game"  `: `D:\Games\My Game`,
		`D:\Games`:                `D:\Games`,
		`  plain  `:                `plain`,
		`"a"`:                     `a`,
		``:                        ``,
	}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Fatalf("CleanPath(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestSplitPathListIgnoresCommasInsideQuotes(t *testing.T) {
	got := SplitPathList(`"D:/Games, Inc/v1.exe",E:/v2.exe`)
	want := []string{`"D:/Games, Inc/v1.exe"`, `E:/v2.exe`}
	if len(got) != len(want) {
		t.Fatalf("拆分数量 = %d，期望 %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestFindBinaryPrefersUserSpecified(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom.exe")
	if err := os.WriteFile(custom, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	// 用户指定优先于同目录候选
	local := filepath.Join(dir, "upgrader.exe")
	if err := os.WriteFile(local, []byte("y"), 0755); err != nil {
		t.Fatal(err)
	}
	got := FindBinary(filepath.Join(dir, "self.exe"), custom, UpgraderCandidates)
	if got != custom {
		t.Fatalf("应优先返回用户指定路径，实际 %q", got)
	}
}

func TestFindBinaryFallsBackToFirstCandidateInSelfDir(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, UpgraderCandidates[0])
	if err := os.WriteFile(want, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	got := FindBinary(filepath.Join(dir, "self.exe"), "", UpgraderCandidates)
	if got != want {
		t.Fatalf("应返回同目录首选候选，实际 %q", got)
	}
}

func TestFindBinaryReturnsEmptyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if got := FindBinary(filepath.Join(dir, "self.exe"), "", RestorerCandidates); got != "" {
		t.Fatalf("未找到时应返回空字符串，实际 %q", got)
	}
	if got := FindBinary(filepath.Join(dir, "self.exe"), filepath.Join(dir, "nope.exe"), RestorerCandidates); got != "" {
		t.Fatalf("用户指定但不存在时应返回空字符串，实际 %q", got)
	}
}