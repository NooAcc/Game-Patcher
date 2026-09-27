package patcher

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const backupDirName = "_backup_before_patch"

type fileMeta struct {
	size uint64
	hash [32]byte
}

type treeJob struct {
	path    string
	action  Action
	oldMeta fileMeta
	newMeta fileMeta
}

// CreatePatch 对比 oldPath 与 newPath，生成单文件升级工具。
// oldPath/newPath 同为文件时生成单文件补丁，同为目录时生成目录树补丁。
// target 仅在单文件模式下使用；为空时取 newPath 的文件名。
func CreatePatch(baseExe, oldPath, newPath, outputPath, target, restorerPath string) error {
	start := time.Now()

	oldInfo, err := os.Stat(oldPath)
	if err != nil {
		return fmt.Errorf("旧版本路径无效: %w", err)
	}
	newInfo, err := os.Stat(newPath)
	if err != nil {
		return fmt.Errorf("新版本路径无效: %w", err)
	}
	if oldInfo.IsDir() != newInfo.IsDir() {
		return fmt.Errorf("-old 与 -new 必须同为文件或同为目录")
	}

	var patch *Patch
	if newInfo.IsDir() {
		patch, err = buildTreePatch(oldPath, newPath, outputPath)
	} else {
		patch, err = buildFilePatch(oldPath, newPath, target)
	}
	if err != nil {
		return err
	}
	if len(patch.Entries) == 0 {
		fmt.Println("✅ 两个版本完全相同，无需生成升级包。")
		return nil
	}

	blob, err := patch.Encode()
	if err != nil {
		return err
	}
	if err := writePatchExecutable(baseExe, blob, outputPath, restorerPath); err != nil {
		return err
	}

	fi, err := os.Stat(outputPath)
	if err != nil {
		return err
	}
	fmt.Printf("✅ 升级工具已生成: %s (%s) [耗时 %v]\n", outputPath, FormatSize(fi.Size()), time.Since(start).Round(time.Millisecond))
	fmt.Println()
	fmt.Println("💡 将此文件放入游戏目标目录，直接运行即可升级。")
	return nil
}

func buildFilePatch(oldFile, newFile, target string) (*Patch, error) {
	if target == "" {
		target = filepath.Base(newFile)
	}
	target = filepath.ToSlash(target)
	if err := validateRelPath(target); err != nil {
		return nil, fmt.Errorf("目标路径非法: %w", err)
	}

	fmt.Println("🔍 计算二进制差异...")
	res, err := buildDelta(oldFile, newFile)
	if err != nil {
		return nil, err
	}
	entry := Entry{
		Path:    target,
		Action:  ActionUpdate,
		OldHash: res.OldHash,
		NewHash: res.NewHash,
		OldSize: res.OldSize,
		NewSize: res.NewSize,
		Ops:     res.Ops,
	}
	return &Patch{Mode: ModeFile, Entries: []Entry{entry}}, nil
}

