package patcher

import (
	"fmt"
	"io"
)

const minChunkEncodedSize = 1 + 32 + 8 + 8 // comp + rawHash + rawLen + dataLen

// ChunkPayload 是补丁的差异数据：版本链 + 共享字面量池 + 跨文件块复用。
//
//   - 版本链决定"能从哪些版本升级到目标版本"，并由各段回放推导出检测指纹；
//   - 所有段的字面量统一放进去重的 Pool，条目以 OpPoolRef 引用；
//   - 同一段内，新增/修改文件可复用"本段被删除文件"的块（OpCopyFrom），
//     因此重命名/移动大文件时不再整块存储。
type ChunkPayload struct {
	Steps []ChainStep
	Pool  []Blob
}

// NewChunkPayload 组装一个补丁后端。
func NewChunkPayload(steps []ChainStep, pool []Blob) *ChunkPayload {
	return &ChunkPayload{Steps: steps, Pool: pool}
}

func (c *ChunkPayload) Kind() PayloadKind { return PayloadChunk }

// Sources 返回按版本升序排列的版本指纹（含最终目标版本）。
func (c *ChunkPayload) Sources() []VersionRef {
	return deriveVersionRefs(c.Steps)
}

// Target 返回最终目标版本的指纹。
func (c *ChunkPayload) Target() VersionRef {
	sources := c.Sources()
	if len(sources) == 0 {
		return VersionRef{}
	}
	return sources[len(sources)-1]
}

// StagesFrom 返回从 Sources()[sourceIdx] 升级到目标所需的阶段序列，并为每段注入共享块池。
func (c *ChunkPayload) StagesFrom(sourceIdx int) []Stage {
	if sourceIdx < 0 {
		sourceIdx = 0
	}
	out := make([]Stage, 0, len(c.Steps))
	for j := sourceIdx; j < len(c.Steps); j++ {
		out = append(out, Stage{
			SourceIndex: c.Steps[j].SourceIndex,
			Entries:     c.Steps[j].Entries,
			Pool:        c.Pool,
		})
	}
	return out
}

// SelfCheck 校验版本链结构，并确认所有块池引用与跨文件源都在合法范围内。
func (c *ChunkPayload) SelfCheck() error {
	if err := checkSteps(c.Steps); err != nil {
		return err
	}
	for i := range c.Steps {
		for j := range c.Steps[i].Entries {
			en := &c.Steps[i].Entries[j]
			for k := range en.Ops {
				op := &en.Ops[k]
				switch op.Kind {
				case OpPoolRef:
					if int(op.PoolIndex) >= len(c.Pool) {
						return fmt.Errorf("第 %d 段条目 %s 的块池引用越界: %d（池大小 %d）", i+1, en.Path, op.PoolIndex, len(c.Pool))
					}
					if c.Pool[op.PoolIndex].RawLen != op.Length {
						return fmt.Errorf("第 %d 段条目 %s 的块池引用长度不匹配", i+1, en.Path)
					}
				case OpCopyFrom:
					if err := validateRelPath(op.SrcPath); err != nil {
						return fmt.Errorf("第 %d 段条目 %s 的跨文件源非法: %w", i+1, en.Path, err)
					}
				}
			}
		}
	}
	return nil
}

func (c *ChunkPayload) Encode(w io.Writer) error {
	if err := c.SelfCheck(); err != nil {
		return err
	}
	e := &encWriter{w: w}
	encodeSteps(e, c.Steps)
	e.u32(uint32(len(c.Pool)))
	for i := range c.Pool {
		b := &c.Pool[i]
		if b.Comp != CompRaw && b.Comp != CompFlate {
			e.fail(fmt.Errorf("块池第 %d 项压缩方式非法: %d", i, b.Comp))
			return e.err
		}
		e.u8(byte(b.Comp))
		e.raw(b.Hash[:])
		e.u64(b.RawLen)
		e.u64(uint64(len(b.Data)))
		e.raw(b.Data)
	}
	return e.err
}

func decodeChunkPayload(d *decoder) (*ChunkPayload, error) {
	steps, err := decodeSteps(d)
	if err != nil {
		return nil, err
	}

	count, err := d.u32()
	if err != nil {
		return nil, err
	}
	if uint64(count) > uint64(d.remaining()/minChunkEncodedSize)+1 {
		return nil, fmt.Errorf("块池数量异常: %d", count)
	}
	pool := make([]Blob, 0, count)
	for i := uint32(0); i < count; i++ {
		comp, err := d.u8()
		if err != nil {
			return nil, fmt.Errorf("块池第 %d 项: %w", i, err)
		}
		if CompMethod(comp) != CompRaw && CompMethod(comp) != CompFlate {
			return nil, fmt.Errorf("块池第 %d 项压缩方式非法: %d", i, comp)
		}
		rawHash, err := d.hash()
		if err != nil {
			return nil, fmt.Errorf("块池第 %d 项: %w", i, err)
		}
		rawLen, err := d.u64()
		if err != nil {
			return nil, fmt.Errorf("块池第 %d 项: %w", i, err)
		}
		dataLen, err := d.u64()
		if err != nil {
			return nil, fmt.Errorf("块池第 %d 项: %w", i, err)
		}
		if dataLen > uint64(d.remaining()) {
			return nil, fmt.Errorf("块池第 %d 项数据越界: 需要 %d 字节，剩余 %d", i, dataLen, d.remaining())
		}
		data, err := d.raw(int(dataLen))
		if err != nil {
			return nil, err
		}
		pool = append(pool, Blob{Hash: rawHash, Comp: CompMethod(comp), RawLen: rawLen, Data: data})
	}

	cp := &ChunkPayload{Steps: steps, Pool: pool}
	if err := cp.SelfCheck(); err != nil {
		return nil, err
	}
	return cp, nil
}
