package main

import (
	"bufio"
	"path/filepath"
	"strings"
	"testing"
)

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
	dir := t.TempDir()
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
