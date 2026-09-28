package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"

	"game-patcher/patcher"
)

var upgraderCandidates = []string{
	"game-patcher-upgrader-win64.exe",
	"game-patcher-upgrader-win64",
	"game-patcher-upgrader.exe",
	"game-patcher-upgrader",
	"upgrader.exe",
	"upgrader",
}

var restorerCandidates = []string{
	"restorer-win64.exe",
	"restorer-win64",
	"restorer.exe",
	"restorer",
}

func main() {
	selfPath, err := os.Executable()
	if err != nil {
		patcher.Fatal("❌ 获取自身路径失败: %v", err)
	}

	// 若自身被追加了补丁数据，则直接作为升级器运行（兜底）。
	if patcher.HasEmbeddedPatch(selfPath) {
		if err := patcher.RunEmbedded(selfPath); err != nil {
			fmt.Printf("\n❌ %v\n", err)
			patcher.Pause()
			os.Exit(1)
		}
		patcher.Pause()
		return
	}

	oldPath := flag.String("old", "", "旧版本文件夹路径")
	newPath := flag.String("new", "", "新版本文件夹路径")
	output := flag.String("out", "", "输出升级工具路径（默认: 当前目录/game-updater.exe）")
	upgraderFlag := flag.String("upgrader", "", "升级工具基础程序路径（默认: 自动查找）")
	restorerFlag := flag.String("restorer", "", "恢复工具二进制路径（默认: 自动查找）")
	shellMode := flag.Bool("shell", false, "进入交互模式（双击或无参数启动时默认进入）")
	flag.Parse()

	// 双击直接启动（无任何参数、标准输入为终端）时默认进入交互模式。
	// 结束后暂停，避免控制台窗口一闪而过。
	launchedDirectly := launchedByDoubleClick(*shellMode, flag.NFlag(), flag.NArg(), term.IsTerminal(int(os.Stdin.Fd())))
	if *shellMode || launchedDirectly {
		if err := runInteractive(selfPath); err != nil {
			handleCreateError(err, launchedDirectly)
		}
		if launchedDirectly {
			patcher.Pause()
		}
		return
	}

	if *oldPath != "" && *newPath != "" {
		if *output == "" {
			*output = "game-updater.exe"
		}
		absOld, err := filepath.Abs(*oldPath)
		if err != nil {
			patcher.Fatal("❌ 旧版本路径无效: %v", err)
		}
		absNew, err := filepath.Abs(*newPath)
		if err != nil {
			patcher.Fatal("❌ 新版本路径无效: %v", err)
		}
		absOut, err := filepath.Abs(*output)
		if err != nil {
			patcher.Fatal("❌ 输出路径无效: %v", err)
		}
		upgraderPath := findBinary(selfPath, *upgraderFlag, upgraderCandidates)
		if upgraderPath == "" {
			patcher.Fatal("❌ 找不到升级工具基础程序 (upgrader)，请使用 -upgrader 指定路径")
		}
		restorerPath := findBinary(selfPath, *restorerFlag, restorerCandidates)
		if err := patcher.CreatePatch(upgraderPath, absOld, absNew, absOut, restorerPath); err != nil {
			handleCreateError(err, false)
		}
		return
	}

	if *oldPath != "" || *newPath != "" {
		patcher.Fatal("❌ -old 和 -new 必须同时提供")
	}

	printUsage()
	os.Exit(1)
}

// launchedByDoubleClick 判断是否为“双击直接启动”：
// 未显式指定 -shell、未传入任何参数，且标准输入是交互式终端。
// 非终端（管道 / CI / 重定向）返回 false，继续按脚本方式打印用法。
func launchedByDoubleClick(shellFlag bool, flagCount, argCount int, stdinIsTTY bool) bool {
	return !shellFlag && flagCount == 0 && argCount == 0 && stdinIsTTY
}

