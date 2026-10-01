package patcher

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tempWorkDir 创建带容错清理的临时工作目录。
//
// Windows 上 t.TempDir() 的清理会与"刚被写入/重命名的文件"的句柄释放或
// 杀毒扫描产生竞态，偶发报 "The directory is not empty"（残留的只是空目录）。
// 这里显式重试清理并忽略最终错误，避免把环境竞态误报为测试失败。
func tempWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gp-test-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 5; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}

// makeVersionDirs 构造三个版本的目录树：
//
//	mod.txt  : v1 -> v2 -> v3 连续修改（跨越两段）
//	keep.bin : 三版相同
//	del.txt  : 仅 v1 存在
//	add.txt  : 仅 v3 存在
func makeVersionDirs(t *testing.T) (v1, v2, v3 string) {
	t.Helper()
	root := tempWorkDir(t)
	v1 = filepath.Join(root, "v1")
	v2 = filepath.Join(root, "v2")
	v3 = filepath.Join(root, "v3")

	writeTestFile(t, v1, "keep.bin", []byte("keep"))
	writeTestFile(t, v1, "mod.txt", []byte("version-one"))
	writeTestFile(t, v1, "del.txt", []byte("deleted in v2"))

	writeTestFile(t, v2, "keep.bin", []byte("keep"))
	writeTestFile(t, v2, "mod.txt", []byte("version-two-longer"))
	writeTestFile(t, v2, "mid.txt", []byte("added in v2"))

	writeTestFile(t, v3, "keep.bin", []byte("keep"))
	writeTestFile(t, v3, "mod.txt", []byte("version-three-even-longer"))
	writeTestFile(t, v3, "add.txt", []byte("added in v3"))
	return v1, v2, v3
}

// buildTestPayload 用补丁后端构建一条 dirs[0]→dirs[1]→… 的版本链。
func buildTestPayload(t *testing.T, dirs []string, pool *blobPool) *ChunkPayload {
	t.Helper()
	if pool == nil {
		pool = newBlobPool()
	}
	steps := make([]ChainStep, 0, len(dirs)-1)
	for i := 0; i+1 < len(dirs); i++ {
		jobs, err := planStep(dirs[i], dirs[i+1], "")
		if err != nil {
			t.Fatalf("planStep 失败: %v", err)
		}
		entries, err := buildChunkStepEntries(dirs[i], dirs[i+1], jobs, pool)
		if err != nil {
			t.Fatalf("buildChunkStepEntries 失败: %v", err)
		}
		if len(entries) == 0 {
			t.Fatalf("版本 %d -> %d 没有任何变更", i+1, i+2)
		}
		steps = append(steps, ChainStep{SourceIndex: uint32(i + 1), Entries: entries})
	}
	payload := NewChunkPayload(steps, pool.blobs)
	if err := payload.SelfCheck(); err != nil {
		t.Fatalf("补丁自检失败: %v", err)
	}
	return payload
}

func findFileState(t *testing.T, v VersionRef, path string) *FileState {
	t.Helper()
	for i := range v.Files {
		if v.Files[i].Path == path {
			return &v.Files[i]
		}
	}
	return nil
}

func TestChainDerivesVersionStates(t *testing.T) {
	v1, v2, v3 := makeVersionDirs(t)
	payload := buildTestPayload(t, []string{v1, v2, v3}, nil)

	sources := payload.Sources()
	if len(sources) != 3 {
		t.Fatalf("应推导出 3 个版本，实际 %d", len(sources))
	}
	for i := range sources {
		if sources[i].Index != uint32(i+1) {
			t.Fatalf("版本 %d 的序号 = %d，期望 %d", i+1, sources[i].Index, i+1)
		}
	}
	wantHashes := []string{"version-one", "version-two-longer", "version-three-even-longer"}
	for i := range sources {
		fs := findFileState(t, sources[i], "mod.txt")
		if fs == nil || !fs.Exists {
			t.Fatalf("版本 %d 的 mod.txt 应存在", i+1)
		}
		if fs.Hash != HashBytes([]byte(wantHashes[i])) {
			t.Fatalf("版本 %d 的 mod.txt 哈希不符合预期", i+1)
		}
	}
	if fs := findFileState(t, sources[0], "del.txt"); fs == nil || !fs.Exists {
		t.Fatal("v1 应存在 del.txt")
	}
	if fs := findFileState(t, sources[2], "del.txt"); fs == nil || fs.Exists {
		t.Fatal("v3 不应存在 del.txt")
	}
	if fs := findFileState(t, sources[0], "add.txt"); fs == nil || fs.Exists {
		t.Fatal("v1 不应存在 add.txt")
	}
	if fs := findFileState(t, sources[2], "add.txt"); fs == nil || !fs.Exists {
		t.Fatal("v3 应存在 add.txt")
	}
}

