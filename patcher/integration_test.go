package patcher

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func realASARFixtures(t *testing.T) (oldPath, newPath string, ok bool) {
	t.Helper()
	oldPath = filepath.Join("..", "test", "app.asar.orig")
	newPath = filepath.Join("..", "test", "app.asar")
	if _, err := os.Stat(oldPath); err != nil {
		return "", "", false
	}
	if _, err := os.Stat(newPath); err != nil {
		return "", "", false
	}
	return oldPath, newPath, true
}

// TestRealASARDeltaRoundTrip 用真实 306MB ASAR 验证差异算法与补丁体积。
func TestRealASARDeltaRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过 306MB ASAR 集成测试")
	}
	oldPath, newPath, ok := realASARFixtures(t)
	if !ok {
		t.Skip("test/ 中缺少 app.asar / app.asar.orig，跳过")
	}

	start := time.Now()
	pool := newBlobPool()
	res, err := buildChunkFileEntry(oldPath, newPath, "app.asar", newCrossIndex(), pool)
	if err != nil {
		t.Fatalf("buildChunkFileEntry 失败: %v", err)
	}
	buildTime := time.Since(start)

	rel := &Release{PatchVersion: 1, Payload: NewChunkPayload(
		[]ChainStep{{SourceIndex: 1, Entries: []Entry{{
			Path: "app.asar", Action: ActionUpdate,
			OldHash: res.OldHash, NewHash: res.NewHash,
			OldSize: res.OldSize, NewSize: res.NewSize, Ops: res.Ops,
		}}}},
		pool.blobs,
	)}
	var blob bytes.Buffer
	if err := EncodeRelease(&blob, rel); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(tempWorkDir(t), "app.asar")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := opContext{gameDir: filepath.Dir(oldPath), pool: pool.blobs}
	if err := applyOps(ctx, oldPath, res.Ops, out); err != nil {
		out.Close()
		t.Fatalf("applyOps 失败: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := HashFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != res.NewHash {
		t.Fatal("重建文件哈希与新版不一致")
	}

	ratio := 100 * float64(blob.Len()) / float64(res.NewSize)
	t.Logf("ASAR 增量: ops=%d patch=%s (%.3f%% of %s) build=%v",
		len(res.Ops), FormatSize(int64(blob.Len())), ratio, FormatSize(int64(res.NewSize)), buildTime.Round(time.Millisecond))
	if uint64(blob.Len()) > res.NewSize/50 {
		t.Fatalf("补丁体积异常: %d 字节（新文件 %d 字节）", blob.Len(), res.NewSize)
	}
}

// TestExecutableEndToEnd 用真实生成的升级工具验证目录补丁的打包、升级与回滚。
func TestExecutableEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端 EXE 测试")
	}

	root := tempWorkDir(t)
	upgraderExe := filepath.Join(root, "upgrader.exe")
	build := exec.Command("go", "build", "-o", upgraderExe, "game-patcher/cmd/upgrader")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 upgrader 失败: %v\n%s", err, out)
	}

	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	writeTestFile(t, oldDir, "keep.bin", []byte("keep"))
	writeTestFile(t, oldDir, "sub/mod.bin", []byte("old content"))
	writeTestFile(t, oldDir, "del.bin", []byte("delete me"))
	writeTestFile(t, newDir, "keep.bin", []byte("keep"))
	writeTestFile(t, newDir, "sub/mod.bin", []byte("new content with more bytes"))
	writeTestFile(t, newDir, "add.bin", []byte("added"))

	gameDir := filepath.Join(root, "game")
	copyTree(t, oldDir, gameDir)

	updateExe := filepath.Join(gameDir, "update.exe")
	if err := CreatePatch(upgraderExe, oldDir, newDir, updateExe, ""); err != nil {
		t.Fatalf("CreatePatch 失败: %v", err)
	}
	info, err := os.Stat(updateExe)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 20<<20 {
		t.Fatalf("生成的升级工具过大: %d 字节", info.Size())
	}

	run := exec.Command(updateExe)
	run.Dir = gameDir
	run.Stdin = strings.NewReader("y\n")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("运行升级工具失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "升级完成") {
		t.Fatalf("升级工具输出异常:\n%s", out)
	}
	assertFilesMatch(t, gameDir, newDir)
	if _, err := os.Stat(filepath.Join(gameDir, "del.bin")); !os.IsNotExist(err) {
		t.Fatal("del.bin 应已被删除")
	}

	backupDir := filepath.Join(gameDir, backupDirName)
	manifest, err := LoadRestoreManifest(backupDir)
	if err != nil {
		t.Fatalf("加载恢复清单失败: %v", err)
	}
	if err := Restore(manifest, backupDir); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	assertFilesMatch(t, gameDir, oldDir)
	if _, err := os.Stat(filepath.Join(gameDir, "add.bin")); !os.IsNotExist(err) {
		t.Fatal("add.bin 应已被回滚删除")
	}
}

