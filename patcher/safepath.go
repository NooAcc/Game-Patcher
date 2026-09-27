package patcher

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validateRelPath 校验补丁内路径必须是安全的相对路径。
func validateRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("路径为空")
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("路径包含 NUL 字节")
	}
	slashed := strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(slashed, "/") {
		return fmt.Errorf("不允许绝对路径: %q", p)
	}
	if len(slashed) >= 2 && slashed[1] == ':' {
		return fmt.Errorf("不允许盘符路径: %q", p)
	}
	for _, part := range strings.Split(slashed, "/") {
		switch part {
		case "":
			return fmt.Errorf("路径包含空片段: %q", p)
		case ".", "..":
			return fmt.Errorf("路径包含非法片段 %q: %q", part, p)
		}
	}
	return nil
}

// SafeJoin 将补丁内的相对路径安全地拼接到 root 下，阻止路径穿越。
func SafeJoin(root, rel string) (string, error) {
	if err := validateRelPath(rel); err != nil {
		return "", err
	}
	normalized := filepath.FromSlash(strings.ReplaceAll(rel, "\\", "/"))
	joined := filepath.Join(root, normalized)

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joinedAbs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	check, err := filepath.Rel(rootAbs, joinedAbs)
	if err != nil {
		return "", err
	}
	if check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) || filepath.IsAbs(check) {
		return "", fmt.Errorf("路径越界: %q", rel)
	}
	return joined, nil
}
