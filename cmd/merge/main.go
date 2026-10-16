package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	_ "game-patcher/adapter/all"
	"game-patcher/internal/cliutil"
	"game-patcher/patcher"
)

// mergeConfig 是命令模式与交互模式共同产出的配置，两种模式最终走同一段执行逻辑。
type mergeConfig struct {
	Inputs     []string
	IDs        map[string]string // 输入路径 -> 游戏标识
	Labels     map[string]string // 输入路径 -> 显示名称
	Output     string
	Upgrader   string
	Restorer   string
	SelfPath   string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	selfPath, err := os.Executable()
	if err != nil {
		fmt.Printf("❌ 获取自身路径失败: %v\n", err)
		return 1
	}

	fs := flag.NewFlagSet("game-patcher-merge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	output := fs.String("out", "", "输出合并补丁路径（默认: 当前目录/game-updater.exe）")
	upgraderFlag := fs.String("upgrader", "", "升级工具基础程序路径（默认: 自动查找）")
	restorerFlag := fs.String("restorer", "", "恢复工具二进制路径（默认: 自动查找）")
	shellMode := fs.Bool("shell", false, "进入交互模式（无参数双击时默认进入）")
	var ids, labels stringList
	fs.Var(&ids, "id", "指定游戏标识，格式 <补丁路径>=<标识>（可重复）")
	fs.Var(&labels, "label", "指定显示名称，格式 <补丁路径>=<名称>（可重复）")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	interactive := *shellMode ||
		(len(fs.Args()) == 0 && fs.NFlag() == 0 && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())))
	if interactive {
		cfg, err := collectMergeConfig(selfPath)
		if err != nil {
			handleError(err, true)
			return 1
		}
		return execute(cfg)
	}

	if len(fs.Args()) == 0 {
		printUsage(selfPath)
		return 1
	}

	cfg, err := commandConfig(selfPath, fs.Args(), *output, *upgraderFlag, *restorerFlag, ids, labels)
	if err != nil {
		handleError(err, false)
		return 1
	}
	return execute(cfg)
}

// commandConfig 把命令行参数归一化成 mergeConfig。
func commandConfig(selfPath string, inputs []string, output, upgrader, restorer string, ids, labels stringList) (mergeConfig, error) {
	cfg := mergeConfig{
		Inputs:   make([]string, 0, len(inputs)),
		IDs:      map[string]string{},
		Labels:   map[string]string{},
		Upgrader: upgrader,
		Restorer: restorer,
		SelfPath: selfPath,
	}
	for _, raw := range inputs {
		p := cliutil.CleanPath(raw)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return cfg, fmt.Errorf("补丁路径无效 %s: %w", p, err)
		}
		cfg.Inputs = append(cfg.Inputs, abs)
	}

	applyPairs := func(list stringList, dst map[string]string, what string) error {
		for _, item := range list {
			k, v, ok := strings.Cut(item, "=")
			if !ok {
				return fmt.Errorf("%s 需要 <补丁路径>=<值> 形式: %q", what, item)
			}
			abs, err := filepath.Abs(cliutil.CleanPath(k))
			if err != nil {
				return fmt.Errorf("%s 的补丁路径无效: %w", what, err)
			}
			dst[abs] = strings.TrimSpace(cliutil.CleanPath(v))
		}
		return nil
	}
	if err := applyPairs(ids, cfg.IDs, "-id"); err != nil {
		return cfg, err
	}
	if err := applyPairs(labels, cfg.Labels, "-label"); err != nil {
		return cfg, err
	}

	if output == "" {
		output = filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	}
	absOut, err := filepath.Abs(cliutil.CleanPath(output))
	if err != nil {
		return cfg, fmt.Errorf("输出路径无效: %w", err)
	}
	cfg.Output = absOut
	return cfg, nil
}

