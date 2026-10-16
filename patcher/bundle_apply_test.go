package patcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree 把给定相对路径与内容写入目录树。
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeFakeBaseExe 生成一个最小可用的载体文件（只需能被追加补丁尾部）。
func writeFakeBaseExe(t *testing.T) string {
	t.Helper()
	dir := tempWorkDir(t)
	base := filepath.Join(dir, "base.exe")
	if err := os.WriteFile(base, []byte("MZ-fake-upgrader"), 0755); err != nil {
		t.Fatal(err)
	}
	return base
}

// buildTargetWithPool 用真实的"目录对"构造一个合并目标，并把该目标的字面量池交回调用方。
func buildTargetWithPool(t *testing.T, gameID, oldDir, newDir string, pool *blobPool) BundleTarget {
	t.Helper()
	jobs, err := planStep(oldDir, newDir, "")
	if err != nil {
		t.Fatalf("planStep 失败: %v", err)
	}
	entries, err := buildChunkStepEntries(oldDir, newDir, jobs, pool)
	if err != nil {
		t.Fatalf("buildChunkStepEntries 失败: %v", err)
	}
	return BundleTarget{
		GameID: gameID,
		Label:  gameID,
		Steps:  []ChainStep{{SourceIndex: 1, Entries: entries}},
	}
}

// hashOfPath 返回文件内容的 BLAKE3 十六进制。
func hashOfPath(t *testing.T, path string) string {
	t.Helper()
	h, err := HashFile(path)
	if err != nil {
		t.Fatalf("哈希 %s 失败: %v", path, err)
	}
	return hashHex(h)
}

// bundleFromTargets 按 merge 的池语义把多个目标组装成合并补丁。
func bundleFromTargets(t *testing.T, targets []BundleTarget, pools [][]Blob) *Release {
	t.Helper()
	inputs := make([]MergeInput, 0, len(targets))
	for i := range targets {
		inputs = append(inputs, MergeInput{
			Name:  targets[i].GameID,
			Label: targets[i].Label,
			Release: &Release{PatchVersion: 1, Payload: &ChunkPayload{
				Steps: targets[i].Steps,
				Pool:  pools[i],
			}},
		})
	}
	rel, err := MergeReleases(inputs)
	if err != nil {
		t.Fatalf("MergeReleases 失败: %v", err)
	}
	return rel
}

// TestBundleApplyTargetsOnlyItsOwnFiles 覆盖 AC2/AC3：在 A 目录应用只改 A 的文件。
func TestBundleApplyTargetsOnlyItsOwnFiles(t *testing.T) {
	oldA, newA := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldA, map[string]string{"shared.bin": "a-old", "only-a.txt": "A1"})
	writeTree(t, newA, map[string]string{"shared.bin": "a-old", "only-a.txt": "A2"})

	oldB, newB := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldB, map[string]string{"shared.bin": "b-old", "only-b.txt": "B1"})
	writeTree(t, newB, map[string]string{"shared.bin": "b-old", "only-b.txt": "B2"})

	poolA, poolB := newBlobPool(), newBlobPool()
	targetA := buildTargetWithPool(t, "game-a", oldA, newA, poolA)
	targetB := buildTargetWithPool(t, "game-b", oldB, newB, poolB)
	rel := bundleFromTargets(t, []BundleTarget{targetA, targetB}, [][]Blob{poolA.blobs, poolB.blobs})

	// 玩家目录 = A 的旧版本状态
	applyDir := tempWorkDir(t)
	writeTree(t, applyDir, map[string]string{"shared.bin": "a-old", "only-a.txt": "A1"})
	sharedBefore := hashOfPath(t, filepath.Join(applyDir, "shared.bin"))

	idx, mismatches, err := detectBundleTarget(applyDir, rel.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if idx < 0 {
		t.Fatalf("应识别出 game-a 目标: %+v", mismatches)
	}
	if rel.Bundle.Targets[idx].GameID != "game-a" {
		t.Fatalf("应命中 game-a，实际 %q", rel.Bundle.Targets[idx].GameID)
	}

	backup := filepath.Join(applyDir, backupDirName)
	if err := os.MkdirAll(backup, 0755); err != nil {
		t.Fatal(err)
	}
	stages := []Stage{{
		SourceIndex: 1,
		Entries:     rel.Bundle.Targets[idx].Steps[0].Entries,
		Pool:        rel.Bundle.Pool,
	}}
	if _, err := ApplyRelease(applyDir, backup, stages); err != nil {
		t.Fatalf("应用 game-a 目标失败: %v", err)
	}
	assertFileContent(t, filepath.Join(applyDir, "only-a.txt"), "A2")
	if got := hashOfPath(t, filepath.Join(applyDir, "shared.bin")); got != sharedBefore {
		t.Fatal("不应改动共享文件")
	}
	if _, err := os.Stat(filepath.Join(applyDir, "only-b.txt")); !os.IsNotExist(err) {
		t.Fatal("不应创建 B 的文件")
	}
}