func buildTreePatch(oldDir, newDir, skipPath string) (*Patch, error) {
	fmt.Println("🔍 扫描旧版本...")
	oldFiles, err := scanTree(oldDir, skipPath)
	if err != nil {
		return nil, fmt.Errorf("扫描旧版本失败: %w", err)
	}
	fmt.Println("🔍 扫描新版本...")
	newFiles, err := scanTree(newDir, skipPath)
	if err != nil {
		return nil, fmt.Errorf("扫描新版本失败: %w", err)
	}

	var jobs []treeJob
	for path, nm := range newFiles {
		if om, ok := oldFiles[path]; ok {
			if om.hash == nm.hash {
				continue
			}
			jobs = append(jobs, treeJob{path: path, action: ActionUpdate, oldMeta: om, newMeta: nm})
			continue
		}
		jobs = append(jobs, treeJob{path: path, action: ActionAdd, newMeta: nm})
	}
	for path, om := range oldFiles {
		if _, ok := newFiles[path]; !ok {
			jobs = append(jobs, treeJob{path: path, action: ActionDelete, oldMeta: om})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].path < jobs[j].path })

	var adds, updates, dels int
	for i := range jobs {
		switch jobs[i].action {
		case ActionAdd:
			adds++
		case ActionUpdate:
			updates++
		case ActionDelete:
			dels++
		}
	}
	fmt.Printf("\n📊 差异: +%d 新增, ~%d 修改, -%d 删除\n\n", adds, updates, dels)
	if len(jobs) == 0 {
		return &Patch{Mode: ModeTree}, nil
	}

	entries := make([]Entry, len(jobs))
	err = parallelFor(len(jobs), runtime.NumCPU(), func(i int) error {
		j := &jobs[i]
		switch j.action {
		case ActionAdd:
			ops, err := buildLiteralOps(filepath.Join(newDir, filepath.FromSlash(j.path)))
			if err != nil {
				return fmt.Errorf("读取新增文件 %s 失败: %w", j.path, err)
			}
			entries[i] = Entry{
				Path:    j.path,
				Action:  ActionAdd,
				NewHash: j.newMeta.hash,
				NewSize: j.newMeta.size,
				Ops:     ops,
			}
		case ActionUpdate:
			res, err := buildDelta(
				filepath.Join(oldDir, filepath.FromSlash(j.path)),
				filepath.Join(newDir, filepath.FromSlash(j.path)),
			)
			if err != nil {
				return fmt.Errorf("计算 %s 差异失败: %w", j.path, err)
			}
			entries[i] = Entry{
				Path:    j.path,
				Action:  ActionUpdate,
				OldHash: res.OldHash,
				NewHash: res.NewHash,
				OldSize: res.OldSize,
				NewSize: res.NewSize,
				Ops:     res.Ops,
			}
		case ActionDelete:
			entries[i] = Entry{
				Path:    j.path,
				Action:  ActionDelete,
				OldHash: j.oldMeta.hash,
				OldSize: j.oldMeta.size,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Patch{Mode: ModeTree, Entries: entries}, nil
}

func scanTree(root, skipPath string) (map[string]fileMeta, error) {
	var rels, abs []string
	skipAbs, _ := filepath.Abs(skipPath)

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == backupDirName {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("不支持非普通文件（符号链接/设备等）: %s", p)
		}
		if skipAbs != "" {
			if a, err := filepath.Abs(p); err == nil && strings.EqualFold(a, skipAbs) {
				return nil
			}
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		abs = append(abs, p)
		return nil
	})
	if err != nil {
		return nil, err
	}

	metas := make([]fileMeta, len(abs))
	err = parallelFor(len(abs), runtime.NumCPU(), func(i int) error {
		h, err := HashFile(abs[i])
		if err != nil {
			return fmt.Errorf("哈希 %s 失败: %w", rels[i], err)
		}
		info, err := os.Stat(abs[i])
		if err != nil {
			return err
		}
		metas[i] = fileMeta{size: uint64(info.Size()), hash: h}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make(map[string]fileMeta, len(rels))
	for i := range rels {
		out[rels[i]] = metas[i]
	}
	return out, nil
}

func parallelFor(n, workers int, fn func(i int) error) error {
	if n <= 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > n {
		workers = n
	}
	var (
		next     int
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if firstErr != nil || next >= n {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()

				if err := fn(i); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func writePatchExecutable(baseExe string, blob []byte, outputPath, restorerPath string) error {
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gamepatch-build-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}

	base, err := os.Open(baseExe)
	if err != nil {
		return fail(fmt.Errorf("读取升级工具基础程序失败: %w", err))
	}
	if _, err := io.Copy(tmp, base); err != nil {
		base.Close()
		return fail(fmt.Errorf("写入升级工具基础程序失败: %w", err))
	}
	base.Close()

	if _, err := tmp.Write(blob); err != nil {
		return fail(err)
	}
	if err := writeU64(tmp, uint64(len(blob))); err != nil {
		return fail(err)
	}
	if _, err := io.WriteString(tmp, patchEndMagic); err != nil {
		return fail(err)
	}

	if restorerPath != "" {
		rst, err := os.Open(restorerPath)
		if err != nil {
			return fail(fmt.Errorf("读取恢复工具失败: %w", err))
		}
		st, err := rst.Stat()
		if err != nil {
			rst.Close()
			return fail(err)
		}
		if _, err := io.Copy(tmp, rst); err != nil {
			rst.Close()
			return fail(fmt.Errorf("写入恢复工具失败: %w", err))
		}
		rst.Close()
		if err := writeU64(tmp, uint64(st.Size())); err != nil {
			return fail(err)
		}
		if _, err := io.WriteString(tmp, restorerMagic); err != nil {
			return fail(err)
		}
		fmt.Printf("📦 恢复工具已嵌入 (%s)\n", FormatSize(st.Size()))
	}

	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, outputPath); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func writeU64(w io.Writer, v uint64) error {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	_, err := w.Write(b[:])
	return err
}

func readU64At(f *os.File, off int64) (uint64, error) {
	if off < 0 {
		return 0, fmt.Errorf("偏移为负: %d", off)
	}
	var b [8]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

// findPatchTail 定位可执行文件尾部的补丁块。
func findPatchTail(f *os.File, size int64) (int64, uint64, error) {
	const lenLen = int64(8)
	endLen := int64(len(patchEndMagic))
	rstLen := int64(len(restorerMagic))

	if size < lenLen+endLen {
		return 0, 0, fmt.Errorf("文件太小: %d 字节", size)
	}

	last := make([]byte, rstLen)
	if _, err := f.ReadAt(last, size-rstLen); err != nil {
		return 0, 0, err
	}

	patchEndOff := size - endLen
	if string(last) == restorerMagic {
		restorerSize, err := readU64At(f, size-rstLen-lenLen)
		if err != nil {
			return 0, 0, err
		}
		if restorerSize > uint64(size) {
			return 0, 0, fmt.Errorf("恢复工具长度异常: %d", restorerSize)
		}
		patchEndOff = size - rstLen - lenLen - int64(restorerSize) - endLen
	}
	if patchEndOff < lenLen+endLen {
		return 0, 0, fmt.Errorf("补丁尾部位置异常")
	}

	magic := make([]byte, endLen)
	if _, err := f.ReadAt(magic, patchEndOff); err != nil {
		return 0, 0, err
	}
	if string(magic) != patchEndMagic {
		return 0, 0, fmt.Errorf("补丁尾标记不匹配")
	}

	blobLen, err := readU64At(f, patchEndOff-lenLen)
	if err != nil {
		return 0, 0, err
	}
	if blobLen > uint64(patchEndOff-lenLen) {
		return 0, 0, fmt.Errorf("补丁数据长度异常: %d", blobLen)
	}
	return patchEndOff - lenLen - int64(blobLen), blobLen, nil
}

// HasEmbeddedPatch 检查可执行文件是否含有效补丁。
func HasEmbeddedPatch(exePath string) bool {
	f, err := os.Open(exePath)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	_, _, err = findPatchTail(f, fi.Size())
	return err == nil
}

func extractRestorer(exePath string) ([]byte, error) {
	f, err := os.Open(exePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	rstLen := int64(len(restorerMagic))
	if size < rstLen+8 {
		return nil, fmt.Errorf("文件太小，无恢复工具")
	}
	last := make([]byte, rstLen)
	if _, err := f.ReadAt(last, size-rstLen); err != nil {
		return nil, err
	}
	if string(last) != restorerMagic {
		return nil, fmt.Errorf("未嵌入恢复工具")
	}
	restorerSize, err := readU64At(f, size-rstLen-8)
	if err != nil {
		return nil, err
	}
	if restorerSize > uint64(size) || !fitsInt(restorerSize) {
		return nil, fmt.Errorf("恢复工具长度异常")
	}
	start := size - rstLen - 8 - int64(restorerSize)
	if start < 0 {
		return nil, fmt.Errorf("恢复工具数据越界")
	}
	data := make([]byte, restorerSize)
	if _, err := f.ReadAt(data, start); err != nil {
		return nil, err
	}
	return data, nil
}

// RunEmbedded 从 exePath 尾部读取补丁并执行升级。
func RunEmbedded(exePath string) error {
	f, err := os.Open(exePath)
	if err != nil {
		return fmt.Errorf("打开自身失败: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	blobOff, blobLen, err := findPatchTail(f, fi.Size())
	if err != nil {
		f.Close()
		return fmt.Errorf("未找到补丁数据: %w", err)
	}
	if blobLen > uint64(fi.Size()) || !fitsInt(blobLen) {
		f.Close()
		return fmt.Errorf("补丁数据长度异常: %d", blobLen)
	}
	blob := make([]byte, blobLen)
	if _, err := f.ReadAt(blob, blobOff); err != nil {
		f.Close()
		return fmt.Errorf("读取补丁数据失败: %w", err)
	}
	f.Close()

	patch, err := DecodePatch(blob)
	if err != nil {
		return fmt.Errorf("补丁数据损坏: %w", err)
	}

	gameDir := filepath.Dir(exePath)
	printPatchSummary(gameDir, patch)

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
	manifest, err := backupAndVerify(gameDir, backupDir, patch)
	if err != nil {
		return err
	}
	if err := saveRestoreManifest(backupDir, manifest); err != nil {
		return fmt.Errorf("写入恢复清单失败: %w", err)
	}
	if data, err := extractRestorer(exePath); err == nil {
		name := "restorer.exe"
		if runtime.GOOS != "windows" {
			name = "restorer"
		}
		if err := writeAtomic(filepath.Join(backupDir, name), func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		}); err != nil {
			fmt.Printf("⚠️  释放恢复工具失败: %v\n", err)
		} else {
			fmt.Printf("📦 恢复工具已放置: %s/%s\n", backupDirName, name)
		}
	} else {
		fmt.Printf("⚠️  未找到嵌入的恢复工具: %v\n", err)
	}

	fmt.Println("🔧 正在升级...")
	if err := applyPatch(gameDir, patch); err != nil {
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
	errs := verifyPatch(gameDir, patch)
	dur := time.Since(verifyStart).Round(time.Millisecond)
	if len(errs) == 0 {
		fmt.Printf("✅ 升级完成! 验证通过 (%d 个文件, 耗时 %v)\n", len(patch.Entries), dur)
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

func printPatchSummary(gameDir string, patch *Patch) {
	var adds, updates, dels int
	for i := range patch.Entries {
		switch patch.Entries[i].Action {
		case ActionAdd:
			adds++
		case ActionUpdate:
			updates++
		case ActionDelete:
			dels++
		}
	}
	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║       🎮 游戏升级工具                ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("📂 目标目录: %s\n", gameDir)
	fmt.Printf("📊 变更: +%d 新增, ~%d 修改, -%d 删除 (共 %d 项)\n", adds, updates, dels, len(patch.Entries))
	if patch.Mode == ModeFile && len(patch.Entries) == 1 {
		fmt.Printf("🎯 目标文件: %s\n", patch.Entries[0].Path)
	}
	fmt.Println()
}

func prepareBackupDir(gameDir string) (string, error) {
	backupDir := filepath.Join(gameDir, backupDirName)
	fi, err := os.Stat(backupDir)
	if err == nil {
		if !fi.IsDir() {
			return "", fmt.Errorf("%s 已存在且不是目录", backupDir)
		}
		entries, err := os.ReadDir(backupDir)
		if err != nil {
			return "", err
		}
		if len(entries) > 0 {
			ts := time.Now().Format("20060102-150405")
			base := filepath.Join(gameDir, backupDirName+"_"+ts)
			old := base
			for i := 1; ; i++ {
				if _, err := os.Stat(old); os.IsNotExist(err) {
					break
				}
				old = fmt.Sprintf("%s_%d", base, i)
			}
			if err := os.Rename(backupDir, old); err != nil {
				return "", fmt.Errorf("保留旧备份失败: %w", err)
			}
			fmt.Printf("📦 检测到已有备份，已保留为: %s\n", filepath.Base(old))
		}
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", err
	}
	return backupDir, nil
}

// backupAndVerify 校验目标是否为补丁期望的源版本，并备份旧文件。
func backupAndVerify(gameDir, backupDir string, patch *Patch) (*RestoreManifest, error) {
	manifest := &RestoreManifest{GameDir: gameDir, Entries: make([]RestoreEntry, 0, len(patch.Entries))}
	for i := range patch.Entries {
		e := &patch.Entries[i]
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			return nil, err
		}
		switch e.Action {
		case ActionAdd:
			if _, err := os.Stat(target); err == nil {
				return nil, fmt.Errorf("目标文件已存在，无法安全新增: %s（补丁可能已应用）", e.Path)
			} else if !os.IsNotExist(err) {
				return nil, err
			}
			manifest.Entries = append(manifest.Entries, RestoreEntry{Action: "add", Path: e.Path})
		case ActionUpdate, ActionDelete:
			got, err := HashFile(target)
			if err != nil {
				return nil, fmt.Errorf("读取目标文件失败 %s: %w", e.Path, err)
			}
			if got != e.OldHash {
				return nil, fmt.Errorf("目标文件与补丁源版本不匹配: %s", e.Path)
			}
			backupPath, err := SafeJoin(backupDir, e.Path)
			if err != nil {
				return nil, err
			}
			if err := copyFileAtomic(target, backupPath); err != nil {
				return nil, fmt.Errorf("备份 %s 失败: %w", e.Path, err)
			}
			action := "update"
			if e.Action == ActionDelete {
				action = "delete"
			}
			manifest.Entries = append(manifest.Entries, RestoreEntry{Action: action, Path: e.Path, OldHash: hashHex(got)})
		default:
			return nil, fmt.Errorf("非法条目行为: %s", e.Action)
		}
	}
	return manifest, nil
}

func applyPatch(gameDir string, patch *Patch) error {
	total := len(patch.Entries)
	for i := range patch.Entries {
		e := &patch.Entries[i]
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			return err
		}
		tag := fmt.Sprintf("[%d/%d]", i+1, total)
		switch e.Action {
		case ActionAdd:
			fmt.Printf("   %s + %s\n", tag, e.Path)
			if err := materialize(target, "", e.Ops, e.NewHash, e.NewSize); err != nil {
				return fmt.Errorf("新增 %s 失败: %w", e.Path, err)
			}
		case ActionUpdate:
			fmt.Printf("   %s ~ %s\n", tag, e.Path)
			if err := materialize(target, target, e.Ops, e.NewHash, e.NewSize); err != nil {
				return fmt.Errorf("更新 %s 失败: %w", e.Path, err)
			}
		case ActionDelete:
			fmt.Printf("   %s - %s\n", tag, e.Path)
			if err := os.Remove(target); err != nil {
				return fmt.Errorf("删除 %s 失败: %w", e.Path, err)
			}
			cleanupEmptyDirs(filepath.Dir(target), gameDir)
		default:
			return fmt.Errorf("非法条目行为: %s", e.Action)
		}
	}
	return nil
}

// materialize 先在临时文件中重建目标，校验哈希后再原子替换。
func materialize(dst, oldPath string, ops []DeltaOp, wantHash [32]byte, wantSize uint64) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gamepatch-apply-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := applyDelta(oldPath, ops, tmp); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	fi, err := os.Stat(tmpName)
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if uint64(fi.Size()) != wantSize {
		os.Remove(tmpName)
		return fmt.Errorf("大小校验失败: 期望 %d，实际 %d", wantSize, fi.Size())
	}
	got, err := HashFile(tmpName)
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if got != wantHash {
		os.Remove(tmpName)
		return fmt.Errorf("哈希校验失败")
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func verifyPatch(gameDir string, patch *Patch) []error {
	var errs []error
	for i := range patch.Entries {
		e := &patch.Entries[i]
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if e.Action == ActionDelete {
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("文件未被删除: %s", e.Path))
			}
			continue
		}
		fi, err := os.Stat(target)
		if err != nil {
			errs = append(errs, fmt.Errorf("文件不存在: %s", e.Path))
			continue
		}
		if uint64(fi.Size()) != e.NewSize {
			errs = append(errs, fmt.Errorf("大小不匹配: %s", e.Path))
			continue
		}
		got, err := HashFile(target)
		if err != nil {
			errs = append(errs, fmt.Errorf("读取失败: %s: %w", e.Path, err))
			continue
		}
		if got != e.NewHash {
			errs = append(errs, fmt.Errorf("哈希不匹配: %s", e.Path))
		}
	}
	return errs
}