func TestDetectSourcePicksChainVersion(t *testing.T) {
	v1, v2, v3 := makeVersionDirs(t)
	payload := buildTestPayload(t, []string{v1, v2, v3}, nil)
	rel := &Release{PatchVersion: 2, Payload: payload}

	cases := []struct {
		name string
		dir  string
		want int
	}{
		{"版本A", v1, 0},
		{"版本B", v2, 1},
		{"版本C(已最新)", v3, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gameDir := filepath.Join(tempWorkDir(t), "game")
			copyTree(t, c.dir, gameDir)
			idx, mismatches, err := DetectSource(gameDir, rel)
			if err != nil {
				t.Fatalf("DetectSource 失败: %v", err)
			}
			if idx != c.want {
				t.Fatalf("检测到版本下标 %d，期望 %d（失败原因: %+v）", idx, c.want, mismatches)
			}
		})
	}
}

func TestDetectSourceRejectsUnknownVersion(t *testing.T) {
	v1, v2, v3 := makeVersionDirs(t)
	payload := buildTestPayload(t, []string{v1, v2, v3}, nil)
	rel := &Release{PatchVersion: 2, Payload: payload}

	gameDir := filepath.Join(tempWorkDir(t), "game")
	copyTree(t, v1, gameDir)
	writeTestFile(t, gameDir, "mod.txt", []byte("tampered content"))

	idx, mismatches, err := DetectSource(gameDir, rel)
	if err != nil {
		t.Fatalf("DetectSource 失败: %v", err)
	}
	if idx != -1 {
		t.Fatalf("被篡改的目录不应匹配任何版本，实际下标 %d", idx)
	}
	if len(mismatches) != len(payload.Sources()) {
		t.Fatalf("应给出每个候选版本的失败原因，实际 %d 条", len(mismatches))
	}
}

// 从版本 A 应用整条链、从版本 B 应用剩余段，都应到达 C；且都能回滚到各自起点。
func TestChainApplyFromEachKnownVersion(t *testing.T) {
	v1, v2, v3 := makeVersionDirs(t)
	payload := buildTestPayload(t, []string{v1, v2, v3}, nil)
	rel := &Release{PatchVersion: 2, Payload: payload}

	cases := []struct {
		name    string
		start   string
		startAt int
	}{
		{"A直达C(两段)", v1, 0},
		{"B直达C(一段)", v2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gameDir := filepath.Join(tempWorkDir(t), "game")
			copyTree(t, c.start, gameDir)
			backupDir := filepath.Join(gameDir, backupDirName)

			idx, _, err := DetectSource(gameDir, rel)
			if err != nil {
				t.Fatal(err)
			}
			if idx != c.startAt {
				t.Fatalf("检测到下标 %d，期望 %d", idx, c.startAt)
			}

			manifest, err := ApplyRelease(gameDir, backupDir, payload.StagesFrom(idx))
			if err != nil {
				t.Fatalf("ApplyRelease 失败: %v", err)
			}
			if errs := verifyStates(gameDir, payload.Target().Files); len(errs) != 0 {
				t.Fatalf("升级到目标版本后校验失败: %v", errs)
			}
			assertFilesMatch(t, gameDir, v3)

			// 回滚必须回到本 case 的起点版本（跨段修改的文件也要还原）。
			if err := Restore(manifest, backupDir); err != nil {
				t.Fatalf("回滚失败: %v", err)
			}
			assertFilesMatch(t, gameDir, c.start)
		})
	}
}

