package patcher

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const backupDirName = "_backup_before_patch"

// RunEmbedded 从 exePath 尾部读取补丁制品，自动识别游戏版本并执行升级。
func RunEmbedded(exePath string) error {
	rel, err := readEmbeddedRelease(exePath)
	if err != nil {
		return err
	}
	gameDir := filepath.Dir(exePath)
	printReleaseSummary(gameDir, rel)

	idx, mismatches, err := DetectSource(gameDir, rel)
	if err != nil {
		return err
	}
	sources := rel.Payload.Sources()
	switch {
	case idx < 0:
		fmt.Println("❌ 未能识别当前游戏版本：")
		for _, m := range mismatches {
			fmt.Printf("   • %s: %s\n", versionLabelOf(m.Label, m.VersionIndex), m.Reason)
		}
		fmt.Println()
		return fmt.Errorf("游戏版本与补丁不匹配，已中止（未修改任何文件）")
	case idx == len(sources)-1:
		fmt.Printf("✅ 当前已经是最新版本（%s），无需升级。\n", versionLabelOf(sources[idx].Label, sources[idx].Index))
		return nil
	}

	fmt.Printf("🔎 检测到当前版本: %s\n", versionLabelOf(sources[idx].Label, sources[idx].Index))
	fmt.Printf("🎯 目标版本: %s\n", versionLabelOf(sources[len(sources)-1].Label, sources[len(sources)-1].Index))
	fmt.Println()
	fmt.Print("确认升级? (Y/n): ")
	var answer string
	fmt.Scanln(&answer)
	if strings.EqualFold(strings.TrimSpace(answer), "n") {
		fmt.Println("❌ 已取消。")
		return nil
	}
	fmt.Println()

	backupDir, err := prepareBackupDir(gameDir)
	if err != nil {
		return err
	}
	releaseRestorer(exePath, backupDir)

	fmt.Println("🔧 正在升级...")
	stages := rel.Payload.StagesFrom(idx)
	manifest, err := ApplyRelease(gameDir, backupDir, stages)
	if err != nil {
		fmt.Printf("\n❌ 升级失败: %v\n", err)
		fmt.Println("↩️  正在尝试回滚...")
		if rerr := Restore(manifest, backupDir); rerr != nil {
			fmt.Printf("❌ 自动回滚未完全成功: %v\n", rerr)
			fmt.Println("💡 请手动运行备份目录中的恢复工具。")
		} else {
			fmt.Println("✅ 已回滚到升级前状态。")
		}
		return err
	}

	fmt.Println()
	fmt.Println("🔍 验证文件完整性...")
	verifyStart := time.Now()
	target := rel.Payload.Target()
	errs := verifyStates(gameDir, target.Files)
	dur := time.Since(verifyStart).Round(time.Millisecond)
	if len(errs) == 0 {
		fmt.Printf("✅ 升级完成! 验证通过 (%d 个文件, 耗时 %v)\n", len(target.Files), dur)
	} else {
		fmt.Printf("⚠️  升级完成，但 %d 个文件校验失败 (耗时 %v)\n", len(errs), dur)
		for _, e := range errs {
			fmt.Printf("   ❌ %v\n", e)
		}
		fmt.Println("💡 建议运行备份目录中的恢复工具回滚。")
		return fmt.Errorf("%d 个文件校验失败", len(errs))
	}

	fmt.Println()
	fmt.Printf("💡 如需恢复旧版本，请运行 %s/ 中的恢复工具。\n", backupDirName)
	return nil
}

func releaseRestorer(exePath, backupDir string) {
	data, err := extractRestorer(exePath)
	if err != nil {
		fmt.Printf("⚠️  未找到嵌入的恢复工具: %v\n", err)
		return
	}
	name := restorerFileName()
	if err := writeAtomic(filepath.Join(backupDir, name), func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}); err != nil {
		fmt.Printf("⚠️  释放恢复工具失败: %v\n", err)
		return
	}
	fmt.Printf("📦 恢复工具已放置: %s/%s\n", backupDirName, name)
}

func printReleaseSummary(gameDir string, rel *Release) {
	sources := rel.Payload.Sources()
	labels := make([]string, 0, len(sources))
	for i := range sources {
		labels = append(labels, versionLabelOf(sources[i].Label, sources[i].Index))
	}
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║       🎮 游戏升级工具                ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("📂 目标目录: %s\n", gameDir)
	fmt.Printf("🏷️  补丁版本: v%d  版本链: %s\n", rel.PatchVersion, strings.Join(labels, " → "))
	fmt.Printf("📊 可从 %d 个已知版本升级到 %s\n", len(sources)-1, labels[len(labels)-1])
	fmt.Println()
}

func versionLabelOf(label string, index uint32) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	return fmt.Sprintf("v%d", index)
}
