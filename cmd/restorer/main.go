package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"game-patcher/patcher"
)

func main() {
	exePath, err := os.Executable()
	if err != nil {
		patcher.Fatal("❌ 获取自身路径失败: %v", err)
	}
	backupDir := filepath.Dir(exePath)
	gameDir := filepath.Dir(backupDir)

	fmt.Println("╔══════════════════════════════════╗")
	fmt.Println("║        游戏版本恢复工具          ║")
	fmt.Println("╚══════════════════════════════════╝")
	fmt.Println()

	manifest, err := patcher.LoadRestoreManifest(backupDir)
	if err != nil {
		patcher.Fatal("❌ %v", err)
	}

	var adds, updates, dels int
	for i := range manifest.Entries {
		switch manifest.Entries[i].Action {
		case "add":
			adds++
		case "update":
			updates++
		case "delete":
			dels++
		}
	}

	fmt.Printf("📁 游戏目录: %s\n", gameDir)
	fmt.Printf("📦 备份目录: %s\n", backupDir)
	fmt.Printf("📊 将执行: -%d 删除(回滚新增), ~%d 恢复(回滚修改), +%d 恢复(回滚删除)\n", adds, updates, dels)
	fmt.Println()

	fmt.Print("⚠️  确认恢复到旧版本？(y/N): ")
	var confirm string
	fmt.Scanln(&confirm)
	if !strings.EqualFold(strings.TrimSpace(confirm), "y") {
		fmt.Println("❌ 已取消恢复。")
		pause()
		return
	}

	fmt.Println()
	fmt.Println("🔧 正在恢复...")
	if err := patcher.Restore(manifest, backupDir); err != nil {
		fmt.Printf("⚠️  恢复过程中出现问题:\n%v\n", err)
		fmt.Println("💡 可修正问题后重新运行本工具。")
	} else {
		fmt.Println("✅ 恢复完成！游戏已恢复到旧版本。")
	}
	fmt.Println()
	fmt.Println("💡 确认无误后可删除 _backup_before_patch 文件夹以释放空间。")
	pause()
}

func pause() {
	fmt.Println("按回车键退出...")
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
}
