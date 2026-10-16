package patcher

import (
	"testing"
)

// singleStepRelease 构造一份用于合并的单段补丁。
func singleStepRelease(version uint32, path string, oldData, newData string) *Release {
	return &Release{
		PatchVersion: version,
		Payload: NewChunkPayload(
			[]ChainStep{{SourceIndex: 1, Entries: []Entry{
				{
					Path: path, Action: ActionUpdate,
					OldHash: HashBytes([]byte(oldData)), NewHash: HashBytes([]byte(newData)),
					OldSize: uint64(len(oldData)), NewSize: uint64(len(newData)),
					Ops: []DeltaOp{{Kind: OpPoolRef, PoolIndex: 0, Length: uint64(len(newData))}},
				},
			}}},
			[]Blob{{Hash: HashBytes([]byte(newData)), Comp: CompRaw, RawLen: uint64(len(newData)), Data: []byte(newData)}},
		),
	}
}

func TestMergeReleasesBuildsBundle(t *testing.T) {
	relA := singleStepRelease(1, "a.bin", "a1", "a2")
	relB := singleStepRelease(1, "b.bin", "b1", "b2")

	merged, err := MergeReleases([]MergeInput{
		{Name: "game-a", Label: "游戏A", Release: relA},
		{Name: "game-b", Label: "游戏B", Release: relB},
	})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Bundle == nil {
		t.Fatal("应产出合并载荷")
	}
	if merged.Payload != nil {
		t.Fatal("合并补丁不应带单链载荷")
	}
	if merged.Flags&FlagIneligibleAsPrev == 0 {
		t.Fatal("合并补丁必须标记为不可作为 -prev")
	}
	if merged.PatchVersion != 0 {
		t.Fatalf("合并补丁版本号应为 0，实际 %d", merged.PatchVersion)
	}
	if len(merged.Bundle.Targets) != 2 {
		t.Fatalf("目标数量应为 2，实际 %d", len(merged.Bundle.Targets))
	}
	if merged.Bundle.Targets[0].GameID != "game-a" || merged.Bundle.Targets[0].Label != "游戏A" {
		t.Fatalf("目标 A 标识不正确: %+v", merged.Bundle.Targets[0])
	}
	if err := merged.Bundle.SelfCheck(); err != nil {
		t.Fatalf("合并结果未通过自检: %v", err)
	}
}

func TestMergeReleasesDedupsSharedPool(t *testing.T) {
	relA := singleStepRelease(1, "a.bin", "x", "same")
	relB := singleStepRelease(1, "b.bin", "y", "same")

	merged, err := MergeReleases([]MergeInput{
		{Name: "game-a", Release: relA},
		{Name: "game-b", Release: relB},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Bundle.Pool) != 1 {
		t.Fatalf("相同内容应只保留一份，实际 %d 份", len(merged.Bundle.Pool))
	}
	for i, target := range merged.Bundle.Targets {
		ops := target.Steps[0].Entries[0].Ops
		if len(ops) != 1 || ops[0].PoolIndex != 0 {
			t.Fatalf("目标 %d 的引用未重映射: %+v", i, ops)
		}
	}
}

func TestMergeReleasesRejectsUnqualifiedInputs(t *testing.T) {
	// 带版本链的输入必须被拒绝（约束 C1）
	chained := &Release{
		PatchVersion: 2,
		Payload: NewChunkPayload([]ChainStep{
			{SourceIndex: 1, Entries: []Entry{{Path: "a", Action: ActionDelete, OldHash: HashBytes([]byte("a"))}}},
			{SourceIndex: 2, Entries: []Entry{{Path: "b", Action: ActionDelete, OldHash: HashBytes([]byte("b"))}}},
		}, nil),
	}
	if _, err := MergeReleases([]MergeInput{
		{Name: "game-a", Release: chained},
		{Name: "game-b", Release: singleStepRelease(1, "b.bin", "y", "z")},
	}); err == nil {
		t.Fatal("带版本链的输入应被拒绝")
	}

	// 重复标识必须被拒绝
	if _, err := MergeReleases([]MergeInput{
		{Name: "same", Release: singleStepRelease(1, "a.bin", "a", "b")},
		{Name: "SAME", Release: singleStepRelease(1, "b.bin", "c", "d")},
	}); err == nil {
		t.Fatal("重复标识应被拒绝")
	}

	// 少于两个输入必须被拒绝
	if _, err := MergeReleases([]MergeInput{{Name: "only", Release: singleStepRelease(1, "a.bin", "a", "b")}}); err == nil {
		t.Fatal("少于两个输入应被拒绝")
	}
}

func TestMergeReleasesUnwrapsNestedBundles(t *testing.T) {
	// 合并补丁同样不带版本链，因此可作为合并输入（决策 D2）
	inner, err := MergeReleases([]MergeInput{
		{Name: "game-a", Release: singleStepRelease(1, "a.bin", "a", "b")},
		{Name: "game-b", Release: singleStepRelease(1, "b.bin", "c", "d")},
	})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := MergeReleases([]MergeInput{
		{Name: "combined", Release: inner},
		{Name: "game-c", Release: singleStepRelease(1, "c.bin", "e", "f")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outer.Bundle.Targets) != 3 {
		t.Fatalf("嵌套合并应展开为 3 个目标，实际 %d", len(outer.Bundle.Targets))
	}
	ids := make(map[string]bool)
	for _, tg := range outer.Bundle.Targets {
		ids[tg.GameID] = true
	}
	for _, want := range []string{"game-a", "game-b", "game-c"} {
		if !ids[want] {
			t.Fatalf("缺少目标 %q: %+v", want, ids)
		}
	}
}