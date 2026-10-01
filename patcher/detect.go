package patcher

import (
	"fmt"
	"os"
)

// SourceMismatch 记录某个候选源版本为何不匹配。
type SourceMismatch struct {
	VersionIndex uint32
	Label        string
	Reason       string
}

// DetectSource 按版本升序匹配 gameDir，返回 Sources() 的下标。
// 返回的下标等于 len(Sources())-1 时表示游戏已经是最新版本。
// 全部不匹配时返回 -1 与每个候选源版本的首个失败原因。
func DetectSource(gameDir string, rel *Release) (int, []SourceMismatch, error) {
	if rel == nil || rel.Payload == nil {
		return -1, nil, fmt.Errorf("补丁内容为空")
	}
	sources := rel.Payload.Sources()
	if len(sources) == 0 {
		return -1, nil, fmt.Errorf("补丁不含任何版本信息")
	}
	var mismatches []SourceMismatch
	for i := range sources {
		reason, err := matchVersion(gameDir, &sources[i])
		if err != nil {
			return -1, mismatches, err
		}
		if reason == "" {
			return i, mismatches, nil
		}
		mismatches = append(mismatches, SourceMismatch{
			VersionIndex: sources[i].Index,
			Label:        sources[i].Label,
			Reason:       reason,
		})
	}
	return -1, mismatches, nil
}

// matchVersion 判断 gameDir 是否与版本指纹一致。
// 返回空字符串表示匹配，否则返回首个不匹配原因。
func matchVersion(gameDir string, v *VersionRef) (string, error) {
	for i := range v.Files {
		fs := &v.Files[i]
		target, err := SafeJoin(gameDir, fs.Path)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(target)
		if os.IsNotExist(err) {
			if !fs.Exists {
				continue
			}
			return fmt.Sprintf("%s 文件不存在", fs.Path), nil
		}
		if err != nil {
			return "", err
		}
		if !fs.Exists {
			return fmt.Sprintf("%s 本应不存在，但实际存在", fs.Path), nil
		}
		if uint64(info.Size()) != fs.Size {
			return fmt.Sprintf("%s 大小不匹配（实际 %d，期望 %d）", fs.Path, info.Size(), fs.Size), nil
		}
		got, err := HashFile(target)
		if err != nil {
			return "", err
		}
		if got != fs.Hash {
			return fmt.Sprintf("%s 内容已被修改", fs.Path), nil
		}
	}
	return "", nil
}

// verifyStates 按给定指纹整体校验目录，返回全部不一致项。
func verifyStates(gameDir string, files []FileState) []error {
	var errs []error
	for i := range files {
		fs := &files[i]
		target, err := SafeJoin(gameDir, fs.Path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		info, err := os.Stat(target)
		if os.IsNotExist(err) {
			if fs.Exists {
				errs = append(errs, fmt.Errorf("文件不存在: %s", fs.Path))
			}
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("读取失败: %s: %w", fs.Path, err))
			continue
		}
		if !fs.Exists {
			errs = append(errs, fmt.Errorf("文件本应不存在: %s", fs.Path))
			continue
		}
		if uint64(info.Size()) != fs.Size {
			errs = append(errs, fmt.Errorf("大小不匹配: %s", fs.Path))
			continue
		}
		got, err := HashFile(target)
		if err != nil {
			errs = append(errs, fmt.Errorf("读取失败: %s: %w", fs.Path, err))
			continue
		}
		if got != fs.Hash {
			errs = append(errs, fmt.Errorf("哈希不匹配: %s", fs.Path))
		}
	}
	return errs
}
