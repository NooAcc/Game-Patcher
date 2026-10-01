package patcher

import (
	"encoding/binary"
	"fmt"
	"io"
)

// GPBIN4 是链式补丁制品格式。信封布局：
//
//	"GPBIN4"       6B   魔数
//	formatVersion  u8   (= 5)
//	payloadKind    u8   差异后端类型
//	patchVersion   u32  自动递增的补丁版本号
//	payload        ...  由 payloadKind 决定
//
// 可执行文件尾部布局：
//
//	[base exe][release blob][blobLen u64]["GPBIN4END!"][[restorer][restorerLen u64]["GPBIN4RST!"]]
const (
	patchMagic    = "GPBIN4"
	patchEndMagic = "GPBIN4END!"
	restorerMagic = "GPBIN4RST!"

	// formatVersion 是补丁制品的线格式版本。5 起版本链不再携带版本名称（Label）。
	formatVersion = 5

	// legacyFormatVersion 是仍携带版本名称的旧线格式，仅用于给出明确的迁移提示。
	legacyFormatVersion = 4
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
	// OpCopy 从本文件的旧版本复制一段数据。
	OpCopy OpKind = 1
	// OpLiteral 写入字面量数据。
	OpLiteral OpKind = 2
	// OpCopyFrom 从同一版本树中的另一个文件复制一段数据（chunk 后端使用）。
	OpCopyFrom OpKind = 3
	// OpPoolRef 引用共享字面量池中的一段数据（chunk 后端使用）。
	OpPoolRef OpKind = 4
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
	OldOffset uint64 // OpCopy / OpCopyFrom: 源偏移
	Length    uint64 // OpCopy / OpCopyFrom: 复制长度；OpLiteral / OpPoolRef: 解压后的长度
	SrcPath   string // OpCopyFrom: 源文件相对路径
	PoolIndex uint32 // OpPoolRef: 共享字面量池下标
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

// EncodeRelease 将一份补丁制品写入 w。
func EncodeRelease(w io.Writer, rel *Release) error {
	if rel == nil || rel.Payload == nil {
		return fmt.Errorf("补丁内容为空")
	}
	e := &encWriter{w: w}
	e.raw([]byte(patchMagic))
	e.u8(formatVersion)
	e.u8(byte(rel.Payload.Kind()))
	e.u32(rel.PatchVersion)
	if e.err != nil {
		return e.err
	}
	return rel.Payload.Encode(w)
}

// DecodeRelease 解析补丁制品字节流。所有长度字段都经过边界校验，
// 损坏数据只会返回错误，不会 panic。
func DecodeRelease(data []byte) (*Release, error) {
	d := &decoder{data: data}
	magic, err := d.raw(len(patchMagic))
	if err != nil {
		return nil, fmt.Errorf("补丁数据过短: %w", err)
	}
	if string(magic) != patchMagic {
		return nil, fmt.Errorf("补丁魔数不匹配")
	}
	ver, err := d.u8()
	if err != nil {
		return nil, err
	}
	if ver == legacyFormatVersion {
		return nil, fmt.Errorf("该补丁使用已淘汰的旧格式（v%d），请用当前版本的 CLI 重新生成补丁", ver)
	}
	if ver != formatVersion {
		return nil, fmt.Errorf("不支持的补丁格式版本: %d", ver)
	}
	kind, err := d.u8()
	if err != nil {
		return nil, err
	}
	patchVersion, err := d.u32()
	if err != nil {
		return nil, fmt.Errorf("补丁版本号: %w", err)
	}

	var payload *ChunkPayload
	switch PayloadKind(kind) {
	case PayloadChunk:
		payload, err = decodeChunkPayload(d)
	default:
		return nil, fmt.Errorf("不支持的差异后端类型: %d", kind)
	}
	if err != nil {
		return nil, err
	}
	if d.remaining() != 0 {
		return nil, fmt.Errorf("补丁数据尾部存在 %d 字节多余内容", d.remaining())
	}
	return &Release{PatchVersion: patchVersion, Payload: payload}, nil
}

func encodeEntry(e *encWriter, en *Entry) {
	if en.Action < ActionAdd || en.Action > ActionDelete {
		e.fail(fmt.Errorf("条目 %s 行为非法: %d", en.Path, en.Action))
		return
	}
	if err := validateRelPath(en.Path); err != nil {
		e.fail(fmt.Errorf("条目路径非法: %w", err))
		return
	}
	e.str(en.Path)
	e.u8(byte(en.Action))
	e.raw(en.OldHash[:])
	e.raw(en.NewHash[:])
	e.u64(en.OldSize)
	e.u64(en.NewSize)
	e.u32(uint32(len(en.Ops)))
	for j := range en.Ops {
		encodeOp(e, &en.Ops[j])
	}
}

func encodeOp(e *encWriter, op *DeltaOp) {
	switch op.Kind {
	case OpCopy:
		e.u8(byte(OpCopy))
		e.u64(op.OldOffset)
		e.u64(op.Length)
	case OpLiteral:
		if op.Comp != CompRaw && op.Comp != CompFlate {
			e.fail(fmt.Errorf("非法压缩方式: %d", op.Comp))
			return
		}
		e.u8(byte(OpLiteral))
		e.u8(byte(op.Comp))
		e.u64(op.Length)
		e.u64(uint64(len(op.Data)))
		e.raw(op.Data)
	case OpCopyFrom:
		if err := validateRelPath(op.SrcPath); err != nil {
			e.fail(fmt.Errorf("跨文件复制的源路径非法: %w", err))
			return
		}
		e.u8(byte(OpCopyFrom))
		e.str(op.SrcPath)
		e.u64(op.OldOffset)
		e.u64(op.Length)
	case OpPoolRef:
		e.u8(byte(OpPoolRef))
		e.u32(op.PoolIndex)
		e.u64(op.Length)
	default:
		e.fail(fmt.Errorf("非法操作类型: %d", op.Kind))
	}
}

func decodeEntry(d *decoder) (Entry, error) {
	var en Entry
	path, err := d.str()
	if err != nil {
		return en, err
	}
	if err := validateRelPath(path); err != nil {
		return en, fmt.Errorf("路径非法: %w", err)
	}
	en.Path = path
	action, err := d.u8()
	if err != nil {
		return en, err
	}
	if Action(action) < ActionAdd || Action(action) > ActionDelete {
		return en, fmt.Errorf("行为非法: %d", action)
	}
	en.Action = Action(action)
	if en.OldHash, err = d.hash(); err != nil {
		return en, err
	}
	if en.NewHash, err = d.hash(); err != nil {
		return en, err
	}
	if en.OldSize, err = d.u64(); err != nil {
		return en, err
	}
	if en.NewSize, err = d.u64(); err != nil {
		return en, err
	}
	opCount, err := d.u32()
	if err != nil {
		return en, err
	}
	// 每个操作至少 1 字节。
	if uint64(opCount) > uint64(d.remaining())+1 {
		return en, fmt.Errorf("操作数量异常: %d", opCount)
	}
	en.Ops = make([]DeltaOp, 0, opCount)
	for j := uint32(0); j < opCount; j++ {
		op, err := decodeOp(d)
		if err != nil {
			return en, fmt.Errorf("操作 %d: %w", j, err)
		}
		en.Ops = append(en.Ops, op)
	}
	return en, nil
}

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
	case OpCopyFrom:
		src, err := d.str()
		if err != nil {
			return DeltaOp{}, err
		}
		if err := validateRelPath(src); err != nil {
			return DeltaOp{}, fmt.Errorf("跨文件复制的源路径非法: %w", err)
		}
		off, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		length, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		if length == 0 {
			return DeltaOp{}, fmt.Errorf("跨文件复制长度为 0")
		}
		return DeltaOp{Kind: OpCopyFrom, SrcPath: src, OldOffset: off, Length: length}, nil
	case OpPoolRef:
		idx, err := d.u32()
		if err != nil {
			return DeltaOp{}, err
		}
		length, err := d.u64()
		if err != nil {
			return DeltaOp{}, err
		}
		if length == 0 {
			return DeltaOp{}, fmt.Errorf("块池引用长度为 0")
		}
		return DeltaOp{Kind: OpPoolRef, PoolIndex: idx, Length: length}, nil
	default:
		return DeltaOp{}, fmt.Errorf("非法操作类型: %d", kind)
	}
}

// ── 写入辅助 ────────────────────────────────────────────────

type encWriter struct {
	w   io.Writer
	err error
}

func (e *encWriter) fail(err error) {
	if e.err == nil {
		e.err = err
	}
}

func (e *encWriter) raw(b []byte) {
	if e.err != nil {
		return
	}
	_, e.err = e.w.Write(b)
}

func (e *encWriter) u8(v byte) { e.raw([]byte{v}) }

func (e *encWriter) u32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	e.raw(b[:])
}

func (e *encWriter) u64(v uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	e.raw(b[:])
}

func (e *encWriter) str(s string) {
	if len(s) > int(^uint32(0)) {
		e.fail(fmt.Errorf("字符串过长: %d", len(s)))
		return
	}
	e.u32(uint32(len(s)))
	e.raw([]byte(s))
}

// ── 读取辅助 ────────────────────────────────────────────────

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

func (d *decoder) str() (string, error) {
	n, err := d.u32()
	if err != nil {
		return "", err
	}
	if uint64(n) > uint64(d.remaining()) {
		return "", fmt.Errorf("字符串长度越界: %d（剩余 %d）", n, d.remaining())
	}
	b, err := d.raw(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
