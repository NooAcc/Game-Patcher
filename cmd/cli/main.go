package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"game-patcher/patcher"
)

func main() {
	selfPath, _ := os.Executable()

	// 如果自身已嵌入补丁数据，直接运行升级
	// （CLI 本身不会嵌入补丁，但作为安全兜底保留此检查）
	if patcher.HasEmbeddedPatch(selfPath) {
		patcher.RunEmbedded(selfPath)
		return
	}

	// ── 参数模式 ────────────────────────────────────────────
	oldDir := flag.String("old", "", "旧版本文件夹路径")
	newDir := flag.String("new", "", "新版本文件夹路径")
	output := flag.String("out", "", "输出升级工具路径（默认: 当前目录/game-updater.exe）")
	upgraderFlag := flag.String("upgrader", "", "升级工具基础程序路径（默认: 自动查找同目录下的 upgrader）")
	restorerFlag := flag.String("restorer", "", "恢复工具二进制路径（默认: 自动查找同目录下的 restorer）")
	shellMode := flag.Bool("shell", false, "进入交互模式")
	flag.Parse()

	// 优先: -shell → 交互模式
	if *shellMode {
		runInteractive(selfPath)
		return
	}

	// -old 和 -new 都提供了 → 参数模式
	if *oldDir != "" && *newDir != "" {
		if *output == "" {
			*output = "game-updater.exe"
		}
		absOld, err := filepath.Abs(*oldDir)
		if err != nil {
			patcher.Fatal("❌ 旧版本路径无效: %v", err)
		}
		absNew, err := filepath.Abs(*newDir)
		if err != nil {
			patcher.Fatal("❌ 新版本路径无效: %v", err)
		}
		absOut, err := filepath.Abs(*output)
		if err != nil {
			patcher.Fatal("❌ 输出路径无效: %v", err)
		}
		upgraderPath := findBinary(selfPath, *upgraderFlag, []string{
			"game-patcher-upgrader-win64.exe",
			"game-patcher-upgrader-win64",
			"game-patcher-upgrader.exe",
			"game-patcher-upgrader",
			"upgrader.exe",
			"upgrader",
		})
		if upgraderPath == "" {
			patcher.Fatal("❌ 找不到升级工具基础程序 (upgrader)，请使用 -upgrader 指定路径")
		}
		restorerPath := findBinary(selfPath, *restorerFlag, []string{
			"restorer-win64.exe",
			"restorer-win64",
			"restorer.exe",
			"restorer",
		})
		if err := patcher.CreatePatch(upgraderPath, absOld, absNew, absOut, restorerPath); err != nil {
			patcher.Fatal("❌ 创建失败: %v", err)
		}
		return
	}

	// -old 或 -new 只提供了一个 → 报错
	if *oldDir != "" || *newDir != "" {
		patcher.Fatal("❌ -old 和 -new 必须同时提供")
	}

	// 啥都没给 → 显示用法
	printUsage()
	os.Exit(1)
}

// findBinary 查找二进制文件
// 优先使用用户指定路径，否则在 CLI 同目录下按候选名查找
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

// runInteractive 交互式引导用户输入路径
func runInteractive(selfPath string) {
	r := bufio.NewReader(os.Stdin)

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║   🎮 游戏升级包制作工具 (交互模式)   ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()

	oldDir := promptPath(r, "请输入旧版本文件夹路径")
	newDir := promptPath(r, "请输入新版本文件夹路径")
	output := promptOutput(r, selfPath)

	upgraderPath := findBinary(selfPath, "", []string{
		"game-patcher-upgrader-win64.exe",
		"game-patcher-upgrader-win64",
		"game-patcher-upgrader.exe",
		"game-patcher-upgrader",
		"upgrader.exe",
		"upgrader",
	})
	if upgraderPath == "" {
		patcher.Fatal("❌ 找不到升级工具基础程序 (upgrader)，请将其放在 CLI 同目录下")
	}
	restorerPath := findBinary(selfPath, "", []string{
		"restorer-win64.exe",
		"restorer-win64",
		"restorer.exe",
		"restorer",
	})

	fmt.Println()
	fmt.Printf("  旧版本: %s\n", oldDir)
	fmt.Printf("  新版本: %s\n", newDir)
	fmt.Printf("  输  出: %s\n", output)
	fmt.Printf("  升级工具基础: %s\n", filepath.Base(upgraderPath))
	if restorerPath != "" {
		fmt.Printf("  恢复工具: %s\n", filepath.Base(restorerPath))
	} else {
		fmt.Println("  恢复工具: 未找到（升级工具将不包含恢复功能）")
	}
	fmt.Println()

	if err := patcher.CreatePatch(upgraderPath, oldDir, newDir, output, restorerPath); err != nil {
		patcher.Fatal("❌ 创建失败: %v", err)
	}
}

func promptPath(r *bufio.Reader, label string) string {
	for {
		fmt.Printf("📂 %s: ", label)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		abs, err := filepath.Abs(line)
		if err != nil {
			fmt.Printf("   ⚠️  路径无效: %v\n\n", err)
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			fmt.Printf("   ⚠️  路径不存在: %s\n\n", abs)
			continue
		}
		if !info.IsDir() {
			fmt.Printf("   ⚠️  不是文件夹: %s\n\n", abs)
			continue
		}
		return abs
	}
}

func promptOutput(r *bufio.Reader, selfPath string) string {
	defaultOut := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	for {
		fmt.Printf("💾 输出文件路径 (回车使用默认 [%s]): ", defaultOut)
		line, _ := r.ReadString('\n')
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
	fmt.Println("=== 游戏升级包制作工具 ===")
	fmt.Println()
	fmt.Println("参数模式:")
	fmt.Printf("  %s -old <旧版本> -new <新版本> [-out <输出>] [-upgrader <升级工具>] [-restorer <恢复工具>]\n", exe)
	fmt.Println()
	fmt.Println("交互模式:")
	fmt.Printf("  %s -shell\n", exe)
	fmt.Println()
	fmt.Println("示例:")
	fmt.Printf("  %s -old ./v1.0 -new ./v1.1\n", exe)
	fmt.Printf("  %s -old ./v1.0 -new ./v1.1 -out ./game-updater.exe\n", exe)
	fmt.Printf("  %s -shell\n", exe)
	fmt.Println()
	fmt.Println("生成的升级工具放入游戏根目录，直接运行即可完成升级。")
	fmt.Println("升级工具和恢复工具会自动从 CLI 同目录下查找并嵌入。")
	fmt.Printf("平台: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
