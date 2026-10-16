package patcher

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

const backupDirName = "_backup_before_patch"

// RunOptions 是应用补丁时的运行选项。
type RunOptions struct {
	// Target 指定合并补丁要应用的目标（gameID，大小写不敏感）。
	// 为空时依赖指纹自动识别；仅在多个目标同时命中（歧义）时才需要指定。
	Target string
}

// RunEmbedded 从 exePath 尾部读取补丁制品，自动识别游戏版本并执行升级。
//
// 普通补丁按版本链识别起点；合并补丁则先确定当前目录属于哪个目标，
// 再对该目标的单段差异执行同一套校验/备份/应用/回滚流程。
func RunEmbedded(exePath string, opts RunOptions) error {
	rel, err := readEmbeddedRelease(exePath)
	if err != nil {
		return err
	}
	gameDir := filepath.Dir(exePath)
	printReleaseSummary(gameDir, rel)

	if rel.Bundle != nil {
		return runBundle(gameDir, exePath, rel, opts)
	}
	return runSingleChain(gameDir, exePath, rel)
}

// runSingleChain 是原有单链补丁的应用流程。
func runSingleChain(gameDir, exePath string, rel *Release) error {
	idx, mismatches, err := DetectSource(gameDir, rel)
	if err != nil {
		return err
	}
	sources := rel.Payload.Sources()
	switch {
	case idx < 0:
		printMismatches(mismatches)
		return fmt.Errorf("游戏版本与补丁不匹配，已中止（未修改任何文件）")
	case idx == len(sources)-1:
		fmt.Printf("✅ 当前已经是最新版本（%s），无需升级。\n", versionDisplay(sources[idx].Index))
		return nil
	}

	fmt.Printf("🔎 检测到当前版本: %s\n", versionDisplay(sources[idx].Index))
	fmt.Printf("🎯 目标版本: %s\n", versionDisplay(sources[len(sources)-1].Index))
	if !confirmUpgrade() {
		return nil
	}

	stages := rel.Payload.StagesFrom(idx)
	target := rel.Payload.Target()
	return applyStages(gameDir, exePath, stages, target.Files)
}

// runBundle 是合并补丁的应用流程：先选定目标，再应用该目标的单段差异。
func runBundle(gameDir, exePath string, rel *Release, opts RunOptions) error {
	b := rel.Bundle
	matched, err := matchedBundleTargets(gameDir, b, opts.Target)
	if err != nil {
		return err
	}
	if len(matched) == 0 {
		_, mismatches, _ := detectBundleTarget(gameDir, b)
		printMismatches(mismatches)
		if opts.Target != "" {
			return fmt.Errorf("当前目录与目标 %q 不匹配，已中止（未修改任何文件）", opts.Target)
		}
		return fmt.Errorf("当前目录与合并补丁中的任何目标都不匹配，已中止（未修改任何文件）")
	}
	if len(matched) > 1 {
		names := make([]string, 0, len(matched))
		for i := range matched {
			names = append(names, matched[i].GameID)
		}
		fmt.Printf("❌ 同时匹配到 %d 个目标，无法确定应用哪一个：\n", len(matched))
		for _, name := range names {
			fmt.Printf("   • %s\n", name)
		}
		return fmt.Errorf("目标歧义，请用 -target <游戏标识> 指定（未修改任何文件）")
	}

	chosen := matched[0]
	fmt.Printf("🎯 目标: %s\n", chosen.DisplayName())
	idx, _, err := detectSourceForSteps(gameDir, chosen.Steps)
	if err != nil {
		return err
	}
	if idx == len(deriveVersionRefs(chosen.Steps))-1 {
		fmt.Printf("✅ 该目标已经是最新版本（%s），无需升级。\n", chosen.DisplayName())
		return nil
	}
	if !confirmUpgrade() {
		return nil
	}

	stages := []Stage{{
		SourceIndex: chosen.Steps[0].SourceIndex,
		Entries:     chosen.Steps[0].Entries,
		Pool:        b.Pool,
	}}
	target := deriveVersionRefs(chosen.Steps)[len(chosen.Steps)]
	return applyStages(gameDir, exePath, stages, target.Files)
}

// applyStages 执行"备份 → 逐段应用 → 整体校验"，失败时尽力回滚。
func applyStages(gameDir, exePath string, stages []Stage, targetFiles []FileState) error {
	backupDir, err := prepareBackupDir(gameDir)
	if err != nil {
		return err
	}
	releaseRestorer(exePath, backupDir)

	fmt.Println("🔧 正在升级...")
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
	errs := verifyStates(gameDir, targetFiles)
	dur := time.Since(verifyStart).Round(time.Millisecond)
	if len(errs) == 0 {
		fmt.Printf("✅ 升级完成! 验证通过 (%d 个文件, 耗时 %v)\n", len(targetFiles), dur)
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

func confirmUpgrade() bool {
	fmt.Println()
	fmt.Print("确认升级? (Y/n): ")
	var answer string
	fmt.Scanln(&answer)
	if strings.EqualFold(strings.TrimSpace(answer), "n") {
		fmt.Println("❌ 已取消。")
		return false
	}
	return true
}

func printMismatches(mismatches []SourceMismatch) {
	fmt.Println("❌ 未能识别当前游戏版本：")
	for _, m := range mismatches {
		fmt.Printf("   • %s\n", m.Reason)
	}
	fmt.Println()
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
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║       🎮 游戏升级工具                ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("📂 目标目录: %s\n", gameDir)

	if rel.Bundle != nil {
		names := make([]string, 0, len(rel.Bundle.Targets))
		for i := range rel.Bundle.Targets {
			names = append(names, rel.Bundle.Targets[i].DisplayName())
		}
		fmt.Printf("🏷️  合并补丁: 含 %d 个目标\n", len(names))
		for _, n := range names {
			fmt.Printf("   • %s\n", n)
		}
		fmt.Println()
		return
	}

	sources := rel.Payload.Sources()
	chain := make([]string, 0, len(sources))
	for i := range sources {
		chain = append(chain, versionDisplay(sources[i].Index))
	}
	fmt.Printf("🏷️  补丁版本: v%d  版本链: %s\n", rel.PatchVersion, strings.Join(chain, " → "))
	fmt.Printf("📊 可从 %d 个已知版本升级到 %s\n", len(sources)-1, chain[len(chain)-1])
	fmt.Println()
}

// versionDisplay 把版本序号渲染为面向用户的 v1/v2/v3。
//
// 补丁只用链内序号标识版本：不同版本的游戏目录名差异很大，记录名称既无判定价值
// 也无显示价值，因此制品里根本不存目录名。
func versionDisplay(index uint32) string {
	return fmt.Sprintf("v%d", index)
}