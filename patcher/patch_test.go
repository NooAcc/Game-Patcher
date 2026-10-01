package patcher

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// buildSingleStepPayload 扫描两个目录并生成一份完整的单段补丁（含共享块池）。
func buildSingleStepPayload(t *testing.T, oldDir, newDir string) *ChunkPayload {
	t.Helper()
	jobs, err := planStep(oldDir, newDir, "")
	if err != nil {
		t.Fatalf("planStep 失败: %v", err)
	}
	pool := newBlobPool()
	entries, err := buildChunkStepEntries(oldDir, newDir, jobs, pool)
	if err != nil {
		t.Fatalf("buildChunkStepEntries 失败: %v", err)
	}
	if len(entries) != len(jobs) {
		t.Fatalf("条目数 %d 与变更数 %d 不一致", len(entries), len(jobs))
	}
	payload := NewChunkPayload(
		[]string{versionLabel(oldDir), versionLabel(newDir)},
		[]ChainStep{{SourceIndex: 1, Entries: entries}},
		pool.blobs,
	)
	if err := payload.SelfCheck(); err != nil {
		t.Fatalf("补丁自检失败: %v", err)
	}
	return payload
}

func TestTreePatchApplyAndRestore(t *testing.T) {
	root := tempWorkDir(t)
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	gameDir := filepath.Join(root, "game")

	writeTestFile(t, oldDir, "keep.txt", []byte("same"))
	writeTestFile(t, oldDir, "mod.txt", []byte("old content"))
	writeTestFile(t, oldDir, "del.txt", []byte("will be deleted"))
	writeTestFile(t, newDir, "keep.txt", []byte("same"))
	writeTestFile(t, newDir, "mod.txt", []byte("new content, much longer than before"))
	writeTestFile(t, newDir, "add.txt", []byte("brand new file"))

	payload := buildSingleStepPayload(t, oldDir, newDir)
	if n := len(payload.Steps[0].Entries); n != 3 {
		t.Fatalf("应有 3 个变更条目，实际 %d", n)
	}

	copyTree(t, oldDir, gameDir)
	backupDir := filepath.Join(gameDir, backupDirName)
	manifest, err := ApplyRelease(gameDir, backupDir, payload.StagesFrom(0))
	if err != nil {
		t.Fatalf("ApplyRelease 失败: %v", err)
	}
	if errs := verifyStates(gameDir, payload.Target().Files); len(errs) != 0 {
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

func TestCreatePatchExecutableLayout(t *testing.T) {
	dir := tempWorkDir(t)
	base := randomBytes(4096, 11)
	baseExe := writeTestFile(t, dir, "base.exe", base)

	oldDir := filepath.Join(dir, "old")
	newDir := filepath.Join(dir, "new")
	oldData := randomBytes(300<<10, 12)
	newData := append([]byte(nil), oldData...)
	newData = append(newData, randomBytes(20<<10, 13)...)
	writeTestFile(t, oldDir, "target.bin", oldData)
	writeTestFile(t, newDir, "target.bin", newData)
	outPath := filepath.Join(dir, "update.exe")

	if err := CreatePatch(baseExe, oldDir, newDir, outPath, ""); err != nil {
		t.Fatalf("CreatePatch 失败: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, base) {
		t.Fatal("生成的升级工具应以基础 EXE 开头")
	}

	rel, err := readEmbeddedRelease(outPath)
	if err != nil {
		t.Fatalf("readEmbeddedRelease 失败: %v", err)
	}
	if rel.PatchVersion != 1 {
		t.Fatalf("单段补丁版本号应为 1，实际 %d", rel.PatchVersion)
	}
	if len(rel.Payload.Steps) != 1 || len(rel.Payload.Steps[0].Entries) != 1 {
		t.Fatalf("补丁结构不符合预期: %+v", rel.Payload)
	}
	entry := rel.Payload.Steps[0].Entries[0]
	if entry.Path != "target.bin" || entry.Action != ActionUpdate {
		t.Fatalf("补丁条目不符合预期: %+v", entry)
	}

	gameDir := filepath.Join(dir, "game")
	copyTree(t, oldDir, gameDir)
	if _, err := ApplyRelease(gameDir, filepath.Join(gameDir, backupDirName), rel.Payload.StagesFrom(0)); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	rebuilt, err := os.ReadFile(filepath.Join(gameDir, "target.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rebuilt, newData) {
		t.Fatal("重建内容与目标不一致")
	}

	if !HasEmbeddedPatch(outPath) {
		t.Fatal("HasEmbeddedPatch 应返回 true")
	}
}

func TestCreatePatchWithRestorerTrailer(t *testing.T) {
	dir := tempWorkDir(t)
	baseExe := writeTestFile(t, dir, "base.exe", randomBytes(2048, 21))
	oldDir := filepath.Join(dir, "old")
	newDir := filepath.Join(dir, "new")
	writeTestFile(t, oldDir, "app.bin", []byte("old-old-old"))
	writeTestFile(t, newDir, "app.bin", []byte("new-new-new"))
	outPath := filepath.Join(dir, "update.exe")
	restorerData := []byte("FAKE-RESTORER-BINARY")
	restorerPath := writeTestFile(t, dir, "restorer.exe", restorerData)

	if err := CreatePatch(baseExe, oldDir, newDir, outPath, restorerPath); err != nil {
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

func TestVerifyStageSourceRejectsWrongSource(t *testing.T) {
	dir := tempWorkDir(t)
	gameDir := filepath.Join(dir, "game")
	writeTestFile(t, gameDir, "app.bin", []byte("unexpected content"))
	entries := []Entry{{
		Path: "app.bin", Action: ActionUpdate,
		OldHash: HashBytes([]byte("expected content")),
		NewHash: HashBytes([]byte("new")),
		OldSize: 16, NewSize: 3,
		Ops: []DeltaOp{{Kind: OpLiteral, Length: 3, Comp: CompRaw, Data: []byte("new")}},
	}}
	if err := verifyStageSource(gameDir, entries); err == nil {
		t.Fatal("源版本不匹配时应返回错误")
	}
}

func TestVerifyStageSourceWrongSourceMessage(t *testing.T) {
	dir := tempWorkDir(t)
	gameDir := filepath.Join(dir, "game")
	writeTestFile(t, gameDir, "app.bin", []byte("unexpected content"))
	entries := []Entry{{
		Path: "app.bin", Action: ActionUpdate,
		OldHash: HashBytes([]byte("expected content")),
		NewHash: HashBytes([]byte("new")),
		OldSize: 16, NewSize: 3,
	}}
	err := verifyStageSource(gameDir, entries)
	if err == nil {
		t.Fatal("源版本不匹配时应返回错误")
	}
	if !strings.Contains(err.Error(), "文件被修改") || !strings.Contains(err.Error(), "游戏版本不一致") {
		t.Fatalf("版本不一致提示不符合预期: %v", err)
	}
}

func TestVerifyStageSourceMissingFileMessage(t *testing.T) {
	dir := tempWorkDir(t)
	gameDir := filepath.Join(dir, "game")
	if err := os.MkdirAll(gameDir, 0755); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{{
		Path: "app.bin", Action: ActionDelete,
		OldHash: HashBytes([]byte("old")),
		OldSize: 3,
	}}
	err := verifyStageSource(gameDir, entries)
	if err == nil {
		t.Fatal("文件缺失时应返回错误")
	}
	if !strings.Contains(err.Error(), "文件不存在") || !strings.Contains(err.Error(), "游戏版本不一致") {
		t.Fatalf("文件缺失提示不符合预期: %v", err)
	}
}

func TestVerifyStageSourceRejectsExistingAddTarget(t *testing.T) {
	dir := tempWorkDir(t)
	gameDir := filepath.Join(dir, "game")
	writeTestFile(t, gameDir, "app.bin", []byte("already here"))
	entries := []Entry{{Path: "app.bin", Action: ActionAdd}}
	err := verifyStageSource(gameDir, entries)
	if err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("已存在的目标应被拒绝: %v", err)
	}
}
