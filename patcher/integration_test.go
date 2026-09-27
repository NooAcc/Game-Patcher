package patcher

import (
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

func TestRealASARDeltaRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过 306MB ASAR 集成测试")
	}
	oldPath, newPath, ok := realASARFixtures(t)
	if !ok {
		t.Skip("test/ 中缺少 app.asar / app.asar.orig，跳过")
	}

	start := time.Now()
	res, err := buildDelta(oldPath, newPath)
	if err != nil {
		t.Fatalf("buildDelta 失败: %v", err)
	}
	buildTime := time.Since(start)

	blob, err := (&Patch{Mode: ModeFile, Entries: []Entry{{
		Path: "app.asar", Action: ActionUpdate,
		OldHash: res.OldHash, NewHash: res.NewHash,
		OldSize: res.OldSize, NewSize: res.NewSize, Ops: res.Ops,
	}}}).Encode()
	if err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "app.asar")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyDelta(oldPath, res.Ops, out); err != nil {
		out.Close()
		t.Fatalf("applyDelta 失败: %v", err)
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

	ratio := 100 * float64(len(blob)) / float64(res.NewSize)
	t.Logf("ASAR 增量: ops=%d patch=%s (%.3f%% of %s) build=%v",
		len(res.Ops), FormatSize(int64(len(blob))), ratio, FormatSize(int64(res.NewSize)), buildTime.Round(time.Millisecond))
	if uint64(len(blob)) > res.NewSize/50 {
		t.Fatalf("补丁体积异常: %d 字节（新文件 %d 字节）", len(blob), res.NewSize)
	}
}

func TestRealASARExecutableEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端 EXE 集成测试")
	}
	oldPath, newPath, ok := realASARFixtures(t)
	if !ok {
		t.Skip("test/ 中缺少 app.asar / app.asar.orig，跳过")
	}

	root := t.TempDir()
	upgraderExe := filepath.Join(root, "upgrader.exe")
	build := exec.Command("go", "build", "-o", upgraderExe, "game-patcher/cmd/upgrader")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建 upgrader 失败: %v\n%s", err, out)
	}

	gameDir := filepath.Join(root, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(gameDir, "app.asar")
	if err := copyFileAtomic(oldPath, target); err != nil {
		t.Fatal(err)
	}

	updateExe := filepath.Join(gameDir, "update.exe")
	if err := CreatePatch(upgraderExe, oldPath, newPath, updateExe, "app.asar", ""); err != nil {
		t.Fatalf("CreatePatch 失败: %v", err)
	}
	updateInfo, err := os.Stat(updateExe)
	if err != nil {
		t.Fatal(err)
	}
	if updateInfo.Size() > 20<<20 {
		t.Fatalf("生成的升级工具过大: %d 字节", updateInfo.Size())
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

	wantNew, err := HashFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantNew {
		t.Fatal("升级后文件哈希与新版不一致")
	}

	// 用备份目录中的恢复清单回滚，验证恢复路径。
	backupDir := filepath.Join(gameDir, backupDirName)
	manifest, err := LoadRestoreManifest(backupDir)
	if err != nil {
		t.Fatalf("加载恢复清单失败: %v", err)
	}
	if err := Restore(manifest, backupDir); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	wantOld, err := HashFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err = HashFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantOld {
		t.Fatal("恢复后文件哈希与旧版不一致")
	}
}