func assertFilesMatch(t *testing.T, root, wantDir string) {
	t.Helper()
	err := filepath.WalkDir(wantDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(wantDir, p)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("%s 内容不一致", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("文件对比失败: %v", err)
	}
}

// runUpdater 把升级工具复制到游戏目录并运行，返回其输出。
func runUpdater(t *testing.T, exe, gameDir string, confirm bool) string {
	t.Helper()
	target := filepath.Join(gameDir, filepath.Base(exe))
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0755); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(target)
	run.Dir = gameDir
	if confirm {
		run.Stdin = strings.NewReader("y\n")
	} else {
		run.Stdin = strings.NewReader("\n")
	}
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("运行升级工具失败: %v\n%s", err, out)
	}
	return string(out)
}

// TestExecutableEndToEndChain 验证链式补丁：fix1(v1→v2) 与 fix2(v2→v3) 合成后，
// v1 与 v2 都能一步升级到 v3，v3 会被识别为已是最新，且回滚可回到链起点。
func TestExecutableEndToEndChain(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端 EXE 测试")
	}

	root := tempWorkDir(t)
	upgraderExe := filepath.Join(root, "upgrader.exe")
	build := exec.Command("go", "build", "-o", upgraderExe, "game-patcher/cmd/upgrader")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 upgrader 失败: %v\n%s", err, out)
	}

	// 版本目录名刻意不使用 v1/v2/v3 形式，用于验证输出只显示链内序号。
	v1 := filepath.Join(root, "build-2026-01-alpha")
	v2 := filepath.Join(root, "build-2026-02-beta")
	v3 := filepath.Join(root, "build-2026-03-final")
	writeTestFile(t, v1, "data.bin", []byte("alpha"))
	writeTestFile(t, v1, "gone.bin", []byte("only in v1"))
	writeTestFile(t, v2, "data.bin", []byte("alpha beta"))
	writeTestFile(t, v2, "mid.bin", []byte("added in v2"))
	writeTestFile(t, v3, "data.bin", []byte("alpha beta gamma"))
	writeTestFile(t, v3, "mid.bin", []byte("added in v2"))
	writeTestFile(t, v3, "last.bin", []byte("only in v3"))

	fix1 := filepath.Join(root, "fix1.exe")
	if err := CreatePatch(upgraderExe, v1, v2, fix1, ""); err != nil {
		t.Fatalf("生成 fix1 失败: %v", err)
	}
	fix2 := filepath.Join(root, "fix2.exe")
	if err := CreatePatchChain(upgraderExe, v2, v3, fix2, "", []string{fix1}); err != nil {
		t.Fatalf("生成 fix2 失败: %v", err)
	}

	// 场景 1：v1 直接升级到 v3，然后回滚回 v1。
	gameA := filepath.Join(root, "gameA")
	copyTree(t, v1, gameA)
	out := runUpdater(t, fix2, gameA, true)
	if !strings.Contains(out, "升级完成") {
		t.Fatalf("v1 升级输出异常:\n%s", out)
	}
	// 版本显示必须使用链内序号，且不得带出构建时的目录名。
	if !strings.Contains(out, "检测到当前版本: v1") || !strings.Contains(out, "目标版本: v3") {
		t.Fatalf("版本显示应使用链内序号 v1/v3：\n%s", out)
	}
	if strings.Contains(out, "build-2026-01-alpha") || strings.Contains(out, "build-2026-03-final") {
		t.Fatalf("版本显示不应带出目录名：\n%s", out)
	}
	assertFilesMatch(t, gameA, v3)

	backupDir := filepath.Join(gameA, backupDirName)
	manifest, err := LoadRestoreManifest(backupDir)
	if err != nil {
		t.Fatalf("加载恢复清单失败: %v", err)
	}
	if err := Restore(manifest, backupDir); err != nil {
		t.Fatalf("回滚失败: %v", err)
	}
	assertFilesMatch(t, gameA, v1)

	// 场景 2：v2 直接升级到 v3。
	gameB := filepath.Join(root, "gameB")
	copyTree(t, v2, gameB)
	if out := runUpdater(t, fix2, gameB, true); !strings.Contains(out, "升级完成") {
		t.Fatalf("v2 升级输出异常:\n%s", out)
	}
	assertFilesMatch(t, gameB, v3)

	// 场景 3：v3 已是最新，应跳过且不产生备份目录。
	gameC := filepath.Join(root, "gameC")
	copyTree(t, v3, gameC)
	outC := runUpdater(t, fix2, gameC, false)
	if !strings.Contains(outC, "已经是最新版本（v3）") {
		t.Fatalf("已是最新版本输出异常:\n%s", outC)
	}
	if strings.Contains(outC, "build-2026-03-final") {
		t.Fatalf("已是最新版本输出不应带出目录名：\n%s", outC)
	}
	if _, err := os.Stat(filepath.Join(gameC, backupDirName)); !os.IsNotExist(err) {
		t.Fatal("已是最新版本时不应创建备份目录")
	}
}