func TestCreatePatchChainWithPrev(t *testing.T) {
	dir := tempWorkDir(t)
	baseExe := writeTestFile(t, dir, "base.exe", randomBytes(1024, 31))
	v1, v2, v3 := makeVersionDirs(t)

	fix1 := filepath.Join(dir, "fix1.exe")
	if err := CreatePatch(baseExe, v1, v2, fix1, ""); err != nil {
		t.Fatalf("生成 fix1 失败: %v", err)
	}
	rel1, err := readEmbeddedRelease(fix1)
	if err != nil {
		t.Fatal(err)
	}
	if rel1.PatchVersion != 1 {
		t.Fatalf("fix1 版本号应为 1，实际 %d", rel1.PatchVersion)
	}

	fix2 := filepath.Join(dir, "fix2.exe")
	if err := CreatePatchChain(baseExe, v2, v3, fix2, "", []string{fix1}); err != nil {
		t.Fatalf("生成 fix2 失败: %v", err)
	}
	rel2, err := readEmbeddedRelease(fix2)
	if err != nil {
		t.Fatal(err)
	}
	if rel2.PatchVersion != 2 {
		t.Fatalf("fix2 版本号应为 2（自动递增），实际 %d", rel2.PatchVersion)
	}
	if len(rel2.Payload.Steps) != 2 {
		t.Fatalf("fix2 应包含两段差异，实际 %d", len(rel2.Payload.Steps))
	}
	sources := rel2.Payload.Sources()
	if len(sources) != 3 || sources[0].Index != 1 || sources[1].Index != 2 || sources[2].Index != 3 {
		t.Fatalf("版本链序号不符合预期: %+v", sources)
	}

	// fix2 必须能把 A 和 B 都升级到 C。
	for _, start := range []string{v1, v2} {
		gameDir := filepath.Join(tempWorkDir(t), "game")
		copyTree(t, start, gameDir)
		idx, _, err := DetectSource(gameDir, rel2)
		if err != nil {
			t.Fatal(err)
		}
		if idx < 0 {
			t.Fatalf("从 %s 出发应能检测到版本", filepath.Base(start))
		}
		if _, err := ApplyRelease(gameDir, filepath.Join(gameDir, backupDirName), rel2.Payload.StagesFrom(idx)); err != nil {
			t.Fatalf("从 %s 升级失败: %v", filepath.Base(start), err)
		}
		assertFilesMatch(t, gameDir, v3)
	}
}

func TestCreatePatchChainRejectsBrokenJunction(t *testing.T) {
	dir := tempWorkDir(t)
	baseExe := writeTestFile(t, dir, "base.exe", randomBytes(512, 32))
	v1, v2, v3 := makeVersionDirs(t)

	fix1 := filepath.Join(dir, "fix1.exe")
	if err := CreatePatch(baseExe, v1, v2, fix1, ""); err != nil {
		t.Fatal(err)
	}
	// fix1 的目标是 v2，却想接到 v3 -> 应被接缝校验拒绝。
	out := filepath.Join(dir, "bad.exe")
	err := CreatePatchChain(baseExe, v3, v3, out, "", []string{fix1})
	if err == nil {
		t.Fatal("接缝不匹配应返回错误")
	}
	if !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("错误提示不符合预期: %v", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("接缝校验失败时不应产出补丁文件")
	}
}

func TestChainEncodeDecodePreservesMultipleSteps(t *testing.T) {
	v1, v2, v3 := makeVersionDirs(t)
	payload := buildTestPayload(t, []string{v1, v2, v3}, nil)
	rel := &Release{PatchVersion: 7, Payload: payload}

	got, err := DecodeRelease(encodeReleaseBytes(t, rel))
	if err != nil {
		t.Fatal(err)
	}
	if got.PatchVersion != 7 {
		t.Fatalf("补丁版本号丢失: %d", got.PatchVersion)
	}
	if len(got.Payload.Steps) != 2 {
		t.Fatalf("段数丢失: %d", len(got.Payload.Steps))
	}
	for i := range payload.Steps {
		if got.Payload.Steps[i].SourceIndex != payload.Steps[i].SourceIndex {
			t.Fatalf("第 %d 段源序号丢失", i+1)
		}
		if len(got.Payload.Steps[i].Entries) != len(payload.Steps[i].Entries) {
			t.Fatalf("第 %d 段条目数丢失", i+1)
		}
	}
	if len(got.Payload.Pool) != len(payload.Pool) {
		t.Fatalf("块池大小丢失: %d != %d", len(got.Payload.Pool), len(payload.Pool))
	}
	if fmt.Sprint(got.Payload.Target().Files) == "" {
		t.Fatal("目标版本指纹不应为空")
	}
}

// 旧线格式（版本链仍携带版本名称）必须被明确拒绝，并提示重新生成。
func TestDecodeRejectsLegacyFormatVersion(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(patchMagic)
	b.WriteByte(byte(legacyFormatVersion))
	b.WriteByte(byte(PayloadChunk))
	b.Write([]byte{0, 0, 0, 0}) // patchVersion

	_, err := DecodeRelease(b.Bytes())
	if err == nil {
		t.Fatal("旧线格式应被拒绝")
	}
	if !strings.Contains(err.Error(), "旧格式") {
		t.Fatalf("错误提示应说明是旧格式: %v", err)
	}
}
