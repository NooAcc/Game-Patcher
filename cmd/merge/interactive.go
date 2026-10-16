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
	fmt.Println("为每份补丁指定标识与名称（回车接受默认值）。")
	for _, path := range cfg.Inputs {
		defaultID := defaultGameID(path)
		id, err := promptLine(r, fmt.Sprintf("[%s] 游戏标识（默认 %s）: ", filepath.Base(path), defaultID))
		if err != nil {
			return cfg, err
		}
		if id == "" {
			id = defaultID
		}
		label, err := promptLine(r, fmt.Sprintf("[%s] 显示名称（默认 %s）: ", filepath.Base(path), id))
		if err != nil {
			return cfg, err
		}
		if label == "" {
			label = id
		}
		cfg.IDs[path] = id
		cfg.Labels[path] = label
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

	found := cliutil.FindBinary(selfPath, "", cliutil.UpgraderCandidates)
	defaultUp := found
	custom, err := promptLine(r, fmt.Sprintf("升级工具基础程序（默认 %s）: ", describeDefault(defaultUp, "自动查找")))
	if err != nil {
		return cfg, err
	}
	if custom != "" {
		abs, err := resolveExistingFile(custom, "升级工具")
		if err != nil {
			return cfg, err
		}
		cfg.Upgrader = abs
	}

	restorerFound := cliutil.FindBinary(selfPath, "", cliutil.RestorerCandidates)
	customRestorer, err := promptLine(r, fmt.Sprintf("恢复工具（默认 %s，输入 - 表示不内嵌）: ", describeDefault(restorerFound, "无")))
	if err != nil {
		return cfg, err
	}
	switch customRestorer {
	case "-":
		cfg.Restorer = ""
	case "":
		cfg.Restorer = restorerFound
	default:
		abs, err := resolveExistingFile(customRestorer, "恢复工具")
		if err != nil {
			return cfg, err
		}
		cfg.Restorer = abs
	}

	fmt.Println()
	fmt.Println("--- 确认 ---")
	for i, path := range cfg.Inputs {
		fmt.Printf("  %d) %s  [标识 %s / 名称 %s]\n", i+1, path, cfg.IDs[path], cfg.Labels[path])
	}
	fmt.Printf("  输出: %s\n", cfg.Output)
	fmt.Printf("  升级工具: %s\n", describeDefault(cfg.Upgrader, "自动查找"))
	if cfg.Restorer == "" {
		fmt.Println("  恢复工具: 不内嵌")
	} else {
		fmt.Printf("  恢复工具: %s\n", cfg.Restorer)
	}

	confirm, err := promptLine(r, "确认合并? (y/N): ")
	if err != nil {
		return cfg, err
	}
	if !strings.EqualFold(strings.TrimSpace(confirm), "y") {
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