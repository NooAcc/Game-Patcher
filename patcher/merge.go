package patcher

import (
	"fmt"
	"strings"
)

// MergeInput 是一份参与合并的补丁输入。
//
// Name / Label 由调用方（merge CLI）解析后给出：Name 是游戏标识，
// Label 是展示名称。Release 既可以是普通单段补丁，也可以是已合并的补丁
// （合并补丁同样不带版本链，因此可以再次参与合并）。
type MergeInput struct {
	Name    string
	Label   string
	Release *Release
}

// MergeReleases 把多份不带版本链的补丁合并成一个合并补丁。
//
// 约束：
//   - 至少两个输入；
//   - 每个输入（或输入内的每个目标）必须恰好包含 1 段差异；
//   - 游戏标识不能为空且不能重复（大小写不敏感）；
//   - 合并结果的字面量池按内容哈希去重，跨目标共享。
//
// 产物必定带 FlagIneligibleAsPrev 标记（约束 C2：合并补丁不能作为 -prev）。
func MergeReleases(inputs []MergeInput) (*Release, error) {
	if len(inputs) < 2 {
		return nil, fmt.Errorf("至少需要 2 份补丁才能合并，实际 %d 份", len(inputs))
	}

	targets := make([]BundleTarget, 0, len(inputs))
	for i := range inputs {
		in := &inputs[i]
		if in.Release == nil {
			return nil, fmt.Errorf("第 %d 份输入为空", i+1)
		}
		extracted, err := extractSingleStepTargets(in)
		if err != nil {
			return nil, err
		}
		targets = append(targets, extracted...)
	}

	seen := make(map[string]bool, len(targets))
	for i := range targets {
		key := strings.ToLower(targets[i].GameID)
		if seen[key] {
			return nil, fmt.Errorf("游戏标识重复: %q（可用 merge 的 -id 指定不同标识）", targets[i].GameID)
		}
		seen[key] = true
	}

	pool, err := mergePools(targets, inputs)
	if err != nil {
		return nil, err
	}

	rel := &Release{
		PatchVersion: 0,
		Flags:        FlagIneligibleAsPrev,
		Bundle:       &BundlePayload{Targets: targets, Pool: pool},
	}
	if err := rel.Bundle.SelfCheck(); err != nil {
		return nil, err
	}
	return rel, nil
}

// extractSingleStepTargets 把一份输入展开成"恰好一段"的目标列表。
// 普通补丁产出 1 个目标；合并补丁产出它内部的所有目标（D2：允许嵌套合并）。
func extractSingleStepTargets(in *MergeInput) ([]BundleTarget, error) {
	rel := in.Release
	switch {
	case rel.Payload != nil && rel.Bundle != nil:
		return nil, fmt.Errorf("输入补丁同时携带两种载荷")
	case rel.Bundle != nil:
		if err := rel.Bundle.SelfCheck(); err != nil {
			return nil, fmt.Errorf("输入补丁本身非法: %w", err)
		}
		out := make([]BundleTarget, len(rel.Bundle.Targets))
		copy(out, rel.Bundle.Targets)
		return out, nil
	case rel.Payload != nil:
		if err := rel.Payload.SelfCheck(); err != nil {
			return nil, fmt.Errorf("输入补丁本身非法: %w", err)
		}
		if len(rel.Payload.Steps) != 1 {
			return nil, fmt.Errorf(
				"输入补丁包含 %d 段版本链，无法合并：请用不带 -prev 的 CLI 重新生成单段补丁",
				len(rel.Payload.Steps))
		}
		if err := validateGameID(in.Name); err != nil {
			return nil, fmt.Errorf("游戏标识非法 %q: %w", in.Name, err)
		}
		return []BundleTarget{{
			GameID: in.Name,
			Label:  in.Label,
			Steps:  rel.Payload.Steps,
		}}, nil
	default:
		return nil, fmt.Errorf("输入补丁内容为空")
	}
}

// mergePools 把所有目标的块池合并成一份去重池，并重写各目标的池引用下标。
//
// 去重键为块的原始内容 BLAKE3；同时校验 RawLen 一致，避免同名不同长的
// 块被静默合并（正常构建不可能出现，出现即为制品损坏）。
func mergePools(targets []BundleTarget, inputs []MergeInput) ([]Blob, error) {
	pool := newBlobPool()

	// 池来源按输入区分：普通补丁用 Payload.Pool，合并补丁内各目标统一用
	// Bundle.Pool。targets 是按输入顺序展开的，因此这里用游标 pos 逐段对齐。
	pos := 0
	for i := range inputs {
		in := &inputs[i]
		var srcPool []Blob
		var count int
		switch {
		case in.Release.Bundle != nil:
			srcPool = in.Release.Bundle.Pool
			count = len(in.Release.Bundle.Targets)
		default:
			srcPool = in.Release.Payload.Pool
			count = 1
		}
		remap := make([]uint32, len(srcPool))
		for j := range srcPool {
			idx, err := internChecked(pool, &srcPool[j])
			if err != nil {
				return nil, err
			}
			remap[j] = idx
		}
		for k := 0; k < count; k++ {
			target := &targets[pos+k]
			for e := range target.Steps[0].Entries {
				en := &target.Steps[0].Entries[e]
				for o := range en.Ops {
					op := &en.Ops[o]
					if op.Kind != OpPoolRef {
						continue
					}
					if int(op.PoolIndex) >= len(remap) {
						return nil, fmt.Errorf("目标 %q 条目 %s 的块池引用越界: %d",
							target.GameID, en.Path, op.PoolIndex)
					}
					op.PoolIndex = remap[op.PoolIndex]
				}
			}
		}
		pos += count
	}
	return pool.blobs, nil
}

// internChecked 把块放入池中并校验同哈希块的原始长度一致。
func internChecked(pool *blobPool, b *Blob) (uint32, error) {
	idx := pool.intern(b.Data)
	got := &pool.blobs[idx]
	if got.RawLen != b.RawLen {
		return 0, fmt.Errorf("块池内容冲突: 哈希 %x 的原始长度 %d != %d", b.Hash, got.RawLen, b.RawLen)
	}
	return idx, nil
}