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
			patcher.Fatal("❌ %v", err)
		}
		return
	}

	oldPath := flag.String("old", "", "旧版本路径（文件或目录）")
	newPath := flag.String("new", "", "新版本路径（文件或目录）")
	output := flag.String("out", "", "输出升级工具路径（默认: 当前目录/game-updater.exe）")
	target := flag.String("target", "", "单文件模式的目标相对路径（默认: 新文件名）")
	upgraderFlag := flag.String("upgrader", "", "升级工具基础程序路径（默认: 自动查找）")
	restorerFlag := flag.String("restorer", "", "恢复工具二进制路径（默认: 自动查找）")
	shellMode := flag.Bool("shell", false, "进入交互模式")
	flag.Parse()

	if *shellMode {
		runInteractive(selfPath)
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
		if err := patcher.CreatePatch(upgraderPath, absOld, absNew, absOut, *target, restorerPath); err != nil {
			patcher.Fatal("❌ 创建失败: %v", err)
		}
		return
	}

	if *oldPath != "" || *newPath != "" {
		patcher.Fatal("❌ -old 和 -new 必须同时提供")
	}

	printUsage()
	os.Exit(1)
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

func runInteractive(selfPath string) {
	r := bufio.NewReader(os.Stdin)

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║   🎮 二进制升级包制作工具 (交互)     ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()

	oldPath := promptPath(r, "旧版本路径（文件或目录）")
	newPath := promptPath(r, "新版本路径（文件或目录）")

	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		patcher.Fatal("❌ 旧版本路径无效: %v", err)
	}
	newInfo, err := os.Stat(newPath)
	if err != nil {
		patcher.Fatal("❌ 新版本路径无效: %v", err)
	}
	if oldInfo.IsDir() != newInfo.IsDir() {
		patcher.Fatal("❌ 旧版本与新版本必须同为文件或同为目录")
	}

	target := ""
	if !newInfo.IsDir() {
		target = promptTarget(r, filepath.Base(newPath))
	}
	output := promptOutput(r, selfPath)

	upgraderPath := findBinary(selfPath, "", upgraderCandidates)
	if upgraderPath == "" {
		patcher.Fatal("❌ 找不到升级工具基础程序 (upgrader)，请将其放在 CLI 同目录下")
	}
	restorerPath := findBinary(selfPath, "", restorerCandidates)

	fmt.Println()
	fmt.Printf("  旧版本: %s\n", oldPath)
	fmt.Printf("  新版本: %s\n", newPath)
	if target != "" {
		fmt.Printf("  目标文件: %s\n", target)
	}
	fmt.Printf("  输  出: %s\n", output)
	fmt.Printf("  升级工具基础: %s\n", filepath.Base(upgraderPath))
	if restorerPath != "" {
		fmt.Printf("  恢复工具: %s\n", filepath.Base(restorerPath))
	} else {
		fmt.Println("  恢复工具: 未找到（升级工具将不包含恢复功能）")
	}
	fmt.Println()

	if err := patcher.CreatePatch(upgraderPath, oldPath, newPath, output, target, restorerPath); err != nil {
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
		if _, err := os.Stat(abs); err != nil {
			fmt.Printf("   ⚠️  路径不存在: %s\n\n", abs)
			continue
		}
		return abs
	}
}

func promptTarget(r *bufio.Reader, defaultTarget string) string {
	fmt.Printf("🎯 目标相对路径（回车默认 [%s]）: ", defaultTarget)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultTarget
	}
	return line
}

func promptOutput(r *bufio.Reader, selfPath string) string {
	defaultOut := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	for {
		fmt.Printf("💾 输出文件路径（回车默认 [%s]）: ", defaultOut)
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
	fmt.Println("=== 二进制增量升级包制作工具 ===")
	fmt.Println()
	fmt.Println("参数模式:")
	fmt.Printf("  %s -old <旧版本> -new <新版本> [-out <输出>] [-target <目标相对路径>]\n", exe)
	fmt.Printf("        [-upgrader <升级工具>] [-restorer <恢复工具>]\n")
	fmt.Println()
	fmt.Println("交互模式:")
	fmt.Printf("  %s -shell\n", exe)
	fmt.Println()
	fmt.Println("说明:")
	fmt.Println("  -old/-new 同为目录时生成目录树补丁；同为文件时生成单文件二进制增量补丁。")
	fmt.Println("  单文件模式下 -target 指定目标相对路径，默认取新文件名。")
	fmt.Println("  生成的升级工具放入目标目录，直接运行即可完成升级。")
	fmt.Printf("平台: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
