package patcher

import (
	"fmt"
	"sort"
)

// ChainStep 是版本链中的一段：把 SourceIndex 版本升级到 SourceIndex+1 版本。
// "版本链"是补丁的核心语义；版本只用链内序号标识，不记录名称。
type ChainStep struct {
	SourceIndex uint32
	Entries     []Entry
}

// checkSteps 校验版本链结构：段号从 1 连续，且每段至少有一个文件变更。
func checkSteps(steps []ChainStep) error {
	if len(steps) == 0 {
		return fmt.Errorf("补丁链为空")
	}
	for i := range steps {
		if steps[i].SourceIndex != uint32(i+1) {
			return fmt.Errorf("第 %d 段的源版本序号应为 %d，实际 %d", i+1, i+1, steps[i].SourceIndex)
		}
		if len(steps[i].Entries) == 0 {
			return fmt.Errorf("第 %d 段没有任何文件变更", i+1)
		}
	}
	return nil
}

// deriveVersionRefs 回放版本链，得到每个版本（1..len(steps)+1）下被触及路径的状态指纹。
//
// 对只被第 j 段触及的路径，它在版本 1..j+1 中的状态都等于第 j 段应用前的状态；
// 之后各版本取其最后一次被触及后的状态。
func deriveVersionRefs(steps []ChainStep) []VersionRef {
	n := len(steps)
	states := make([]map[string]FileState, n+1)
	for i := range states {
		states[i] = make(map[string]FileState)
	}
	for j := range steps {
		for k := range steps[j].Entries {
			en := &steps[j].Entries[k]
			before := FileState{
				Path:   en.Path,
				Exists: en.Action != ActionAdd,
				Size:   en.OldSize,
				Hash:   en.OldHash,
			}
			after := FileState{
				Path:   en.Path,
				Exists: en.Action != ActionDelete,
				Size:   en.NewSize,
				Hash:   en.NewHash,
			}
			// 本段之前：该路径尚未被触及，状态等于本段的输入状态。
			for v := 0; v <= j; v++ {
				if _, ok := states[v][en.Path]; !ok {
					states[v][en.Path] = before
				}
			}
			// 本段之后：本段产物一直保持到被后续某段再次修改为止。
			for v := j + 1; v <= n; v++ {
				states[v][en.Path] = after
			}
		}
	}

	out := make([]VersionRef, 0, len(states))
	for i := range states {
		files := make([]FileState, 0, len(states[i]))
		for _, fs := range states[i] {
			files = append(files, fs)
		}
		sort.Slice(files, func(a, b int) bool { return files[a].Path < files[b].Path })
		out = append(out, VersionRef{Index: uint32(i + 1), Files: files})
	}
	return out
}

// encodeSteps 写出段列表。
func encodeSteps(e *encWriter, steps []ChainStep) {
	e.u32(uint32(len(steps)))
	for i := range steps {
		st := &steps[i]
		e.u32(st.SourceIndex)
		e.u32(uint32(len(st.Entries)))
		for j := range st.Entries {
			encodeEntry(e, &st.Entries[j])
		}
	}
}

// decodeSteps 读入段列表。
func decodeSteps(d *decoder) ([]ChainStep, error) {
	stepCount, err := d.u32()
	if err != nil {
		return nil, err
	}
	if uint64(stepCount) > uint64(d.remaining())+1 {
		return nil, fmt.Errorf("版本段数量异常: %d", stepCount)
	}
	steps := make([]ChainStep, 0, stepCount)
	for i := uint32(0); i < stepCount; i++ {
		srcIndex, err := d.u32()
		if err != nil {
			return nil, fmt.Errorf("第 %d 段: %w", i+1, err)
		}
		entryCount, err := d.u32()
		if err != nil {
			return nil, fmt.Errorf("第 %d 段: %w", i+1, err)
		}
		if uint64(entryCount) > uint64(d.remaining())+1 {
			return nil, fmt.Errorf("第 %d 段条目数量异常: %d", i+1, entryCount)
		}
		st := ChainStep{SourceIndex: srcIndex, Entries: make([]Entry, 0, entryCount)}
		for j := uint32(0); j < entryCount; j++ {
			en, err := decodeEntry(d)
			if err != nil {
				return nil, fmt.Errorf("第 %d 段条目 %d: %w", i+1, j, err)
			}
			st.Entries = append(st.Entries, en)
		}
		steps = append(steps, st)
	}
	if err := checkSteps(steps); err != nil {
		return nil, err
	}
	return steps, nil
}
