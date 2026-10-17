package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"game-patcher/patcher"
)

// tempWorkDir 创建临时目录并在清理时重试：Windows 上杀软/索引器可能短暂持有
// 新建 .exe 的句柄，直接依赖 t.TempDir() 的清理会偶发 "directory is not empty"。
func tempWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gp-merge-test-*")
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

// writeFakeBaseExe 写一个最小载体文件。
func writeFakeBaseExe(t *testing.T, dir string) string {
	t.Helper()
	base := filepath.Join(dir, "upgrader.exe")
	if err := os.WriteFile(base, []byte("MZ-fake-upgrader"), 0755); err != nil {
		t.Fatal(err)
	}
	return base
}

// makeSingleStepPatch 用真实目录对生成一份单段补丁 EXE。
func makeSingleStepPatch(t *testing.T, dir, name, oldContent, newContent string) string {
	t.Helper()
	oldDir := filepath.Join(dir, name+"-old")
	newDir := filepath.Join(dir, name+"-new")
	if err := os.MkdirAll(oldDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "file.bin"), []byte(oldContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "file.bin"), []byte(newContent), 0644); err != nil {
		t.Fatal(err)
	}

	base := writeFakeBaseExe(t, dir)
	out := filepath.Join(dir, name+".exe")
	if err := patcher.CreatePatch(base, oldDir, newDir, out, ""); err != nil {
		t.Fatalf("生成补丁失败: %v", err)
	}
	return out
}