// TestBundleDetectRejectsUnknownGame 覆盖 AC4：无关目录不应命中任何目标。
func TestBundleDetectRejectsUnknownGame(t *testing.T) {
	oldA, newA := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldA, map[string]string{"only-a.txt": "A1"})
	writeTree(t, newA, map[string]string{"only-a.txt": "A2"})
	oldB, newB := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldB, map[string]string{"only-b.txt": "B1"})
	writeTree(t, newB, map[string]string{"only-b.txt": "B2"})

	poolA, poolB := newBlobPool(), newBlobPool()
	targetA := buildTargetWithPool(t, "game-a", oldA, newA, poolA)
	targetB := buildTargetWithPool(t, "game-b", oldB, newB, poolB)
	rel := bundleFromTargets(t, []BundleTarget{targetA, targetB}, [][]Blob{poolA.blobs, poolB.blobs})

	stranger := tempWorkDir(t)
	writeTree(t, stranger, map[string]string{"unrelated.txt": "nothing"})
	idx, mismatches, err := detectBundleTarget(stranger, rel.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	if idx != -1 {
		t.Fatalf("无关目录不应命中任何目标，实际下标 %d", idx)
	}
	if len(mismatches) != 2 {
		t.Fatalf("应给出 2 条失败原因，实际 %d", len(mismatches))
	}
}

// TestBundleDetectAmbiguity 覆盖决策 D1：多目标同时命中必须能识别为歧义。
func TestBundleDetectAmbiguity(t *testing.T) {
	oldA, newA := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldA, map[string]string{"same.txt": "v1"})
	writeTree(t, newA, map[string]string{"same.txt": "v2"})
	oldB, newB := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldB, map[string]string{"same.txt": "v1"})
	writeTree(t, newB, map[string]string{"same.txt": "v2"})

	poolA, poolB := newBlobPool(), newBlobPool()
	targetA := buildTargetWithPool(t, "game-a", oldA, newA, poolA)
	targetB := buildTargetWithPool(t, "game-b", oldB, newB, poolB)
	rel := bundleFromTargets(t, []BundleTarget{targetA, targetB}, [][]Blob{poolA.blobs, poolB.blobs})

	applyDir := tempWorkDir(t)
	writeTree(t, applyDir, map[string]string{"same.txt": "v1"})
	matched, mErr := matchedBundleTargets(applyDir, rel.Bundle, "")
	if mErr != nil {
		t.Fatal(mErr)
	}
	if len(matched) != 2 {
		t.Fatalf("两个目标应同时命中，实际 %d 个", len(matched))
	}

	// 指定 -target 后应只剩一个候选
	matched2, mErr2 := matchedBundleTargets(applyDir, rel.Bundle, "game-b")
	if mErr2 != nil {
		t.Fatal(mErr2)
	}
	if len(matched2) != 1 || matched2[0].GameID != "game-b" {
		t.Fatalf("-target 过滤失败: %+v", matched2)
	}

	// 指定不存在的 target 应报错
	if _, err := matchedBundleTargets(applyDir, rel.Bundle, "nope"); err == nil {
		t.Fatal("未知 -target 应报错")
	}
}

// TestDetectSourceForStepsMatchesBundleTarget 覆盖 D-4 的检测抽取。
func TestDetectSourceForStepsMatchesBundleTarget(t *testing.T) {
	oldDir, newDir := tempWorkDir(t), tempWorkDir(t)
	writeTree(t, oldDir, map[string]string{"a.txt": "v1"})
	writeTree(t, newDir, map[string]string{"a.txt": "v2"})

	pool := newBlobPool()
	target := buildTargetWithPool(t, "game-a", oldDir, newDir, pool)

	idx, _, err := detectSourceForSteps(oldDir, target.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Fatalf("旧目录应匹配版本 1，实际下标 %d", idx)
	}

	idx2, _, err := detectSourceForSteps(newDir, target.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if idx2 != 1 {
		t.Fatalf("新目录应匹配目标版本，实际下标 %d", idx2)
	}
}

// TestLoadPrevReleaseRejectsBundle 覆盖 AC5 + 风险 R-1（不得 panic）。
func TestLoadPrevReleaseRejectsBundle(t *testing.T) {
	base := writeFakeBaseExe(t)
	out := filepath.Join(tempWorkDir(t), "merged.exe")
	rel := &Release{
		Flags: FlagIneligibleAsPrev,
		Bundle: &BundlePayload{Targets: []BundleTarget{{
			GameID: "g",
			Steps:  []ChainStep{{SourceIndex: 1, Entries: []Entry{{Path: "x", Action: ActionDelete}}}},
		}}},
	}
	if err := writePatchExecutable(base, rel, out, ""); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("读取合并补丁不应 panic: %v", r)
		}
	}()
	_, err := loadPrevRelease([]string{out})
	if err == nil {
		t.Fatal("合并补丁作为 -prev 应被拒绝")
	}
	if !strings.Contains(err.Error(), "-prev") {
		t.Fatalf("错误提示应说明不能作为 -prev: %v", err)
	}
}

// TestLoadPrevReleaseRejectsFlaggedRelease 覆盖约束 C2 的标记位路径。
func TestLoadPrevReleaseRejectsFlaggedRelease(t *testing.T) {
	base := writeFakeBaseExe(t)
	out := filepath.Join(tempWorkDir(t), "flagged.exe")
	rel := singleStepRelease(1, "a.bin", "a", "b")
	rel.Flags = FlagIneligibleAsPrev
	if err := writePatchExecutable(base, rel, out, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrevRelease([]string{out}); err == nil {
		t.Fatal("带不可作 -prev 标记的补丁应被拒绝")
	}
}