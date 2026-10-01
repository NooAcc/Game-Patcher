package patcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// prepareBackupDir 准备备份目录；若已存在且非空，则改名为带时间戳的目录保留。
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

// verifyStageSource 校验某一段差异的源状态是否与当前目录一致。
func verifyStageSource(gameDir string, entries []Entry) error {
	for i := range entries {
		e := &entries[i]
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			return err
		}
		switch e.Action {
		case ActionAdd:
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s 文件已存在，游戏版本不一致（补丁可能已应用）", e.Path)
			} else if !os.IsNotExist(err) {
				return err
			}
		case ActionUpdate, ActionDelete:
			got, err := HashFile(target)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("%s 文件不存在，游戏版本不一致", e.Path)
				}
				return fmt.Errorf("读取目标文件失败 %s: %w", e.Path, err)
			}
			if got != e.OldHash {
				return fmt.Errorf("%s 文件被修改，游戏版本不一致", e.Path)
			}
		default:
			return fmt.Errorf("非法条目行为: %s", e.Action)
		}
	}
	return nil
}

// backupEntries 为尚未记录过的路径建立回滚记录。
// 该路径的当前内容即链起点内容：更早的段没有触及它，因此不会被覆盖。
func backupEntries(gameDir, backupDir string, entries []Entry, manifest *RestoreManifest, recorded map[string]bool) error {
	for i := range entries {
		e := &entries[i]
		if recorded[e.Path] {
			continue
		}
		target, err := SafeJoin(gameDir, e.Path)
		if err != nil {
			return err
		}
		_, statErr := os.Stat(target)
		switch {
		case statErr == nil:
			backupPath, err := SafeJoin(backupDir, e.Path)
			if err != nil {
				return err
			}
			if err := copyFileAtomic(target, backupPath); err != nil {
				return fmt.Errorf("备份 %s 失败: %w", e.Path, err)
			}
			got, err := HashFile(target)
			if err != nil {
				return fmt.Errorf("备份校验 %s 失败: %w", e.Path, err)
			}
			manifest.Entries = append(manifest.Entries, RestoreEntry{Action: "update", Path: e.Path, OldHash: hashHex(got)})
		case os.IsNotExist(statErr):
			manifest.Entries = append(manifest.Entries, RestoreEntry{Action: "add", Path: e.Path})
		default:
			return statErr
		}
		recorded[e.Path] = true
	}
	return nil
}

// applyEntries 执行一段差异（不含校验与备份）。
//
// 同一段内先执行新增/修改、最后执行删除：这样被删除的文件在读取阶段仍然存在，
// 可以作为 OpCopyFrom 的源，从而让"重命名/移动"场景复用块而不必整块存储。
func applyEntries(gameDir string, entries []Entry, pool []Blob) error {
	ctx := opContext{gameDir: gameDir, pool: pool}
	total := len(entries)

	pass := func(deletes bool) error {
		for i := range entries {
			e := &entries[i]
			if (e.Action == ActionDelete) != deletes {
				continue
			}
			target, err := SafeJoin(gameDir, e.Path)
			if err != nil {
				return err
			}
			tag := fmt.Sprintf("[%d/%d]", i+1, total)
			switch e.Action {
			case ActionAdd:
				fmt.Printf("      %s + %s\n", tag, e.Path)
				if err := materialize(ctx, target, "", e.Ops, e.NewHash, e.NewSize); err != nil {
					return fmt.Errorf("新增 %s 失败: %w", e.Path, err)
				}
			case ActionUpdate:
				fmt.Printf("      %s ~ %s\n", tag, e.Path)
				if err := materialize(ctx, target, target, e.Ops, e.NewHash, e.NewSize); err != nil {
					return fmt.Errorf("更新 %s 失败: %w", e.Path, err)
				}
			case ActionDelete:
				fmt.Printf("      %s - %s\n", tag, e.Path)
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
	if err := pass(false); err != nil {
		return err
	}
	return pass(true)
}

// ApplyRelease 从 stages[0] 起逐段应用：每段先校验源状态、再增量备份、随后应用。
// 返回的清单可用于把目录回滚到链起点状态。任何一步失败都会立即返回，
// 调用方应使用返回的清单执行回滚。
func ApplyRelease(gameDir, backupDir string, stages []Stage) (*RestoreManifest, error) {
	manifest := &RestoreManifest{GameDir: gameDir}
	recorded := make(map[string]bool)

	for si := range stages {
		st := &stages[si]
		if err := verifyStageSource(gameDir, st.Entries); err != nil {
			return manifest, fmt.Errorf("第 %d 段源版本校验失败: %w", si+1, err)
		}
		if err := backupEntries(gameDir, backupDir, st.Entries, manifest, recorded); err != nil {
			return manifest, err
		}
		// 先落盘清单再改动文件，进程中途退出也能用 restorer 恢复。
		if err := saveRestoreManifest(backupDir, manifest); err != nil {
			return manifest, fmt.Errorf("写入恢复清单失败: %w", err)
		}
		fmt.Printf("   ▶ 应用第 %d/%d 段差异\n", si+1, len(stages))
		if err := applyEntries(gameDir, st.Entries, st.Pool); err != nil {
			return manifest, fmt.Errorf("第 %d 段应用失败: %w", si+1, err)
		}
	}
	return manifest, nil
}

// materialize 先在临时文件中重建目标，校验哈希后再原子替换。
func materialize(ctx opContext, dst, oldPath string, ops []DeltaOp, wantHash [32]byte, wantSize uint64) error {
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
	if err := applyOps(ctx, oldPath, ops, tmp); err != nil {
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