func TestCommandConfigParsesNameAndOutput(t *testing.T) {
	self := filepath.Join(tempWorkDir(t), "merge.exe")
	inputA := filepath.Join(tempWorkDir(t), "a.exe")
	inputB := filepath.Join(tempWorkDir(t), "b.exe")

	cfg, err := commandConfig(self, []string{inputA, inputB}, "out.exe", "", "",
		stringList{inputA + "=游戏 A"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inputs) != 2 {
		t.Fatalf("输入数量 = %d，期望 2", len(cfg.Inputs))
	}
	if cfg.IDs[inputA] != "游戏 A" {
		t.Fatalf("-name 未写入 ID: %+v", cfg.IDs)
	}
	if cfg.Labels[inputA] != "游戏 A" {
		t.Fatalf("-name 未写入 Label: %+v", cfg.Labels)
	}
	if cfg.Output != filepath.Join(filepath.Dir(self), "out.exe") && filepath.Base(cfg.Output) != "out.exe" {
		t.Fatalf("输出路径不正确: %s", cfg.Output)
	}
}

func TestCommandConfigRejectsBadIDFormat(t *testing.T) {
	self := filepath.Join(tempWorkDir(t), "merge.exe")
	if _, err := commandConfig(self, []string{"a.exe", "b.exe"}, "", "", "", stringList{"no-equals"}); err == nil {
		t.Fatal("-name 缺少 '=' 应报错")
	}
}

// TestInteractiveConfigScriptedInput 覆盖简化后的交互流程：
// 输入补丁路径、可选游戏标识、输出路径、默认确认。
func TestInteractiveConfigScriptedInput(t *testing.T) {
	dir := tempWorkDir(t)
	patchA := makeSingleStepPatch(t, dir, "a", "a1", "a2")
	patchB := makeSingleStepPatch(t, dir, "b", "b1", "b2")

	out := filepath.Join(dir, "combined.exe")
	// 问答顺序: 输入A、输入B、结束输入、A 的游戏标识、B 留空使用文件名、
	//          输出路径、空行确认(Y)。
	script := strings.Join([]string{
		patchA,
		patchB,
		"",
		"game-a",
		"",
		out,
		"",
	}, "\n")

	r := bufio.NewReader(strings.NewReader(script))
	cfg, err := collectMergeConfigFrom(r, filepath.Join(dir, "merge.exe"))
	if err != nil {
		t.Fatalf("交互配置失败: %v", err)
	}
	if len(cfg.Inputs) != 2 {
		t.Fatalf("输入数量 = %d，期望 2", len(cfg.Inputs))
	}
	if cfg.IDs[patchA] != "game-a" || cfg.Labels[patchA] != "game-a" {
		t.Fatalf("自定义游戏标识未同时写入 ID/Label: %+v / %+v", cfg.IDs, cfg.Labels)
	}
	if cfg.IDs[patchB] != "b" || cfg.Labels[patchB] != "b" {
		t.Fatalf("留空时应默认使用文件名: %+v / %+v", cfg.IDs, cfg.Labels)
	}
	if cfg.Output != out {
		t.Fatalf("输出 = %s，期望 %s", cfg.Output, out)
	}
	if filepath.Base(cfg.Upgrader) != "upgrader.exe" {
		t.Fatalf("交互模式应自动查找同目录升级工具，实际 %q", cfg.Upgrader)
	} else if cfg.Restorer != "" {
		t.Fatalf("同目录不存在恢复工具时不应要求输入，实际 %q", cfg.Restorer)
	}
}

// TestInteractiveConfigRequiresTwoInputs 覆盖 AC9 的下限校验。
func TestInteractiveConfigRequiresTwoInputs(t *testing.T) {
	dir := tempWorkDir(t)
	patchA := makeSingleStepPatch(t, dir, "a", "a1", "a2")
	script := patchA + "\n\n"
	_, err := collectMergeConfigFrom(bufio.NewReader(strings.NewReader(script)), filepath.Join(dir, "merge.exe"))
	if err == nil {
		t.Fatal("少于 2 份补丁应报错")
	}
	if !strings.Contains(err.Error(), "至少需要 2") {
		t.Fatalf("错误提示不符合预期: %v", err)
	}
}

func TestCollectMergeConfigDefaultsConfirmToYes(t *testing.T) {
	dir := tempWorkDir(t)
	patchA := makeSingleStepPatch(t, dir, "a", "a1", "a2")
	patchB := makeSingleStepPatch(t, dir, "b", "b1", "b2")
	out := filepath.Join(dir, "combined.exe")

	// 最终确认输入空行，应等价于 Y。
	script := strings.Join([]string{patchA, patchB, "", "", "", out, ""}, "\n")
	cfg, err := collectMergeConfigFrom(bufio.NewReader(strings.NewReader(script)), filepath.Join(dir, "merge.exe"))
	if err != nil {
		t.Fatalf("空行确认应默认执行合并: %v", err)
	}
	if cfg.Output != out {
		t.Fatalf("输出 = %s，期望 %s", cfg.Output, out)
	}
}

func TestCommandConfigNameSupportsChineseAndSpace(t *testing.T) {
	self := filepath.Join(tempWorkDir(t), "merge.exe")
	inputA := filepath.Join(tempWorkDir(t), "a.exe")
	inputB := filepath.Join(tempWorkDir(t), "b.exe")
	cfg, err := commandConfig(self, []string{inputA, inputB}, "", "", "", stringList{inputA + "=游戏 A"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IDs[inputA] != "游戏 A" || cfg.Labels[inputA] != "游戏 A" {
		t.Fatalf("名称应同时写入 ID/Label: %+v / %+v", cfg.IDs, cfg.Labels)
	}
}
// TestExecuteMergesTwoGames 覆盖 AC1 + AC6：合并成功且共享池去重。
func TestExecuteMergesTwoGames(t *testing.T) {
	dir := tempWorkDir(t)
	// 两份补丁的新内容相同，便于验证跨目标池去重
	patchA := makeSingleStepPatch(t, dir, "a", "aaa", "shared-new")
	patchB := makeSingleStepPatch(t, dir, "b", "bbb", "shared-new")
	base := writeFakeBaseExe(t, dir)
	out := filepath.Join(dir, "combined.exe")

	cfg := mergeConfig{
		Inputs:   []string{patchA, patchB},
		IDs:      map[string]string{},
		Labels:   map[string]string{},
		Output:   out,
		Upgrader: base,
		SelfPath: filepath.Join(dir, "merge.exe"),
	}
	if code := execute(cfg); code != 0 {
		t.Fatalf("execute 返回 %d，期望 0", code)
	}

	rel, err := patcher.ReadPatchFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Bundle == nil {
		t.Fatal("输出应为合并补丁")
	}
	if len(rel.Bundle.Targets) != 2 {
		t.Fatalf("目标数量 = %d，期望 2", len(rel.Bundle.Targets))
	}
	if rel.Flags&patcher.FlagIneligibleAsPrev == 0 {
		t.Fatal("合并补丁必须标记不可作为 -prev")
	}
	if rel.Bundle.Targets[0].GameID != "a" || rel.Bundle.Targets[1].GameID != "b" {
		t.Fatalf("默认标识应回退为文件名: %+v", rel.Bundle.Targets)
	}
	if len(rel.Bundle.Pool) != 1 {
		t.Fatalf("相同字面量应去重为 1 份，实际 %d", len(rel.Bundle.Pool))
	}
}

// TestExecuteRejectsChainedPatch 覆盖约束 C1。
func TestExecuteRejectsChainedPatch(t *testing.T) {
	dir := tempWorkDir(t)
	v1 := filepath.Join(dir, "v1")
	v2 := filepath.Join(dir, "v2")
	v3 := filepath.Join(dir, "v3")
	for _, d := range []string{v1, v2, v3} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(v1, "f.bin"), []byte("1"), 0644)
	os.WriteFile(filepath.Join(v2, "f.bin"), []byte("2"), 0644)
	os.WriteFile(filepath.Join(v3, "f.bin"), []byte("3"), 0644)

	base := writeFakeBaseExe(t, dir)
	fix1 := filepath.Join(dir, "fix1.exe")
	if err := patcher.CreatePatch(base, v1, v2, fix1, ""); err != nil {
		t.Fatal(err)
	}
	fix2 := filepath.Join(dir, "fix2.exe")
	if err := patcher.CreatePatchChain(base, v2, v3, fix2, "", []string{fix1}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "combined.exe")
	cfg := mergeConfig{
		Inputs:   []string{fix2, fix1},
		IDs:      map[string]string{},
		Labels:   map[string]string{},
		Output:   out,
		Upgrader: base,
		SelfPath: filepath.Join(dir, "merge.exe"),
	}
	if code := execute(cfg); code == 0 {
		t.Fatal("带版本链的补丁应被拒绝")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("失败时不应产出补丁文件")
	}
}