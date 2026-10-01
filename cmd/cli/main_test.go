package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tempWorkDir 创建带容错清理的临时目录。
// Windows 上 t.TempDir() 的清理会与刚写入文件的句柄释放产生竞态而偶发失败。
func tempWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gp-cli-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 5; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}

func TestLaunchedByDoubleClick(t *testing.T) {
	cases := []struct {
		name       string
		shellFlag  bool
		flagCount  int
		argCount   int
		stdinIsTTY bool
		want       bool
	}{
		{"无参数 + 控制台 => 交互", false, 0, 0, true, true},
		{"无参数 + 管道 => 不交互", false, 0, 0, false, false},
		{"显式 -shell => 不算双击", true, 0, 0, true, false},
		{"带参数 => 不交互", false, 2, 0, true, false},
		{"带位置参数 => 不交互", false, 0, 1, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := launchedByDoubleClick(c.shellFlag, c.flagCount, c.argCount, c.stdinIsTTY); got != c.want {
				t.Fatalf("launchedByDoubleClick(%v, %d, %d, %v) = %v, want %v",
					c.shellFlag, c.flagCount, c.argCount, c.stdinIsTTY, got, c.want)
			}
		})
	}
}

func TestPromptPathErrorsOnEOF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	_, err := promptPath(r, "旧版本文件夹路径")
	if err == nil {
		t.Fatal("输入结束时应返回错误，而不是无限循环")
	}
}

func TestPromptPathRetriesUntilValid(t *testing.T) {
	dir := tempWorkDir(t)
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	r := bufio.NewReader(strings.NewReader("不存在的路径\n" + dir + "\n"))
	got, err := promptPath(r, "旧版本文件夹路径")
	if err != nil {
		t.Fatalf("promptPath 返回错误: %v", err)
	}
	if got != want {
		t.Fatalf("promptPath = %q, 期望 %q", got, want)
	}
}

func TestPromptOutputDefaultsOnEmptyInput(t *testing.T) {
	selfPath := filepath.Join("tools", "game-patcher-cli-win64.exe")
	want := filepath.Join("tools", "game-updater.exe")
	r := bufio.NewReader(strings.NewReader("\n"))
	got := promptOutput(r, selfPath)
	if got != want {
		t.Fatalf("promptOutput = %q, 期望 %q", got, want)
	}
}

func TestStringListParsesRepeatableAndCommaSeparated(t *testing.T) {
	var s stringList
	if err := s.Set("a.exe,b.exe"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("c.exe"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(" , "); err != nil {
		t.Fatal(err)
	}
	want := []string{"a.exe", "b.exe", "c.exe"}
	if len(s) != len(want) {
		t.Fatalf("stringList = %v，期望 %v", s, want)
	}
	for i := range want {
		if s[i] != want[i] {
			t.Fatalf("stringList[%d] = %q，期望 %q", i, s[i], want[i])
		}
	}
}

func TestResolvePrevPatches(t *testing.T) {
	dir := tempWorkDir(t)
	fix := filepath.Join(dir, "fix1.exe")
	if err := os.WriteFile(fix, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(fix)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolvePrevPatches([]string{fix})
	if err != nil {
		t.Fatalf("resolvePrevPatches 失败: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("resolvePrevPatches = %v，期望 [%s]", got, want)
	}
	quoted, err := resolvePrevPatches([]string{"\"" + fix + "\""})
	if err != nil {
		t.Fatalf("带双引号的路径应被接受: %v", err)
	}
	if len(quoted) != 1 || quoted[0] != want {
		t.Fatalf("带双引号的路径解析 = %v，期望 [%s]", quoted, want)
	}

	if _, err := resolvePrevPatches([]string{filepath.Join(dir, "missing.exe")}); err == nil {
		t.Fatal("不存在的旧补丁应返回错误")
	}
	if _, err := resolvePrevPatches([]string{dir}); err == nil {
		t.Fatal("目录作为旧补丁应返回错误")
	}
}

func TestCleanPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\"C:/Games/My Game\"", "C:/Games/My Game"},
		{"   \"C:/Games/My Game\"   ", "C:/Games/My Game"},
		{"C:/Games/My Game", "C:/Games/My Game"},
		{"\"C:/Games/My Game", "\"C:/Games/My Game"},
		{"\"\"", ""},
		{"\"a\"b\"", "a\"b"},
	}
	for _, c := range cases {
		if got := cleanPath(c.in); got != c.want {
			t.Fatalf("cleanPath(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestSplitPathListIgnoresCommasInsideQuotes(t *testing.T) {
	got := splitPathList("\"D:/Games, Inc/v1.exe\",E:/v2.exe")
	want := []string{"\"D:/Games, Inc/v1.exe\"", "E:/v2.exe"}
	if len(got) != len(want) {
		t.Fatalf("splitPathList = %q，期望 %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitPathList[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestStringListAcceptsQuotedCommaPaths(t *testing.T) {
	var s stringList
	if err := s.Set("\"D:/Games, Inc/v1.exe\", E:/v2.exe"); err != nil {
		t.Fatal(err)
	}
	want := []string{"D:/Games, Inc/v1.exe", "E:/v2.exe"}
	if len(s) != len(want) {
		t.Fatalf("stringList = %q，期望 %q", s, want)
	}
	for i := range want {
		if s[i] != want[i] {
			t.Fatalf("stringList[%d] = %q，期望 %q", i, s[i], want[i])
		}
	}
}

func TestPromptPathAcceptsQuotedPath(t *testing.T) {
	dir := tempWorkDir(t)
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟从资源管理器复制目录地址：外层带双引号，前后还有空白与一个空行
	r := bufio.NewReader(strings.NewReader("\n   \"" + dir + "\"   \n"))
	got, err := promptPath(r, "旧版本文件夹路径")
	if err != nil {
		t.Fatalf("promptPath 返回错误: %v", err)
	}
	if got != want {
		t.Fatalf("promptPath = %q，期望 %q", got, want)
	}
}
