package patcher

import (
	"bytes"
	"testing"
)

// sampleBundleRelease 构造一份双目标合并补丁：game-a 修改 a.bin，game-b 新增 b.bin。
func sampleBundleRelease() *Release {
	return &Release{
		Flags: FlagIneligibleAsPrev,
		Bundle: &BundlePayload{
			Targets: []BundleTarget{
				{
					GameID: "game-a",
					Label:  "游戏A",
					Steps: []ChainStep{{SourceIndex: 1, Entries: []Entry{
						{
							Path: "a.bin", Action: ActionUpdate,
							OldHash: HashBytes([]byte("a1")), NewHash: HashBytes([]byte("a2")),
							OldSize: 2, NewSize: 2,
							Ops: []DeltaOp{{Kind: OpPoolRef, PoolIndex: 0, Length: 2}},
						},
					}}},
				},
				{
					GameID: "game-b",
					Label:  "游戏B",
					Steps: []ChainStep{{SourceIndex: 1, Entries: []Entry{
						{
							Path: "b.bin", Action: ActionAdd,
							NewHash: HashBytes([]byte("ab")), NewSize: 2,
							Ops: []DeltaOp{{Kind: OpPoolRef, PoolIndex: 0, Length: 2}},
						},
					}}},
				},
			},
			Pool: []Blob{{Hash: HashBytes([]byte("ab")), Comp: CompRaw, RawLen: 2, Data: []byte("ab")}},
		},
	}
}

func TestBundleReleaseEncodeDecodeRoundTrip(t *testing.T) {
	rel := sampleBundleRelease()
	got, err := DecodeRelease(encodeReleaseBytes(t, rel))
	if err != nil {
		t.Fatal(err)
	}
	if got.Bundle == nil {
		t.Fatal("合并数据丢失")
	}
	if got.Payload != nil {
		t.Fatal("合并补丁不应带单链载荷")
	}
	if got.Flags != FlagIneligibleAsPrev {
		t.Fatalf("标记位丢失: %d", got.Flags)
	}
	if got.PatchVersion != 0 {
		t.Fatalf("合并补丁版本号应为 0，实际 %d", got.PatchVersion)
	}
	src := rel.Bundle
	if len(got.Bundle.Targets) != len(src.Targets) {
		t.Fatalf("目标数量不一致: %d != %d", len(got.Bundle.Targets), len(src.Targets))
	}
	for i := range src.Targets {
		a, b := src.Targets[i], got.Bundle.Targets[i]
		if a.GameID != b.GameID || a.Label != b.Label {
			t.Fatalf("目标 %d 标识不一致: %+v != %+v", i, a, b)
		}
		if len(a.Steps) != len(b.Steps) {
			t.Fatalf("目标 %d 段数不一致", i)
		}
		for j := range a.Steps {
			ae, be := a.Steps[j].Entries, b.Steps[j].Entries
			if a.Steps[j].SourceIndex != b.Steps[j].SourceIndex || len(ae) != len(be) {
				t.Fatalf("目标 %d 第 %d 段不一致", i, j)
			}
			for k := range ae {
				if ae[k].Path != be[k].Path || ae[k].Action != be[k].Action ||
					ae[k].OldHash != be[k].OldHash || ae[k].NewHash != be[k].NewHash ||
					ae[k].OldSize != be[k].OldSize || ae[k].NewSize != be[k].NewSize {
					t.Fatalf("目标 %d 条目 %d 不一致", i, k)
				}
			}
		}
	}
	if len(got.Bundle.Pool) != len(src.Pool) {
		t.Fatalf("块池不一致: %d != %d", len(got.Bundle.Pool), len(src.Pool))
	}
}

func TestEncodeReleaseRejectsBadPayloadCombination(t *testing.T) {
	both := sampleBundleRelease()
	both.Payload = NewChunkPayload([]ChainStep{{SourceIndex: 1, Entries: []Entry{{Path: "x", Action: ActionDelete}}}}, nil)
	var buf bytes.Buffer
	if err := EncodeRelease(&buf, both); err == nil {
		t.Fatal("同时携带两种载荷应被拒绝")
	}

	neither := &Release{}
	buf.Reset()
	if err := EncodeRelease(&buf, neither); err == nil {
		t.Fatal("两种载荷都为空应被拒绝")
	}
}

func TestBundleSelfCheckRejectsInvalid(t *testing.T) {
	valid := sampleBundleRelease().Bundle

	cases := []struct {
		name string
		in   *BundlePayload
	}{
		{"空目标列表", &BundlePayload{}},
		{"目标缺少段", &BundlePayload{Targets: []BundleTarget{{GameID: "a"}}}},
		{"目标包含多段", &BundlePayload{Targets: []BundleTarget{{GameID: "a", Steps: []ChainStep{
			{SourceIndex: 1, Entries: []Entry{{Path: "x", Action: ActionDelete}}},
			{SourceIndex: 2, Entries: []Entry{{Path: "y", Action: ActionDelete}}},
		}}}}},
		{"目标段号非法", &BundlePayload{Targets: []BundleTarget{{GameID: "a", Steps: []ChainStep{
			{SourceIndex: 2, Entries: []Entry{{Path: "x", Action: ActionDelete}}},
		}}}}},
		{"重复游戏标识", &BundlePayload{Targets: []BundleTarget{
			{GameID: "dup", Steps: []ChainStep{{SourceIndex: 1, Entries: []Entry{{Path: "x", Action: ActionDelete}}}}},
			{GameID: "DUP", Steps: []ChainStep{{SourceIndex: 1, Entries: []Entry{{Path: "y", Action: ActionDelete}}}}},
		}}},
		{"游戏标识非法字符", &BundlePayload{Targets: []BundleTarget{
			{GameID: "bad/id", Steps: []ChainStep{{SourceIndex: 1, Entries: []Entry{{Path: "x", Action: ActionDelete}}}}},
		}}},
		{"块池引用越界", &BundlePayload{Targets: []BundleTarget{
			{GameID: "a", Steps: []ChainStep{{SourceIndex: 1, Entries: []Entry{{
				Path: "x", Action: ActionAdd, NewSize: 1,
				Ops: []DeltaOp{{Kind: OpPoolRef, PoolIndex: 3, Length: 1}},
			}}}}},
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.in.SelfCheck(); err == nil {
				t.Fatal("非法合并数据应被拒绝")
			}
		})
	}

	if err := valid.SelfCheck(); err != nil {
		t.Fatalf("合法合并数据被误拒: %v", err)
	}
}

func TestDecodeReleaseRejectsUnknownFlagBits(t *testing.T) {
	blob := encodeReleaseBytes(t, sampleBundleRelease())
	// 信封布局: magic(6) + formatVersion(1) + payloadKind(1) + flags(1)
	flagsOff := len(patchMagic) + 2
	blob[flagsOff] = 0x80
	if _, err := DecodeRelease(blob); err == nil {
		t.Fatal("未知标记位应被拒绝")
	}
}