package patcher

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// 补丁数据格式版本 3（二进制增量修补，仅支持目录树补丁）。
//
//	"GPBIN3"                6B  补丁块魔数
//	version                 uint8
//	entryCount              uint32
//	Entry[entryCount]
//
// 可执行文件尾部布局：
//
//	[base exe][patch blob][patchLen uint64][GPBIN3END!][[restorer][restorerLen uint64][GPBIN3RST!]]
const (
	patchMagic    = "GPBIN3"
	patchEndMagic = "GPBIN3END!"
	restorerMagic = "GPBIN3RST!"
	patchVersion  = 3
)

// Action 描述一个条目要执行的变更。
type Action uint8

const (
	ActionAdd    Action = 1
	ActionUpdate Action = 2
	ActionDelete Action = 3
)

func (a Action) String() string {
	switch a {
	case ActionAdd:
		return "add"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(a))
	}
}

// OpKind 描述差异操作类型。
type OpKind uint8

const (
	// OpCopy 从旧文件复制一段数据。
	OpCopy OpKind = 1
	// OpLiteral 写入字面量数据。
	OpLiteral OpKind = 2
)

// CompMethod 描述字面量压缩方式。
type CompMethod uint8

const (
	CompRaw   CompMethod = 0
	CompFlate CompMethod = 1
)

// DeltaOp 是构成新文件的最小操作单元。
type DeltaOp struct {
	Kind      OpKind
	OldOffset uint64 // OpCopy: 旧文件偏移
	Length    uint64 // OpCopy: 复制长度；OpLiteral: 解压后的长度
	Comp      CompMethod
	Data      []byte // OpLiteral 的载荷（可能已压缩）
}

// Entry 描述一个文件的升级操作。
type Entry struct {
	Path    string
	Action  Action
	OldHash [32]byte
	NewHash [32]byte
	OldSize uint64
	NewSize uint64
	Ops     []DeltaOp
}

// Patch 是一个完整的补丁数据集。
type Patch struct {
	Entries []Entry
}

// Encode 将补丁序列化为字节流。
func (p *Patch) Encode() ([]byte, error) {
	if uint64(len(p.Entries)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("条目数量过多")
	}

	var buf bytes.Buffer
	buf.WriteString(patchMagic)
	buf.WriteByte(patchVersion)
	putU32(&buf, uint32(len(p.Entries)))

	for i := range p.Entries {
		e := &p.Entries[i]
		if err := validateRelPath(e.Path); err != nil {
			return nil, fmt.Errorf("条目 %d 路径非法: %w", i, err)
		}
		if len(e.Path) > int(^uint32(0)) {
			return nil, fmt.Errorf("条目 %d 路径过长", i)
		}
		if e.Action < ActionAdd || e.Action > ActionDelete {
			return nil, fmt.Errorf("条目 %d 行为非法: %d", i, e.Action)
		}
		putU32(&buf, uint32(len(e.Path)))
		buf.WriteString(e.Path)
		buf.WriteByte(byte(e.Action))
		buf.Write(e.OldHash[:])
		buf.Write(e.NewHash[:])
		putU64(&buf, e.OldSize)
		putU64(&buf, e.NewSize)

		if uint64(len(e.Ops)) > uint64(^uint32(0)) {
			return nil, fmt.Errorf("条目 %s 操作过多", e.Path)
		}
		putU32(&buf, uint32(len(e.Ops)))
		for j := range e.Ops {
			if err := encodeOp(&buf, &e.Ops[j]); err != nil {
				return nil, fmt.Errorf("条目 %s 操作 %d: %w", e.Path, j, err)
			}
		}
	}
	return buf.Bytes(), nil
}

func encodeOp(buf *bytes.Buffer, op *DeltaOp) error {
	switch op.Kind {
	case OpCopy:
		buf.WriteByte(byte(OpCopy))
		putU64(buf, op.OldOffset)
		putU64(buf, op.Length)
		return nil
	case OpLiteral:
		if op.Comp != CompRaw && op.Comp != CompFlate {
			return fmt.Errorf("非法压缩方式: %d", op.Comp)
		}
		buf.WriteByte(byte(OpLiteral))
		buf.WriteByte(byte(op.Comp))
		putU64(buf, op.Length)
		putU64(buf, uint64(len(op.Data)))
		buf.Write(op.Data)
		return nil
	default:
		return fmt.Errorf("非法操作类型: %d", op.Kind)
	}
}

