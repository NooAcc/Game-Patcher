package patcher

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zeebo/blake3"
	"golang.org/x/exp/mmap"
)

// ── 数据结构 ──────────────────────────────────────────────

type Manifest struct {
	VersionFrom string   `json:"version_from"`
	VersionTo   string   `json:"version_to"`
	Operations  []FileOp `json:"operations"`
}

type FileOp struct {
	Action string `json:"action"` // add / delete / update
	Path   string `json:"path"`
	Hash   string `json:"hash,omitempty"`
}

// 嵌入在可执行文件尾部的标记
const (
	MagicPatch    = "GAMEPATCH1"
	MagicTail     = "PATCHTAIL!"
	MagicRestorer = "RESTOREBIN!"
)

// 由 magic 标记长度派生的 trailer 大小，避免硬编码
var (
	patchTrailerSize    = 8 + len(MagicTail)     // patchLen(8B) + PATCHTAIL!
	restorerTrailerSize = 8 + len(MagicRestorer) // restorerLen(8B) + RESTOREBIN!
)

// mmap 阈值
const mmapThreshold = 1 << 20 // 1 MB

var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 256*1024)
		return &buf
	},
}

// ── 并行文件扫描与哈希 ────────────────────────────────────

type fileEntry struct {
	relPath string
	absPath string
	size    int64
}

type hashResult struct {
	relPath string
	hash    string
	err     error
}