// findBinary 查找二进制：优先用户指定，否则在 CLI 同目录按候选名查找。
func findBinary(selfPath, userSpecified string, candidates []string) string {
	if userSpecified != "" {
		if _, err := os.Stat(userSpecified); err != nil {
			fmt.Printf("⚠️  指定的文件不存在: %s\n", userSpecified)
			return ""
		}
		abs, _ := filepath.Abs(userSpecified)
		return abs
	}
	selfDir := filepath.Dir(selfPath)
	for _, name := range candidates {
		candidate := filepath.Join(selfDir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func runInteractive(selfPath string) error {
	r := bufio.NewReader(os.Stdin)

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║   🎮 二进制升级包制作工具 (交互)     ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()

	oldPath, err := promptPath(r, "旧版本文件夹路径")
	if err != nil {
		return err
	}
	newPath, err := promptPath(r, "新版本文件夹路径")
	if err != nil {
		return err
	}

	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		return fmt.Errorf("旧版本路径无效: %w", err)
	}
	newInfo, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("新版本路径无效: %w", err)
	}
	if !oldInfo.IsDir() || !newInfo.IsDir() {
		return errors.New("仅支持目录模式：旧版本与新版本必须都是文件夹")
	}
	output := promptOutput(r, selfPath)

	upgraderPath := findBinary(selfPath, "", upgraderCandidates)
	if upgraderPath == "" {
		return errors.New("找不到升级工具基础程序 (upgrader)，请将其放在 CLI 同目录下")
	}
	restorerPath := findBinary(selfPath, "", restorerCandidates)

	fmt.Println()
	fmt.Printf("  旧版本: %s\n", oldPath)
	fmt.Printf("  新版本: %s\n", newPath)
	fmt.Printf("  输  出: %s\n", output)
	fmt.Printf("  升级工具基础: %s\n", filepath.Base(upgraderPath))
	if restorerPath != "" {
		fmt.Printf("  恢复工具: %s\n", filepath.Base(restorerPath))
	} else {
		fmt.Println("  恢复工具: 未找到（升级工具将不包含恢复功能）")
	}
	fmt.Println()

	return patcher.CreatePatch(upgraderPath, oldPath, newPath, output, restorerPath)
}

func promptPath(r *bufio.Reader, label string) (string, error) {
	for {
		fmt.Printf("📂 %s: ", label)
		line, err := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			if err != nil {
				return "", fmt.Errorf("输入已结束，未获取到%s", label)
			}
			continue
		}
		abs, err := filepath.Abs(line)
		if err != nil {
			fmt.Printf("   ⚠️  路径无效: %v\n\n", err)
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			fmt.Printf("   ⚠️  路径不存在: %s\n\n", abs)
			continue
		}
		return abs, nil
	}
}

func promptOutput(r *bufio.Reader, selfPath string) string {
	defaultOut := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	for {
		fmt.Printf("💾 输出文件路径（回车默认 [%s]）: ", defaultOut)
		line, err := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultOut
		}
		abs, err := filepath.Abs(line)
		if err != nil {
			fmt.Printf("   ⚠️  路径无效: %v\n\n", err)
			continue
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			abs = filepath.Join(abs, "game-updater.exe")
		}
		return abs
	}
}

func printUsage() {
	exe := filepath.Base(os.Args[0])
	fmt.Println("=== 二进制增量升级包制作工具 ===")
	fmt.Println()
	fmt.Println("参数模式:")
	fmt.Printf("  %s -old <旧版本目录> -new <新版本目录> [-out <输出>]\n", exe)
	fmt.Printf("        [-upgrader <升级工具>] [-restorer <恢复工具>]\n")
	fmt.Println()
	fmt.Println("交互模式:")
	fmt.Printf("  %s -shell\n", exe)
	fmt.Printf("  %s                        （双击 / 无参数启动时默认进入）\n", exe)
	fmt.Println()
	fmt.Println("说明:")
	fmt.Println("  -old/-new 必须是两个版本的游戏目录，程序按目录树对比并生成二进制增量补丁。")
	fmt.Println("  扫描后会列出所有变更文件，使用 ↑/↓ 移动、空格 选择/取消、回车 生成补丁。")
	fmt.Println("  非交互终端（管道/CI）会自动包含全部变更文件。")
	fmt.Println("  生成的升级工具放入游戏根目录，直接运行即可完成升级。")
	fmt.Printf("平台: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}

// handleCreateError 统一处理补丁生成错误；用户在选择界面取消时友好退出。
// keepOpen 为真（双击启动）时先暂停，避免控制台窗口立即关闭。
func handleCreateError(err error, keepOpen bool) {
	if errors.Is(err, patcher.ErrCancelled) {
		fmt.Println("❌ 已取消，未生成补丁。")
		if keepOpen {
			patcher.Pause()
		}
		os.Exit(1)
	}
	if keepOpen {
		fmt.Printf("\n❌ %v\n", err)
		patcher.Pause()
		os.Exit(1)
	}
	patcher.Fatal("❌ 创建失败: %v", err)
}
