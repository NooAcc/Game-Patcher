package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"game-patcher/patcher"
)

func main() {
	exePath, _ := os.Executable()
	exeDir := filepath.Dir(exePath)

	// 恢复清单路径
	manifestPath := filepath.Join(exeDir, "restore.json")

	fmt.Println("╔══════════════════════════════════╗")
	fmt.Println("║        游戏版本恢复工具          ║")
	fmt.Println("╚══════════════════════════════════╝")
	fmt.Println()

	// 读取恢复清单
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		patcher.Fatal("❌ 无法读取恢复清单: %v\n请确保 restore.json 文件与此工具在同一目录。", err)
	}

	var manifest patcher.RestoreManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		patcher.Fatal("❌ 解析恢复清单失败: %v", err)
	}

	gameDir := manifest.GameDir
	if gameDir == "" {
		// 默认游戏目录为备份目录的上一级
		gameDir = filepath.Dir(exeDir)
	}

	fmt.Printf("📋 将恢复操作: %s → %s\n", manifest.Operations[0].Path, "旧版本")
	fmt.Printf("📁 游戏目录: %s\n", gameDir)
	fmt.Printf("📦 备份目录: %s\n", exeDir)
	fmt.Println()

	// 计算各类操作数
	var addOps, updateOps, deleteOps int
	for _, op := range manifest.Operations {
		switch op.Action {
		case "add":
			addOps++
		case "update":
			updateOps++
		case "delete":
			deleteOps++
		}
	}
	fmt.Printf("📊 将执行: -%d 删除(回滚新增), ~%d 恢复(回滚修改), +%d 恢复(回滚删除)\n", addOps, updateOps, deleteOps)
	fmt.Println()

	// 确认
	fmt.Print("⚠️  确认恢复到旧版本？(y/N): ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("❌ 已取消恢复。")
		fmt.Println("\n按回车键退出...")
		buf := make([]byte, 1)
		os.Stdin.Read(buf)
		return
	}

	fmt.Println()
	fmt.Println("🔧 正在恢复...")
	total := len(manifest.Operations)

	for i, op := range manifest.Operations {
		full := filepath.Join(gameDir, filepath.FromSlash(op.Path))
		backupPath := filepath.Join(exeDir, filepath.FromSlash(op.Path))
		tag := fmt.Sprintf("[%d/%d]", i+1, total)

		switch op.Action {
		case "add":
			// 回滚新增：删除新版本中新增的文件
			fmt.Printf("   %s - 删除 %s\n", tag, op.Path)
			os.Remove(full)
			patcher.CleanupEmpty(filepath.Dir(full), gameDir)

		case "update":
			// 回滚修改：从备份恢复旧文件
			fmt.Printf("   %s ~ 恢复 %s\n", tag, op.Path)
			if _, err := os.Stat(backupPath); err == nil {
				patcher.CopyFile(backupPath, full)
			} else {
				fmt.Printf("      ⚠️  备份不存在: %s\n", op.Path)
			}

		case "delete":
			// 回滚删除：从备份恢复被删除的文件
			fmt.Printf("   %s + 恢复 %s\n", tag, op.Path)
			if _, err := os.Stat(backupPath); err == nil {
				os.MkdirAll(filepath.Dir(full), 0755)
				patcher.CopyFile(backupPath, full)
			} else {
				fmt.Printf("      ⚠️  备份不存在: %s\n", op.Path)
			}
		}
	}

	// 验证
	fmt.Println()
	fmt.Println("🔍 验证中...")
	var errCount int
	for _, op := range manifest.Operations {
		full := filepath.Join(gameDir, filepath.FromSlash(op.Path))
		switch op.Action {
		case "add":
			// 新增的文件应该已被删除
			if _, err := os.Stat(full); !os.IsNotExist(err) {
				fmt.Printf("   ❌ 未删除: %s\n", op.Path)
				errCount++
			}
		case "update", "delete":
			// 修改/删除的文件应该已恢复
			if _, err := os.Stat(full); err != nil {
				fmt.Printf("   ❌ 未恢复: %s\n", op.Path)
				errCount++
			}
		}
	}

	fmt.Println()
	if errCount > 0 {
		fmt.Printf("⚠️  恢复完成，但 %d 个文件验证失败。\n", errCount)
	} else {
		fmt.Println("✅ 恢复完成！游戏已恢复到旧版本。")
	}

	fmt.Println()
	if runtime.GOOS == "windows" {
		fmt.Println("💡 建议删除 _backup_before_patch 文件夹以释放空间。")
	} else {
		fmt.Println("💡 建议删除 _backup_before_patch 文件夹以释放空间。")
	}
	fmt.Println()
	fmt.Println("按回车键退出...")
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
}