func CollectFilesParallel(root string, verbose bool) (map[string]string, error) {
	t0 := time.Now()

	var files []fileEntry
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		files = append(files, fileEntry{
			relPath: filepath.ToSlash(rel),
			absPath: path,
			size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描目录失败: %w", err)
	}

	if verbose {
		fmt.Printf("   📁 发现 %d 个文件\n", len(files))
	}

	resultCh := make(chan hashResult, len(files))
	var wg sync.WaitGroup
	var totalBytes atomic.Int64

	for _, fe := range files {
		wg.Add(1)
		go func(fe fileEntry) {
			defer wg.Done()
			h := hashFile(fe.absPath, fe.size, &totalBytes)
			resultCh <- hashResult{relPath: fe.relPath, hash: h}
		}(fe)
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	fileMap := make(map[string]string, len(files))
	var hashErrs int
	for r := range resultCh {
		if r.err != nil {
			hashErrs++
			if verbose {
				fmt.Printf("   ⚠️  哈希失败: %s: %v\n", r.relPath, r.err)
			}
			continue
		}
		fileMap[r.relPath] = r.hash
	}

	if hashErrs > 0 {
		return fileMap, fmt.Errorf("%d 个文件哈希失败", hashErrs)
	}

	dur := time.Since(t0)
	if verbose {
		fmt.Printf("   ⏱  扫描+哈希: %v, 总数据: %s\n", dur.Round(time.Millisecond), FormatSize(totalBytes.Load()))
	}
	return fileMap, nil
}

func hashFile(path string, size int64, totalBytes *atomic.Int64) string {
	totalBytes.Add(size)
	if size >= int64(mmapThreshold) {
		return hashFileMmap(path)
	}
	return hashFileBuffered(path)
}

func hashFileMmap(path string) string {
	mm, err := mmap.Open(path)
	if err != nil {
		return hashFileBuffered(path)
	}
	defer mm.Close()

	fi, err := os.Stat(path)
	if err != nil {
		return hashFileBuffered(path)
	}

	h := blake3.New()
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)

	remain := fi.Size()
	var off int64
	for remain > 0 {
		chunk := int64(len(*bufPtr))
		if chunk > remain {
			chunk = remain
		}
		n, err := mm.ReadAt((*bufPtr)[:chunk], off)
		if n > 0 {
			h.Write((*bufPtr)[:n])
			off += int64(n)
			remain -= int64(n)
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashFileBuffered(path string) string {
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := blake3.New()
	for {
		n, err := f.Read(*bufPtr)
		if n > 0 {
			h.Write((*bufPtr)[:n])
		}
		if err != nil {
			break
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func FormatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ── 补丁创建 ──────────────────────────────────────────────

// CreatePatch 对比两个目录，生成单文件升级工具
//   - baseExe: 升级工具的基础二进制（game-patcher-upgrader），生成的 EXE 以此开头
//   - restorerPath 可选：恢复工具二进制路径，传空则不嵌入
func CreatePatch(baseExe, oldDir, newDir, outputPath, restorerPath string) error {
	totalStart := time.Now()

	fmt.Println("🔍 扫描旧版本...")
	oldFiles, err := CollectFilesParallel(oldDir, true)
	if err != nil {
		return err
	}

	fmt.Println("🔍 扫描新版本...")
	newFiles, err := CollectFilesParallel(newDir, true)
	if err != nil {
		return err
	}

	diffStart := time.Now()

	var ops []FileOp
	var dataFiles []string

	for rel, newHash := range newFiles {
		if oldHash, ok := oldFiles[rel]; !ok {
			ops = append(ops, FileOp{Action: "add", Path: rel, Hash: newHash})
			dataFiles = append(dataFiles, rel)
		} else if oldHash != newHash {
			ops = append(ops, FileOp{Action: "update", Path: rel, Hash: newHash})
			dataFiles = append(dataFiles, rel)
		}
	}
	for rel := range oldFiles {
		if _, ok := newFiles[rel]; !ok {
			ops = append(ops, FileOp{Action: "delete", Path: rel})
		}
	}

	diffDur := time.Since(diffStart)

	if len(ops) == 0 {
		fmt.Println("✅ 两个版本完全相同，无需生成升级包。")
		return nil
	}

	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Action != ops[j].Action {
			return ops[i].Action < ops[j].Action
		}
		return ops[i].Path < ops[j].Path
	})
	sort.Strings(dataFiles)

	var adds, dels, updates int
	for _, op := range ops {
		switch op.Action {
		case "add":
			adds++
		case "delete":
			dels++
		case "update":
			updates++
		}
	}

	fmt.Printf("\n📊 差异: +%d 新增, ~%d 修改, -%d 删除, %d 需打包 (diff: %v)\n\n", adds, updates, dels, len(dataFiles), diffDur.Round(time.Millisecond))

	patchData, err := buildPatchData(Manifest{
		VersionFrom: "1.0",
		VersionTo:   "1.1",
		Operations:  ops,
	}, newDir, dataFiles)
	if err != nil {
		return err
	}

	baseBin, err := os.ReadFile(baseExe)
	if err != nil {
		return fmt.Errorf("读取升级工具基础程序失败: %w", err)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	// 写入: [upgrader二进制][补丁数据][补丁长度(8B)][PATCHTAIL!]
	if _, err := out.Write(baseBin); err != nil {
		return err
	}
	if _, err := out.Write(patchData); err != nil {
		return err
	}
	if err := WriteUint64(out, uint64(len(patchData))); err != nil {
		return err
	}
	if _, err := out.WriteString(MagicTail); err != nil {
		return err
	}

	// 嵌入恢复工具: [恢复工具数据][恢复工具长度(8B)][RESTOREBIN!]
	if restorerPath != "" {
		restorerBin, err := os.ReadFile(restorerPath)
		if err != nil {
			fmt.Printf("⚠️  读取恢复工具失败 (%v)，升级工具将不包含恢复功能\n", err)
		} else {
			if _, err := out.Write(restorerBin); err != nil {
				return fmt.Errorf("写入恢复工具失败: %w", err)
			}
			if err := WriteUint64(out, uint64(len(restorerBin))); err != nil {
				return err
			}
			if _, err := out.WriteString(MagicRestorer); err != nil {
				return err
			}
			fmt.Printf("📦 恢复工具已嵌入 (%s)\n", FormatSize(int64(len(restorerBin))))
		}
	}

	if runtime.GOOS != "windows" {
		os.Chmod(outputPath, 0755)
	}

	fi, _ := os.Stat(outputPath)
	totalDur := time.Since(totalStart)
	fmt.Printf("✅ 升级工具已生成: %s (%s) [耗时 %v]\n", outputPath, FormatSize(fi.Size()), totalDur.Round(time.Millisecond))
	fmt.Println()
	fmt.Println("💡 将此文件放入游戏根目录，直接运行即可升级。")

	return nil
}

func buildPatchData(manifest Manifest, newDir string, dataFiles []string) ([]byte, error) {
	var buf []byte
	buf = append(buf, []byte(MagicPatch)...)

	mj, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	buf = AppendUint32(buf, uint32(len(mj)))
	buf = append(buf, mj...)

	buf = AppendUint32(buf, uint32(len(dataFiles)))

	for _, rel := range dataFiles {
		abs := filepath.Join(newDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("读取文件 %s 失败: %w", rel, err)
		}

		pathBytes := []byte(rel)
		buf = AppendUint32(buf, uint32(len(pathBytes)))
		buf = append(buf, pathBytes...)
		buf = AppendUint64(buf, uint64(len(data)))
		buf = append(buf, data...)
	}

	return buf, nil
}

// ── 补丁应用 ──────────────────────────────────────────────

type RestoreManifest struct {
	Operations []FileOp `json:"operations"`
	GameDir    string   `json:"game_dir"`
}

// findPatchTail 在文件中查找 PATCHTAIL! 标记的位置
//
// 二进制格式:
//
//	无 restorer: [base][patchData][patchLen(8B)][PATCHTAIL!]
//	有 restorer: [base][patchData][patchLen(8B)][PATCHTAIL!][restorerBin][restorerLen(8B)][RESTOREBIN!]
func findPatchTail(f *os.File, fi os.FileInfo) (patchTailOffset int64, patchDataLen uint64, err error) {
	fileSize := fi.Size()
	magicTailLen := int64(len(MagicTail))         // 10
	magicRestorerLen := int64(len(MagicRestorer))  // 11

	minSize := int64(8) + magicTailLen // patchLen(8) + PATCHTAIL!
	if fileSize < minSize {
		return 0, 0, fmt.Errorf("文件太小")
	}

	// 先检查末尾是否为 RESTOREBIN!
	tailBuf := make([]byte, magicRestorerLen)
	if _, err := f.ReadAt(tailBuf, fileSize-magicRestorerLen); err != nil {
		return 0, 0, err
	}

	if string(tailBuf) == MagicRestorer {
		// 有 restorer 嵌入，跳过它来找 PATCHTAIL!
		// 格式: ...[restorerBin][restorerLen(8B)][RESTOREBIN!(11B)]
		lenBuf := make([]byte, 8)
		if _, err := f.ReadAt(lenBuf, fileSize-int64(restorerTrailerSize)); err != nil {
			return 0, 0, err
		}
		restorerLen := ReadUint64LE(lenBuf)
		// PATCHTAIL! 结束位置 = fileSize - restorerTrailerSize - restorerLen
		patchTailEnd := fileSize - int64(restorerTrailerSize) - int64(restorerLen)
		if patchTailEnd < int64(restorerTrailerSize) {
			return 0, 0, fmt.Errorf("restorer 数据异常大")
		}
		patchTailOffset = patchTailEnd - magicTailLen
	} else {
		// 没有 restorer，PATCHTAIL! 就在文件末尾
		patchTailOffset = fileSize - magicTailLen
	}

	// 读取 patchLen (在 PATCHTAIL! 前 8 字节)
	lenBuf := make([]byte, 8)
	if _, err := f.ReadAt(lenBuf, patchTailOffset-8); err != nil {
		return 0, 0, err
	}
	patchDataLen = ReadUint64LE(lenBuf)

	// 验证 PATCHTAIL! 标记
	magic := make([]byte, magicTailLen)
	if _, err := f.ReadAt(magic, patchTailOffset); err != nil {
		return 0, 0, err
	}
	if string(magic) != MagicTail {
		return 0, 0, fmt.Errorf("PATCHTAIL! 标记不匹配")
	}

	return patchTailOffset, patchDataLen, nil
}

// HasEmbeddedPatch 检查可执行文件是否包含嵌入的补丁数据
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

	if fi.Size() < int64(patchTrailerSize+1) {
		return false
	}

	_, _, err = findPatchTail(f, fi)
	return err == nil
}

// RunEmbedded 从自身提取补丁数据并执行升级
func RunEmbedded(exePath string) {
	f, err := os.Open(exePath)
	if err != nil {
		Fatal("❌ 打开自身失败: %v", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		Fatal("❌ 获取文件信息失败: %v", err)
	}

	patchTailOffset, patchDataLen, err := findPatchTail(f, fi)
	if err != nil {
		Fatal("❌ 未找到补丁数据: %v", err)
	}

	patchDataStart := patchTailOffset - 8 - int64(patchDataLen)

	patchData := make([]byte, patchDataLen)
	if _, err := f.ReadAt(patchData, patchDataStart); err != nil {
		Fatal("❌ 读取补丁数据失败: %v", err)
	}

	magicPatchLen := len(MagicPatch)
	if len(patchData) < magicPatchLen || string(patchData[:magicPatchLen]) != MagicPatch {
		Fatal("❌ 无效的补丁数据")
	}

	manifestLen := ReadUint32LE(patchData[magicPatchLen : magicPatchLen+4])
	if int64(magicPatchLen+4)+int64(manifestLen) > int64(len(patchData)) {
		Fatal("❌ manifest 数据损坏")
	}

	var manifest Manifest
	if err := json.Unmarshal(patchData[magicPatchLen+4:magicPatchLen+4+int(manifestLen)], &manifest); err != nil {
		Fatal("❌ 解析 manifest 失败: %v", err)
	}

	offset := int64(magicPatchLen+4) + int64(manifestLen)
	fileCount := ReadUint32LE(patchData[offset : offset+4])
	offset += 4

	fileData := make(map[string][]byte, fileCount)
	for i := uint32(0); i < fileCount; i++ {
		pathLen := ReadUint32LE(patchData[offset : offset+4])
		offset += 4
		path := string(patchData[offset : offset+int64(pathLen)])
		offset += int64(pathLen)
		dataLen := ReadUint64LE(patchData[offset : offset+8])
		offset += 8
		data := make([]byte, dataLen)
		copy(data, patchData[offset:offset+int64(dataLen)])
		offset += int64(dataLen)
		fileData[path] = data
	}

	gameDir := filepath.Dir(exePath)

	fmt.Println("╔══════════════════════════════════════╗")
	fmt.Println("║       🎮 游戏升级工具                ║")
	fmt.Println("╚══════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("📂 游戏目录: %s\n", gameDir)
	fmt.Printf("📊 变更: %d 个操作\n", len(manifest.Operations))
	fmt.Println()
	fmt.Print("确认升级? (Y/n): ")

	var answer string
	fmt.Scanln(&answer)
	if strings.ToLower(answer) == "n" {
		fmt.Println("❌ 已取消。")
		return
	}

	fmt.Println()

	backupDir := filepath.Join(gameDir, "_backup_before_patch")
	os.MkdirAll(backupDir, 0755)

	restoreManifest := RestoreManifest{
		Operations: manifest.Operations,
		GameDir:    gameDir,
	}
	rmj, _ := json.MarshalIndent(restoreManifest, "", "  ")
	os.WriteFile(filepath.Join(backupDir, "restore.json"), rmj, 0644)

	for _, op := range manifest.Operations {
		if op.Action == "delete" || op.Action == "update" {
			src := filepath.Join(gameDir, filepath.FromSlash(op.Path))
			if _, err := os.Stat(src); err == nil {
				dst := filepath.Join(backupDir, filepath.FromSlash(op.Path))
				os.MkdirAll(filepath.Dir(dst), 0755)
				CopyFile(src, dst)
			}
		}
	}

	// 嵌入恢复工具（如果有）
	if restorerBin, err := extractRestorer(exePath); err == nil {
		restorerName := "restorer.exe"
		if runtime.GOOS != "windows" {
			restorerName = "restorer"
		}
		restorerPath := filepath.Join(backupDir, restorerName)
		os.WriteFile(restorerPath, restorerBin, 0755)
		fmt.Printf("📦 恢复工具已放置: _backup_before_patch/%s\n", restorerName)
	} else {
		fmt.Printf("⚠️  未找到嵌入的恢复工具: %v\n", err)
	}

	fmt.Println("🔧 正在升级...")
	total := len(manifest.Operations)
	for i, op := range manifest.Operations {
		full := filepath.Join(gameDir, filepath.FromSlash(op.Path))
		tag := fmt.Sprintf("[%d/%d]", i+1, total)
		switch op.Action {
		case "add":
			fmt.Printf("   %s + %s\n", tag, op.Path)
			os.MkdirAll(filepath.Dir(full), 0755)
			os.WriteFile(full, fileData[op.Path], 0644)
		case "update":
			fmt.Printf("   %s ~ %s\n", tag, op.Path)
			os.WriteFile(full, fileData[op.Path], 0644)
		case "delete":
			fmt.Printf("   %s - %s\n", tag, op.Path)
			os.Remove(full)
		}
	}

	fmt.Println()
	fmt.Println("🔍 验证文件完整性...")
	verifyStart := time.Now()

	verifyMap := make(map[string]string)
	for _, op := range manifest.Operations {
		if op.Action != "delete" && op.Hash != "" {
			verifyMap[op.Path] = op.Hash
		}
	}

	var errs int
	for path, expectedHash := range verifyMap {
		full := filepath.Join(gameDir, filepath.FromSlash(path))
		actualHash := hashFileBuffered(full)
		if actualHash != expectedHash {
			fmt.Printf("   ❌ %s\n", path)
			errs++
		}
	}

	verifyDur := time.Since(verifyStart)

	if errs == 0 {
		fmt.Printf("✅ 升级完成! 验证通过 (%d 个文件, 耗时 %v)\n", len(verifyMap), verifyDur.Round(time.Millisecond))
	} else {
		fmt.Printf("⚠️  完成，但 %d 个文件校验失败 (验证耗时 %v)。备份: _backup_before_patch/\n", errs, verifyDur.Round(time.Millisecond))
	}

	fmt.Println()
	fmt.Println("💡 如需恢复旧版本，请运行 _backup_before_patch/ 中的恢复工具。")
}

// extractRestorer 从可执行文件尾部提取嵌入的恢复工具
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

	fileSize := fi.Size()
	magicRestorerLen := int64(len(MagicRestorer))

	if fileSize < int64(restorerTrailerSize)+1 {
		return nil, fmt.Errorf("文件太小，无恢复工具")
	}

	magic := make([]byte, magicRestorerLen)
	if _, err := f.ReadAt(magic, fileSize-magicRestorerLen); err != nil {
		return nil, err
	}
	if string(magic) != MagicRestorer {
		return nil, fmt.Errorf("未嵌入恢复工具")
	}

	lenBuf := make([]byte, 8)
	if _, err := f.ReadAt(lenBuf, fileSize-int64(restorerTrailerSize)); err != nil {
		return nil, err
	}
	restorerLen := ReadUint64LE(lenBuf)

	restorerStart := fileSize - int64(restorerTrailerSize) - int64(restorerLen)
	data := make([]byte, restorerLen)
	if _, err := f.ReadAt(data, restorerStart); err != nil {
		return nil, err
	}

	return data, nil
}

// CleanupEmpty 递归删除空目录，直到遇到非空目录或 stopAt
func CleanupEmpty(dir, stopAt string) {
	for dir != stopAt {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		os.Remove(dir)
		dir = filepath.Dir(dir)
	}
}

// ── 辅助函数 ──────────────────────────────────────────────

func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func AppendUint32(buf []byte, v uint32) []byte {
	var b [4]byte
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	return append(buf, b[:]...)
}

func AppendUint64(buf []byte, v uint64) []byte {
	var b [8]byte
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	b[4] = byte(v >> 32)
	b[5] = byte(v >> 40)
	b[6] = byte(v >> 48)
	b[7] = byte(v >> 56)
	return append(buf, b[:]...)
}

func WriteUint32(w io.Writer, v uint32) error {
	var b [4]byte
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	_, err := w.Write(b[:])
	return err
}

func WriteUint64(w io.Writer, v uint64) error {
	var b [8]byte
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
	b[4] = byte(v >> 32)
	b[5] = byte(v >> 40)
	b[6] = byte(v >> 48)
	b[7] = byte(v >> 56)
	_, err := w.Write(b[:])
	return err
}

func ReadUint32LE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func ReadUint64LE(b []byte) uint64 {
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}

// Fatal 输出错误信息并退出
func Fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
