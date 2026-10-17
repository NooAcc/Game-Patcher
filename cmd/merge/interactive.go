package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"game-patcher/internal/cliutil"
)

// collectMergeConfig 是纯文本交互向导：逐项问答，回车接受默认值。
//
// 刻意不引入 Bubble Tea / lipgloss / ANSI 颜色：本工具只需要"能交互"，
// 纯顺序问答既满足需求又便于用 strings.Reader 直接单测。
// r 为 nil 时使用标准输入。
func collectMergeConfig(selfPath string) (mergeConfig, error) {
	return collectMergeConfigFrom(bufio.NewReader(os.Stdin), selfPath)
}

// collectMergeConfigFrom 是交互逻辑的可测试入口。
func collectMergeConfigFrom(r *bufio.Reader, selfPath string) (mergeConfig, error) {
	cfg := mergeConfig{
		IDs:      map[string]string{},
		Labels:   map[string]string{},
		SelfPath: selfPath,
	}

	fmt.Println("=== 多游戏补丁合并工具 ===")
	fmt.Println()
	fmt.Println("依次输入要合并的补丁文件路径；直接回车结束输入（至少 2 个）。")

	for {
		line, err := promptLine(r, fmt.Sprintf("补丁 %d 路径（回车结束）: ", len(cfg.Inputs)+1))
		if err != nil {
			return cfg, err
		}
		if line == "" {
			break
		}
		abs, err := resolveExistingFile(line, "补丁")
		if err != nil {
			fmt.Printf("   ⚠️  %v\n", err)
			continue
		}
		if containsPath(cfg.Inputs, abs) {
			fmt.Printf("   ⚠️  该补丁已在列表中: %s\n", abs)
			continue
		}
		cfg.Inputs = append(cfg.Inputs, abs)
		fmt.Printf("   ✔ 已添加: %s\n", filepath.Base(abs))
	}

	if len(cfg.Inputs) < 2 {
		return cfg, fmt.Errorf("至少需要 2 份补丁才能合并，实际 %d 份", len(cfg.Inputs))
	}

	fmt.Println()
	fmt.Println("为每份补丁输入游戏标识（留空使用文件名）。")
	for _, path := range cfg.Inputs {
		defaultID := defaultGameID(path)
		id, err := promptLine(r, fmt.Sprintf("[%s] 游戏标识（留空使用 %s）: ", filepath.Base(path), defaultID))
		if err != nil {
			return cfg, err
		}
		if id == "" {
			id = defaultID
		}
		// 用户界面只保留一个“游戏标识”；Label 与 GameID 使用同一个值，
		// 以兼容现有 GPBIN4 制品的两个字段。
		cfg.IDs[path] = id
		cfg.Labels[path] = id
	}

	fmt.Println()
	defaultOut := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	out, err := promptLine(r, fmt.Sprintf("输出文件路径（默认 %s）: ", defaultOut))
	if err != nil {
		return cfg, err
	}
	if out == "" {
		cfg.Output = defaultOut
	} else {
		abs, err := filepath.Abs(cliutil.CleanPath(out))
		if err != nil {
			return cfg, fmt.Errorf("输出路径无效: %w", err)
		}
		cfg.Output = abs
	}

	// 四个工具通常位于同一目录；交互模式不再询问基座/恢复工具路径，
	// 执行阶段会自动查找。命令模式仍可用 -upgrader / -restorer 覆盖。
	cfg.Upgrader = cliutil.FindBinary(selfPath, "", cliutil.UpgraderCandidates)
	cfg.Restorer = cliutil.FindBinary(selfPath, "", cliutil.RestorerCandidates)

	fmt.Println()
	fmt.Println("--- 确认 ---")
	for i, path := range cfg.Inputs {
		fmt.Printf("  %d) %s  [标识 %s]\n", i+1, path, cfg.IDs[path])
	}
	fmt.Printf("  输出: %s\n", cfg.Output)

	confirm, err := promptConfirm(r, "确认合并? (Y/n): ")
	if err != nil {
		return cfg, err
	}
	if !confirm {
		return cfg, fmt.Errorf("已取消")
	}
	cfg.SelfPath = selfPath
	return cfg, nil
}

func describeDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return filepath.Base(value)
}

// promptConfirm 读取 Y/n 确认；空输入或 EOF 都按默认 Yes 处理。
func promptConfirm(r *bufio.Reader, prompt string) (bool, error) {
	fmt.Print(prompt)
	line, err := r.ReadString('\n')
	line = cliutil.CleanPath(line)
	if err != nil && line == "" {
		if err.Error() == "EOF" {
			return true, nil
		}
		return false, err
	}
	return !strings.EqualFold(strings.TrimSpace(line), "n"), nil
}
func promptLine(r *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := r.ReadString('\n')
	line = cliutil.CleanPath(line)
	if err != nil && line == "" {
		if err.Error() == "EOF" {
			return "", fmt.Errorf("输入已结束，已取消")
		}
		return "", err
	}
	return line, nil
}

func resolveExistingFile(raw, what string) (string, error) {
	abs, err := filepath.Abs(cliutil.CleanPath(raw))
	if err != nil {
		return "", fmt.Errorf("%s路径无效: %w", what, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s不存在: %s", what, abs)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s必须是文件: %s", what, abs)
	}
	return abs, nil
}

func containsPath(list []string, path string) bool {
	for _, p := range list {
		if strings.EqualFold(p, path) {
			return true
		}
	}
	return false
}