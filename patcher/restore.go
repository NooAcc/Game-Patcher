package patcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RestoreEntry 描述一个文件的回滚方式。
type RestoreEntry struct {
	Action  string `json:"action"` // add / update / delete
	Path    string `json:"path"`
	OldHash string `json:"old_hash,omitempty"` // 升级前文件的 BLAKE3 十六进制
}

// RestoreManifest 是备份目录中的 restore.json。
type RestoreManifest struct {
	GameDir string         `json:"game_dir,omitempty"` // 仅供显示，实际以备份目录父目录为准
	Entries []RestoreEntry `json:"entries"`
}

const restoreManifestName = "restore.json"

// LoadRestoreManifest 读取备份目录中的恢复清单。
func LoadRestoreManifest(backupDir string) (*RestoreManifest, error) {
	data, err := os.ReadFile(filepath.Join(backupDir, restoreManifestName))
	if err != nil {
		return nil, fmt.Errorf("无法读取恢复清单（应位于备份目录 %s）: %w", backupDir, err)
	}
	var m RestoreManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析恢复清单失败: %w", err)
	}
	if len(m.Entries) == 0 {
		return nil, fmt.Errorf("恢复清单为空")
	}
	for i := range m.Entries {
		e := &m.Entries[i]
		if err := validateRelPath(e.Path); err != nil {
			return nil, fmt.Errorf("恢复清单第 %d 项路径非法: %w", i, err)
		}
		switch e.Action {
		case "add":
		case "update", "delete":
			if _, err := parseHashHex(e.OldHash); err != nil {
				return nil, fmt.Errorf("恢复清单第 %d 项 %s: %w", i, e.Path, err)
			}
		default:
			return nil, fmt.Errorf("恢复清单第 %d 项行为非法: %q", i, e.Action)
		}
	}
	return &m, nil
}

func saveRestoreManifest(backupDir string, m *RestoreManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(backupDir, restoreManifestName), func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// Restore 把备份目录中的旧文件恢复到游戏目录（备份目录的父目录）。
// 每个 update/delete 条目恢复后会校验内容哈希；add 条目会删除新增文件。
func Restore(m *RestoreManifest, backupDir string) error {
	if m == nil {
		return fmt.Errorf("恢复清单为空")
	}
	backupDir, err := filepath.Abs(backupDir)
	if err != nil {
		return err
	}
	gameDir := filepath.Dir(backupDir)

	var errs []error
	for i := range m.Entries {
		e := &m.Entries[i]
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Path, err))
			continue
		}
		switch e.Action {
		case "add":
			// 回滚新增：删除升级时新增的文件。
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("删除 %s 失败: %w", e.Path, err))
				continue
			}
			cleanupEmptyDirs(filepath.Dir(target), gameDir)
		case "update", "delete":
			// 回滚修改/删除：从备份恢复旧文件。
			backupPath, err := SafeJoin(backupDir, e.Path)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", e.Path, err))
				continue
			}
			if err := copyFileAtomic(backupPath, target); err != nil {
				errs = append(errs, fmt.Errorf("恢复 %s 失败: %w", e.Path, err))
				continue
			}
			want, err := parseHashHex(e.OldHash)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", e.Path, err))
				continue
			}
			got, err := HashFile(target)
			if err != nil {
				errs = append(errs, fmt.Errorf("校验 %s 失败: %w", e.Path, err))
				continue
			}
			if got != want {
				errs = append(errs, fmt.Errorf("恢复后哈希不匹配: %s", e.Path))
			}
		default:
			errs = append(errs, fmt.Errorf("%s: 非法行为 %q", e.Path, e.Action))
		}
	}

	// 最终整体校验。
	for i := range m.Entries {
		e := &m.Entries[i]
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		switch e.Action {
		case "add":
			if _, err := os.Stat(target); err == nil {
				errs = append(errs, fmt.Errorf("新增文件未被删除: %s", e.Path))
			}
		case "update", "delete":
			want, err := parseHashHex(e.OldHash)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			got, err := HashFile(target)
			if err != nil {
				errs = append(errs, fmt.Errorf("文件未恢复: %s: %w", e.Path, err))
				continue
			}
			if got != want {
				errs = append(errs, fmt.Errorf("文件内容不正确: %s", e.Path))
			}
		}
	}
	return errors.Join(errs...)
}
