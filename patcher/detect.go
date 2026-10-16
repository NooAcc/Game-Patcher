package patcher

import (
	"fmt"
	"os"
	"strings"
)

// SourceMismatch 记录某个候选源版本为何不匹配。
type SourceMismatch struct {
	VersionIndex uint32
	Reason       string
}

// DetectSource 按版本升序匹配 gameDir，返回 Sources() 的下标。
// 返回的下标等于 len(Sources())-1 时表示游戏已经是最新版本。
// 全部不匹配时返回 -1 与每个候选源版本的首个失败原因。
func DetectSource(gameDir string, rel *Release) (int, []SourceMismatch, error) {
	if rel == nil {
		return -1, nil, fmt.Errorf("补丁内容为空")
	}
	if rel.Payload == nil {
		if rel.Bundle != nil {
			return -1, nil, fmt.Errorf("该补丁是合并补丁，请使用目标匹配接口")
		}
		return -1, nil, fmt.Errorf("补丁内容为空")
	}
	return detectSourceForSteps(gameDir, rel.Payload.Steps)
}

// detectSourceForSteps 按版本升序匹配 gameDir 与给定版本链的各个版本指纹。
//
// 合并补丁的每个目标都是"单段链"，因此与普通补丁共用这条匹配路径，
// 保证两种载荷的识别行为完全一致。
func detectSourceForSteps(gameDir string, steps []ChainStep) (int, []SourceMismatch, error) {
	sources := deriveVersionRefs(steps)
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
			Reason:       reason,
		})
	}
	return -1, mismatches, nil
}

// matchedBundleTargets 返回与 gameDir 匹配的合并目标。
//
//   - target 非空时只考虑该 gameID 的目标（决策 D1 的逃生舱口）；
//   - 返回空切片表示没有任何目标匹配；
//   - 返回多条表示歧义，由调用方决定如何提示（不在此处静默取首个）。
func matchedBundleTargets(gameDir string, b *BundlePayload, target string) ([]BundleTarget, error) {
	if b == nil {
		return nil, fmt.Errorf("合并数据为空")
	}
	if err := b.SelfCheck(); err != nil {
		return nil, err
	}
	if target != "" {
		if err := validateGameID(target); err != nil {
			return nil, fmt.Errorf("指定的目标标识非法 %q: %w", target, err)
		}
	}

	var matched []BundleTarget
	found := false
	for i := range b.Targets {
		t := &b.Targets[i]
		if target != "" {
			if !strings.EqualFold(t.GameID, target) {
				continue
			}
			found = true
		}
		idx, _, err := detectSourceForSteps(gameDir, t.Steps)
		if err != nil {
			return nil, fmt.Errorf("目标 %q: %w", t.DisplayName(), err)
		}
		if idx >= 0 {
			matched = append(matched, *t)
		}
	}
	if target != "" && !found {
		return nil, fmt.Errorf("补丁中不存在目标 %q", target)
	}
	return matched, nil
}

// detectBundleTarget 返回唯一匹配的合并目标下标；多目标命中时返回歧义错误。
//
// 第二个返回值是所有目标的首个失败原因，供"全部不匹配"时向用户解释。
func detectBundleTarget(gameDir string, b *BundlePayload) (int, []SourceMismatch, error) {
	matched, err := matchedBundleTargets(gameDir, b, "")
	if err != nil {
		return -1, nil, err
	}
	if len(matched) == 1 {
		for i := range b.Targets {
			if b.Targets[i].GameID == matched[0].GameID {
				return i, nil, nil
			}
		}
	}
	if len(matched) > 1 {
		names := make([]string, 0, len(matched))
		for i := range matched {
			names = append(names, matched[i].GameID)
		}
		return -1, nil, fmt.Errorf("同时匹配到 %d 个目标（%s），无法确定应用哪一个",
			len(matched), strings.Join(names, "、"))
	}

	var mismatches []SourceMismatch
	for i := range b.Targets {
		t := &b.Targets[i]
		_, ms, err := detectSourceForSteps(gameDir, t.Steps)
		if err != nil {
			return -1, mismatches, err
		}
		reason := "未知原因"
		if len(ms) > 0 {
			reason = ms[0].Reason
		}
		mismatches = append(mismatches, SourceMismatch{
			VersionIndex: uint32(i + 1),
			Reason:       fmt.Sprintf("%s: %s", t.DisplayName(), reason),
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