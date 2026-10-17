package patcher

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// BundleTarget 是合并补丁中的一个独立目标：一款游戏的一次状态迁移。
//
// 约束 C1：参与合并的补丁不带版本链，因此 Steps 长度恒为 1。
type BundleTarget struct {
	GameID string      // 路由与展示标识，可为空
	Label  string      // 展示名称；为空时调用方回退为 GameID
	Steps  []ChainStep // 恒为 1 段
}

// DisplayName 返回面向用户的名称，缺省回退为 GameID。
func (t *BundleTarget) DisplayName() string {
	if strings.TrimSpace(t.Label) != "" {
		return t.Label
	}
	return t.GameID
}

// BundlePayload 是合并补丁的差异数据：多个独立目标 + 跨目标共享字面量池。
//
// 与 ChunkPayload 的关系：二进制布局分叉（这里没有版本链语义），
// 但应用层强制统一——每个目标仍是"单段链"，可直接复用
// deriveVersionRefs / DetectSource / ApplyRelease。
type BundlePayload struct {
	Targets []BundleTarget
	Pool    []Blob
}

// Kind 返回差异后端类型。
func (b *BundlePayload) Kind() PayloadKind { return PayloadBundle }

// SelfCheck 校验合并数据的结构完整性与所有块池引用的合法性。
func (b *BundlePayload) SelfCheck() error {
	if b == nil {
		return fmt.Errorf("合并数据为空")
	}
	if len(b.Targets) == 0 {
		return fmt.Errorf("合并补丁不含任何目标")
	}
	seen := make(map[string]bool, len(b.Targets))
	for i := range b.Targets {
		t := &b.Targets[i]
		if err := validateGameID(t.GameID); err != nil {
			return fmt.Errorf("目标 %d 标识非法: %w", i, err)
		}
		key := strings.ToLower(t.GameID)
		if seen[key] {
			return fmt.Errorf("目标 %d 标识重复: %q", i, t.GameID)
		}
		seen[key] = true

		if len(t.Steps) != 1 {
			return fmt.Errorf("目标 %q 必须恰好包含 1 段差异，实际 %d 段（合并只接受不带版本链的补丁）",
				t.GameID, len(t.Steps))
		}
		if err := checkSteps(t.Steps); err != nil {
			return fmt.Errorf("目标 %q: %w", t.GameID, err)
		}
		for j := range t.Steps[0].Entries {
			en := &t.Steps[0].Entries[j]
			if err := checkEntryPoolRefs(en, len(b.Pool)); err != nil {
				return fmt.Errorf("目标 %q 条目 %s: %w", t.GameID, en.Path, err)
			}
		}
	}
	return nil
}

// Encode 写出合并数据载荷。
func (b *BundlePayload) Encode(w io.Writer) error {
	if err := b.SelfCheck(); err != nil {
		return err
	}
	e := &encWriter{w: w}
	e.u8(bundleVersion)
	e.u32(uint32(len(b.Targets)))
	for i := range b.Targets {
		t := &b.Targets[i]
		e.str(t.GameID)
		e.str(t.Label)
		encodeSteps(e, t.Steps)
	}
	encodeBlobs(e, b.Pool)
	return e.err
}

func decodeBundlePayload(d *decoder) (*BundlePayload, error) {
	ver, err := d.u8()
	if err != nil {
		return nil, fmt.Errorf("合并数据版本: %w", err)
	}
	if ver != bundleVersion {
		return nil, fmt.Errorf("不支持的合并数据版本: %d", ver)
	}
	count, err := d.u32()
	if err != nil {
		return nil, fmt.Errorf("目标数量: %w", err)
	}
	if uint64(count) > uint64(d.remaining())+1 {
		return nil, fmt.Errorf("目标数量异常: %d", count)
	}
	targets := make([]BundleTarget, 0, count)
	for i := uint32(0); i < count; i++ {
		id, err := d.str()
		if err != nil {
			return nil, fmt.Errorf("目标 %d 标识: %w", i, err)
		}
		label, err := d.str()
		if err != nil {
			return nil, fmt.Errorf("目标 %d 名称: %w", i, err)
		}
		steps, err := decodeSteps(d)
		if err != nil {
			return nil, fmt.Errorf("目标 %d: %w", i, err)
		}
		targets = append(targets, BundleTarget{GameID: id, Label: label, Steps: steps})
	}
	pool, err := decodeBlobs(d)
	if err != nil {
		return nil, err
	}
	b := &BundlePayload{Targets: targets, Pool: pool}
	if err := b.SelfCheck(); err != nil {
		return nil, err
	}
	return b, nil
}

// checkEntryPoolRefs 校验条目内所有块池引用与跨文件源是否合法。
func checkEntryPoolRefs(en *Entry, poolSize int) error {
	for k := range en.Ops {
		op := &en.Ops[k]
		switch op.Kind {
		case OpPoolRef:
			if int(op.PoolIndex) >= poolSize {
				return fmt.Errorf("块池引用越界: %d（池大小 %d）", op.PoolIndex, poolSize)
			}
		case OpCopyFrom:
			if err := validateRelPath(op.SrcPath); err != nil {
				return fmt.Errorf("跨文件源非法: %w", err)
			}
		}
	}
	return nil
}

// validateGameID 校验用户可见的游戏标识。
// 标识同时作为运行时展示名称，因此允许中文、空格等字符；仍禁止控制字符和
// 路径分隔符，避免影响命令行输出、日志与 Windows 路径习惯。
func validateGameID(id string) error {
	if id == "" {
		return fmt.Errorf("长度非法: 0")
	}
	if utf8.RuneCountInString(id) > 64 {
		return fmt.Errorf("长度非法: %d 个字符", utf8.RuneCountInString(id))
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("含控制字符 %q", r)
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return fmt.Errorf("含非法字符 %q", r)
		}
	}
	return nil
}