package patcher

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
	if err != nil {
		t.Fatalf("复制目录失败: %v", err)
	}
}

func TestTreePatchApplyAndRestore(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	gameDir := filepath.Join(root, "game")

	writeTestFile(t, oldDir, "keep.txt", []byte("same"))
	writeTestFile(t, oldDir, "mod.txt", []byte("old content"))
	writeTestFile(t, oldDir, "del.txt", []byte("will be deleted"))
	writeTestFile(t, newDir, "keep.txt", []byte("same"))
	writeTestFile(t, newDir, "mod.txt", []byte("new content, much longer than before"))
	writeTestFile(t, newDir, "add.txt", []byte("brand new file"))

	patch, err := buildTreePatch(oldDir, newDir, "")
	if err != nil {
		t.Fatalf("buildTreePatch 失败: %v", err)
	}
	if len(patch.Entries) != 3 {
		t.Fatalf("应有 3 个变更条目，实际 %d", len(patch.Entries))
	}

	copyTree(t, oldDir, gameDir)
	backupDir := filepath.Join(gameDir, backupDirName)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest, err := backupAndVerify(gameDir, backupDir, patch)
	if err != nil {
		t.Fatalf("backupAndVerify 失败: %v", err)
	}
	if err := saveRestoreManifest(backupDir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := applyPatch(gameDir, patch); err != nil {
		t.Fatalf("applyPatch 失败: %v", err)
	}
	if errs := verifyPatch(gameDir, patch); len(errs) != 0 {
		t.Fatalf("升级后校验失败: %v", errs)
	}
	assertFileContent(t, filepath.Join(gameDir, "mod.txt"), "new content, much longer than before")
	assertFileContent(t, filepath.Join(gameDir, "add.txt"), "brand new file")
	if _, err := os.Stat(filepath.Join(gameDir, "del.txt")); !os.IsNotExist(err) {
		t.Fatal("del.txt 应已被删除")
	}

	if err := Restore(manifest, backupDir); err != nil {
		t.Fatalf("Restore 失败: %v", err)
	}
	assertFileContent(t, filepath.Join(gameDir, "mod.txt"), "old content")
	assertFileContent(t, filepath.Join(gameDir, "del.txt"), "will be deleted")
	if _, err := os.Stat(filepath.Join(gameDir, "add.txt")); !os.IsNotExist(err) {
		t.Fatal("add.txt 应已被回滚删除")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s 内容 = %q, want %q", path, data, want)
	}
}

func TestCreatePatchSingleFileExecutableLayout(t *testing.T) {
	dir := t.TempDir()
	base := randomBytes(4096, 11)
	baseExe := writeTestFile(t, dir, "base.exe", base)

	oldData := randomBytes(300<<10, 12)
	newData := append([]byte(nil), oldData...)
	newData = append(newData, randomBytes(20<<10, 13)...)
	oldPath := writeTestFile(t, dir, "old.bin", oldData)
	newPath := writeTestFile(t, dir, "new.bin", newData)
	outPath := filepath.Join(dir, "update.exe")

	if err := CreatePatch(baseExe, oldPath, newPath, outPath, "target.bin", ""); err != nil {
		t.Fatalf("CreatePatch 失败: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, base) {
		t.Fatal("生成的升级工具应以基础 EXE 开头")
	}

	f, err := os.Open(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	off, length, err := findPatchTail(f, fi.Size())
	if err != nil {
		t.Fatalf("findPatchTail 失败: %v", err)
	}
	blob := make([]byte, length)
	if _, err := f.ReadAt(blob, off); err != nil {
		t.Fatal(err)
	}
	patch, err := DecodePatch(blob)
	if err != nil {
		t.Fatalf("DecodePatch 失败: %v", err)
	}
	if patch.Mode != ModeFile || len(patch.Entries) != 1 || patch.Entries[0].Path != "target.bin" {
		t.Fatalf("补丁内容不符合预期: %+v", patch)
	}

	var rebuilt bytes.Buffer
	if err := applyDelta(oldPath, patch.Entries[0].Ops, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rebuilt.Bytes(), newData) {
		t.Fatal("使用补丁布局中的操作重建失败")
	}

	if !HasEmbeddedPatch(outPath) {
		t.Fatal("HasEmbeddedPatch 应返回 true")
	}
}

func TestCreatePatchWithRestorerTrailer(t *testing.T) {
	dir := t.TempDir()
	baseExe := writeTestFile(t, dir, "base.exe", randomBytes(2048, 21))
	oldPath := writeTestFile(t, dir, "old.bin", []byte("old-old-old"))
	newPath := writeTestFile(t, dir, "new.bin", []byte("new-new-new"))
	outPath := filepath.Join(dir, "update.exe")
	restorerData := []byte("FAKE-RESTORER-BINARY")
	restorerPath := writeTestFile(t, dir, "restorer.exe", restorerData)

	if err := CreatePatch(baseExe, oldPath, newPath, outPath, "app.bin", restorerPath); err != nil {
		t.Fatal(err)
	}
	extracted, err := extractRestorer(outPath)
	if err != nil {
		t.Fatalf("extractRestorer 失败: %v", err)
	}
	if !bytes.Equal(extracted, restorerData) {
		t.Fatalf("恢复工具内容不匹配: %q", extracted)
	}
	if !HasEmbeddedPatch(outPath) {
		t.Fatal("带恢复工具的补丁应可被识别")
	}
}

func TestBackupAndVerifyRejectsWrongSource(t *testing.T) {
	dir := t.TempDir()
	gameDir := filepath.Join(dir, "game")
	backupDir := filepath.Join(gameDir, backupDirName)
	writeTestFile(t, gameDir, "app.bin", []byte("unexpected content"))
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	patch := &Patch{Mode: ModeFile, Entries: []Entry{{
		Path: "app.bin", Action: ActionUpdate,
		OldHash: HashBytes([]byte("expected content")),
		NewHash: HashBytes([]byte("new")),
		OldSize: 16, NewSize: 3,
		Ops: []DeltaOp{{Kind: OpLiteral, Length: 3, Comp: CompRaw, Data: []byte("new")}},
	}}}
	if _, err := backupAndVerify(gameDir, backupDir, patch); err == nil {
		t.Fatal("源版本不匹配时应返回错误")
	}
}