// execute 读取全部输入、执行合并并写出补丁 EXE。
func execute(cfg mergeConfig) int {
	if len(cfg.Inputs) < 2 {
		handleError(fmt.Errorf("至少需要 2 份补丁才能合并，实际 %d 份", len(cfg.Inputs)), false)
		return 1
	}

	baseExe := cliutil.FindBinary(cfg.SelfPath, cfg.Upgrader, cliutil.UpgraderCandidates)
	if baseExe == "" {
		fmt.Printf("❌ 找不到升级工具基础程序 (upgrader)，请使用 -upgrader 指定路径\n")
		return 1
	}
	restorerPath := cliutil.FindBinary(cfg.SelfPath, cfg.Restorer, cliutil.RestorerCandidates)

	inputs := make([]patcher.MergeInput, 0, len(cfg.Inputs))
	for _, path := range cfg.Inputs {
		rel, err := patcher.ReadPatchFile(path)
		if err != nil {
			handleError(fmt.Errorf("读取补丁 %s 失败: %w", filepath.Base(path), err), false)
			return 1
		}
		id := cfg.IDs[path]
		if id == "" {
			id = defaultGameID(path)
		}
		label := cfg.Labels[path]
		if label == "" {
			label = id
		}
		inputs = append(inputs, patcher.MergeInput{Name: id, Label: label, Release: rel})
	}

	merged, err := patcher.MergeReleases(inputs)
	if err != nil {
		handleError(err, false)
		return 1
	}

	fmt.Printf("🔀 正在合并 %d 份补丁...\n", len(cfg.Inputs))
	if err := patcher.WritePatchExecutable(baseExe, merged, cfg.Output, restorerPath); err != nil {
		handleError(err, false)
		return 1
	}

	fi, err := os.Stat(cfg.Output)
	if err != nil {
		handleError(err, false)
		return 1
	}
	fmt.Printf("✅ 合并补丁已生成: %s (%s)\n", cfg.Output, patcher.FormatSize(fi.Size()))
	fmt.Printf("   包含 %d 个目标: %s\n", len(merged.Bundle.Targets), targetNames(merged))
	fmt.Println()
	fmt.Println("💡 将此文件放入任一目标游戏的目录，直接运行即可自动识别并升级。")
	fmt.Println("   （合并补丁不能作为 -prev 使用）")
	return 0
}

func targetNames(rel *patcher.Release) string {
	if rel.Bundle == nil {
		return ""
	}
	names := make([]string, 0, len(rel.Bundle.Targets))
	for i := range rel.Bundle.Targets {
		names = append(names, rel.Bundle.Targets[i].DisplayName())
	}
	return strings.Join(names, ", ")
}

// defaultGameID 在用户未指定标识时回退为输入文件名（去扩展名）。
func defaultGameID(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func handleError(err error, tty bool) {
	fmt.Printf("\n❌ %v\n", err)
	if !tty {
		return
	}
	fmt.Println()
	fmt.Println("按回车键退出...")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}

func printUsage(selfPath string) {
	exe := filepath.Base(selfPath)
	fmt.Println("=== 多游戏补丁合并工具 ===")
	fmt.Println()
	fmt.Println("用法:")
	fmt.Printf("  %s -out <输出> <补丁1> <补丁2> [补丁3...]\n", exe)
	fmt.Printf("        [-id <补丁>=<游戏标识>]... [-label <补丁>=<名称>]...\n")
	fmt.Printf("        [-upgrader <升级工具>] [-restorer <恢复工具>]\n")
	fmt.Println()
	fmt.Println("交互模式:")
	fmt.Printf("  %s -shell\n", exe)
	fmt.Printf("  %s            （双击 / 无参数启动时默认进入）\n", exe)
	fmt.Println()
	fmt.Println("说明:")
	fmt.Println("  输入补丁必须是不带版本链的单段补丁（生成时未使用 -prev）。")
	fmt.Println("  合并结果会自动识别目标游戏，只应用匹配的那一份。")
	fmt.Println("  合并补丁不能作为 -prev 使用。")
}

// stringList 支持重复传入与逗号分隔的字符串参数。
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, part := range cliutil.SplitPathList(v) {
		if part = cliutil.CleanPath(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}