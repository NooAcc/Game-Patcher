package patcher

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// countWriter 统计写入字节数，用于在流式编码后回填补丁数据长度。
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// writePatchExecutable 将基础 EXE + 补丁制品 + 尾部标记（+可选 restorer）
// 原子地写为单个可执行文件。
func writePatchExecutable(baseExe string, rel *Release, outputPath, restorerPath string) error {
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

	cw := &countWriter{w: tmp}
	if err := EncodeRelease(cw, rel); err != nil {
		return fail(fmt.Errorf("编码补丁数据失败: %w", err))
	}
	if err := writeU64(tmp, uint64(cw.n)); err != nil {
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

// readEmbeddedRelease 读取可执行文件尾部的补丁制品。
func readEmbeddedRelease(exePath string) (*Release, error) {
	f, err := os.Open(exePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	blobOff, blobLen, err := findPatchTail(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("未找到补丁数据: %w", err)
	}
	if !fitsInt(blobLen) {
		return nil, fmt.Errorf("补丁数据长度异常: %d", blobLen)
	}
	blob := make([]byte, blobLen)
	if _, err := f.ReadAt(blob, blobOff); err != nil {
		return nil, fmt.Errorf("读取补丁数据失败: %w", err)
	}
	return DecodeRelease(blob)
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

func restorerFileName() string {
	if runtime.GOOS == "windows" {
		return "restorer.exe"
	}
	return "restorer"
}
