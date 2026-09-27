package patcher

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Fatal 输出错误信息并退出进程。
func Fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// FormatSize 格式化字节数。
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

// writeAtomic 先写同目录临时文件，成功后 rename 覆盖目标。
func writeAtomic(dst string, fn func(w io.Writer) error) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gamepatch-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := fn(tmp); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// copyFileAtomic 原子复制文件。
func copyFileAtomic(src, dst string) error {
	return writeAtomic(dst, func(w io.Writer) error {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
}

// cleanupEmptyDirs 删除 dir 及其空父目录，直到 stopAt 或遇到非空目录。
// 若 dir 不在 stopAt 之内则立即返回，避免误删目录树之外的目录。
func cleanupEmptyDirs(dir, stopAt string) {
	stop := filepath.Clean(stopAt)
	cur := filepath.Clean(dir)
	for {
		if cur == stop {
			return
		}
		rel, err := filepath.Rel(stop, cur)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return
		}
		entries, err := os.ReadDir(cur)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(cur); err != nil {
			return
		}
		cur = filepath.Dir(cur)
	}
}

// fitsInt 判断 uint64 是否能安全转换为 int。
func fitsInt(n uint64) bool {
	return n <= uint64(^uint(0)>>1)
}

// Pause 等待用户按回车，避免双击运行时控制台窗口立即关闭。
// 标准输入已结束（管道/CI/重定向）时立即返回，不会阻塞。
func Pause() {
	fmt.Println()
	fmt.Println("按回车键退出...")
	var b [1]byte
	_, _ = os.Stdin.Read(b[:])
}