// DecodePatch 解析补丁字节流。所有长度字段都经过边界校验，损坏数据只会返回错误。
func DecodePatch(data []byte) (*Patch, error) {
	d := &decoder{data: data}
	magic, err := d.raw(len(patchMagic))
	if err != nil {
		return nil, fmt.Errorf("补丁数据过短: %w", err)
	}
	if string(magic) != patchMagic {
		return nil, fmt.Errorf("补丁魔数不匹配")
	}
	version, err := d.u8()
	if err != nil {
		return nil, err
	}
	if version != patchVersion {
		return nil, fmt.Errorf("不支持的补丁版本: %d", version)
	}
	count, err := d.u32()
	if err != nil {
		return nil, err
	}
	// 每个条目至少包含 pathLen(4)+action(1)+oldHash(32)+newHash(32)+oldSize(8)+newSize(8)+opCount(4)。
	if uint64(count) > uint64(d.remaining()/minEntryEncodedSize)+1 {
		return nil, fmt.Errorf("条目数量异常: %d", count)
	}

	p := &Patch{Entries: make([]Entry, 0, count)}
	for i := uint32(0); i < count; i++ {
		pathLen, err := d.u32()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		pathBytes, err := d.raw(int(pathLen))
		if err != nil {
			return nil, fmt.Errorf("条目 %d 路径越界: %w", i, err)
		}
		path := string(pathBytes)
		if err := validateRelPath(path); err != nil {
			return nil, fmt.Errorf("条目 %d 路径非法: %w", i, err)
		}
		action, err := d.u8()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		if Action(action) < ActionAdd || Action(action) > ActionDelete {
			return nil, fmt.Errorf("条目 %d 行为非法: %d", i, action)
		}
		oldHash, err := d.hash()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		newHash, err := d.hash()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		oldSize, err := d.u64()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		newSize, err := d.u64()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		opCount, err := d.u32()
		if err != nil {
			return nil, fmt.Errorf("条目 %d: %w", i, err)
		}
		// 每个操作至少 1 字节。
		if uint64(opCount) > uint64(d.remaining())+1 {
			return nil, fmt.Errorf("条目 %d 操作数量异常: %d", i, opCount)
		}
		e := Entry{
			Path:    path,
			Action:  Action(action),
			OldHash: oldHash,
			NewHash: newHash,
			OldSize: oldSize,
			NewSize: newSize,
			Ops:     make([]DeltaOp, 0, opCount),
		}
		for j := uint32(0); j < opCount; j++ {
			op, err := decodeOp(d)
			if err != nil {
				return nil, fmt.Errorf("条目 %s 操作 %d: %w", path, j, err)
			}
			e.Ops = append(e.Ops, op)
		}
		p.Entries = append(p.Entries, e)
	}
	if d.remaining() != 0 {
		return nil, fmt.Errorf("补丁数据尾部存在 %d 字节多余内容", d.remaining())
	}
	return p, nil
}

const minEntryEncodedSize = 4 + 1 + 32 + 32 + 8 + 8 + 4

func decodeOp(d *decoder) (DeltaOp, error) {
	kind, err := d.u8()
	if err != nil {
		return DeltaOp{}, err
	}
	switch OpKind(kind) {
	case OpCopy:
		off, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		length, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		if length == 0 {
			return DeltaOp{}, fmt.Errorf("COPY 长度为 0")
		}
		return DeltaOp{Kind: OpCopy, OldOffset: off, Length: length}, nil
	case OpLiteral:
		comp, err := d.u8()
		if err != nil {
			return DeltaOp{}, err
		}
		if CompMethod(comp) != CompRaw && CompMethod(comp) != CompFlate {
			return DeltaOp{}, fmt.Errorf("非法压缩方式: %d", comp)
		}
		rawLen, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		compLen, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		if compLen > uint64(d.remaining()) {
			return DeltaOp{}, fmt.Errorf("字面量数据越界: 需要 %d 字节，剩余 %d", compLen, d.remaining())
		}
		payload, err := d.raw(int(compLen))
		if err != nil {
			return DeltaOp{}, err
		}
		return DeltaOp{Kind: OpLiteral, Length: rawLen, Comp: CompMethod(comp), Data: payload}, nil
	default:
		return DeltaOp{}, fmt.Errorf("非法操作类型: %d", kind)
	}
}

type decoder struct {
	data []byte
	off  int
}

func (d *decoder) remaining() int { return len(d.data) - d.off }

func (d *decoder) raw(n int) ([]byte, error) {
	if n < 0 || n > d.remaining() {
		return nil, fmt.Errorf("读取 %d 字节越界（剩余 %d）", n, d.remaining())
	}
	b := d.data[d.off : d.off+n]
	d.off += n
	return b, nil
}

func (d *decoder) u8() (byte, error) {
	b, err := d.raw(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (d *decoder) u32() (uint32, error) {
	b, err := d.raw(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

func (d *decoder) u64() (uint64, error) {
	b, err := d.raw(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

func (d *decoder) hash() ([32]byte, error) {
	var h [32]byte
	b, err := d.raw(32)
	if err != nil {
		return h, err
	}
	copy(h[:], b)
	return h, nil
}

func putU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func putU64(buf *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	buf.Write(b[:])
}
