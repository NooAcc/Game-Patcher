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
	res, err := buildDelta(oldPath, newPath)
	if err != nil {
		t.Fatalf("buildDelta 失败: %v", err)
	}
	buildTime := time.Since(start)

	blob, err := (&Patch{Entries: []Entry{{
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

// TestExecutableEndToEnd 用真实生成的升级工具验证目录补丁的打包、升级与回滚。
func TestExecutableEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端 EXE 测试")
	}

	root := t.TempDir()
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
