package godot

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"github.com/zeebo/blake3"

	"game-patcher/adapter"
)

const godotKeyOption = "godot.key"

// Adapter 是 Godot PCK 引擎适配器。
type Adapter struct{}

func New() adapter.Adapter { return &Adapter{} }

func init() { adapter.Register(New()) }

func (a *Adapter) Name() string { return "godot" }

func (a *Adapter) Description() string { return "Godot PCK 文件级增量（推荐）" }

func (a *Adapter) Match(ctx adapter.Context) adapter.MatchResult {
	if ctx.OldPath == "" || ctx.NewPath == "" {
		return adapter.MatchResult{}
	}
	oldOff, oldOK := detectPCKOffset(ctx.OldPath)
	newOff, newOK := detectPCKOffset(ctx.NewPath)
	if !oldOK || !newOK {
		return adapter.MatchResult{}
	}
	return adapter.MatchResult{
		Matched:    true,
		Confidence: 100,
		Reason:     fmt.Sprintf("GDPC old=%#x new=%#x", oldOff, newOff),
	}
}

func (a *Adapter) Build(ctx adapter.BuildContext) (adapter.BuildResult, error) {
	oldData, err := os.ReadFile(ctx.OldPath)
	if err != nil {
		return adapter.BuildResult{}, err
	}
	newData, err := os.ReadFile(ctx.NewPath)
	if err != nil {
		return adapter.BuildResult{}, err
	}
	var key []byte
	oldPCK, err := parsePCK(oldData, nil)
	if err != nil && errorsIsKeyRequired(err) {
		// 密钥仅在旧目录加密时需要；新版目录若加密会在下方显式拒绝。
		var keyErr error
		key, keyErr = resolveKey(ctx, oldData, ctx.OldPath)
		if keyErr != nil {
			return adapter.BuildResult{Accepted: false, Reason: keyErr.Error()}, nil
		}
		oldPCK, err = parsePCK(oldData, key)
	}
	if err != nil {
		if errorsIsKeyRequired(err) {
			return adapter.BuildResult{Accepted: false, Reason: "encrypted Godot PCK requires -godot-key"}, nil
		}
		return adapter.BuildResult{Accepted: false, Reason: "old PCK parse failed: " + err.Error()}, nil
	}

	newPCK, err := parsePCK(newData, key)
	if err != nil {
		if errorsIsKeyRequired(err) {
			return adapter.BuildResult{Accepted: false, Reason: "new encrypted Godot PCK directory rewrite is not supported"}, nil
		}
		return adapter.BuildResult{Accepted: false, Reason: "new PCK parse failed: " + err.Error()}, nil
	}
	if newPCK.Version != 2 {
		return adapter.BuildResult{Accepted: false, Reason: fmt.Sprintf("new PCK version %d rewrite not supported yet", newPCK.Version)}, nil
	}
	if newPCK.Flags&packFlagDirEncrypted != 0 {
		return adapter.BuildResult{Accepted: false, Reason: "new encrypted Godot PCK directory rewrite is not supported"}, nil
	}
	if oldPCK.Start != newPCK.Start {
		return adapter.BuildResult{Accepted: false, Reason: "old/new PCK offsets differ"}, nil
	}
	if (len(oldPCK.Trailer) != 0) != (len(newPCK.Trailer) != 0) {
		return adapter.BuildResult{Accepted: false, Reason: "old/new embedded PCK layouts differ"}, nil
	}
	prefixOps, ok := buildPrefixOps(oldData[:oldPCK.Start], newData[:newPCK.Start])
	if !ok {
		return adapter.BuildResult{Accepted: false, Reason: "PE prefix sizes differ"}, nil
	}
	pckOps, pckSize, err := buildV2PCKOps(oldPCK, oldData, newPCK, newData)
	if err != nil {
		return adapter.BuildResult{Accepted: false, Reason: err.Error()}, nil
	}
	ops := append(prefixOps, pckOps...)
	trailer := append([]byte(nil), newPCK.Trailer...)
	if len(trailer) != 0 {
		if len(trailer) < 12 || string(trailer[len(trailer)-4:]) != pckMagic {
			return adapter.BuildResult{Accepted: false, Reason: "unsupported embedded PCK trailer"}, nil
		}
		binary.LittleEndian.PutUint64(trailer[len(trailer)-12:], pckSize)
		ops = append(ops, adapter.Op{Kind: adapter.OpLiteral, Data: trailer, Length: uint64(len(trailer))})
	}
	newSize := uint64(newPCK.Start) + pckSize + uint64(len(trailer))
	h := blake3.New()
	written, err := hashOps(h, oldData, ops)
	if err != nil {
		return adapter.BuildResult{}, err
	}
	if written != newSize {
		return adapter.BuildResult{}, fmt.Errorf("adapter output size mismatch: ops=%d expected=%d", written, newSize)
	}
	var newHash [32]byte
	copy(newHash[:], h.Sum(nil))
	return adapter.BuildResult{
		Accepted: true,
		Delta: &adapter.FileDelta{
			OldHash: ctx.OldHash,
			NewHash: newHash,
			OldSize: ctx.OldSize,
			NewSize: newSize,
			Ops:     ops,
		},
	}, nil
}

func resolveKey(ctx adapter.BuildContext, oldData []byte, oldPath string) ([]byte, error) {
	if raw := optionValue(ctx.Options, godotKeyOption); raw != "" {
		return parseHexKey(raw)
	}
	key, err := findGodotKey(oldData, oldPath)
	if err != nil {
		return nil, fmt.Errorf("encrypted Godot PCK requires -godot-key or automatic key discovery: %w", err)
	}
	return key, nil
}

func optionValue(opts adapter.Options, key string) string {
	if opts.Values == nil {
		return ""
	}
	return opts.Values[key]
}

func errorsIsKeyRequired(err error) bool {
	return err != nil && (err == ErrKeyRequired || strings.Contains(err.Error(), ErrKeyRequired.Error()))
}

// detectPCKOffset 读取文件头和 PE section table，尽量只读少量数据就找到 GDPC。
func detectPCKOffset(path string) (int, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, false
	}
	var head [4096]byte
	n, _ := f.Read(head[:])
	if n >= 4 && string(head[:4]) == pckMagic {
		return 0, true
	}
	if info.Size() >= 12 {
		var footer [12]byte
		if _, err := f.ReadAt(footer[:], info.Size()-12); err == nil && string(footer[8:]) == pckMagic {
			size := binary.LittleEndian.Uint64(footer[:8])
			if size <= uint64(info.Size()-12) {
				start := info.Size() - 12 - int64(size)
				if _, err := f.Seek(start, 0); err == nil {
					var magic [4]byte
					if _, err := f.Read(magic[:]); err == nil && string(magic[:]) == pckMagic {
						return int(start), true
					}
				}
			}
		}
	}
	limit := int64(16 << 20)
	if info.Size() < limit {
		limit = info.Size()
	}
	if _, err := f.Seek(0, 0); err != nil {
		return 0, false
	}
	buf := make([]byte, 1<<20)
	var fileOff int64
	for fileOff < limit {
		want := int64(len(buf))
		if fileOff+want > limit {
			want = limit - fileOff
		}
		n, err := f.ReadAt(buf[:want], fileOff)
		if n > 0 {
			if idx := bytes.Index(buf[:n], []byte(pckMagic)); idx >= 0 {
				return int(fileOff) + idx, true
			}
		}
		if err != nil {
			break
		}
		fileOff += int64(n)
	}
	return 0, false
}

func detectPCKOffsetBytes(data []byte) (int, bool) {
	start, err := findPCKStart(data)
	return start, err == nil
}

func buildPrefixOps(oldPrefix, newPrefix []byte) ([]adapter.Op, bool) {
	if len(oldPrefix) != len(newPrefix) {
		return nil, false
	}
	var ops []adapter.Op
	i := 0
	for i < len(newPrefix) {
		start := i
		for i < len(newPrefix) && oldPrefix[i] == newPrefix[i] {
			i++
		}
		if i > start {
			ops = append(ops, adapter.Op{Kind: adapter.OpCopy, OldOffset: uint64(start), Length: uint64(i - start)})
		}
		start = i
		for i < len(newPrefix) && oldPrefix[i] != newPrefix[i] {
			i++
		}
		if i > start {
			ops = append(ops, adapter.Op{Kind: adapter.OpLiteral, Data: append([]byte(nil), newPrefix[start:i]...), Length: uint64(i - start)})
		}
	}
	return ops, true
}

type plannedFile struct {
	path        string
	size        uint64
	md5         [16]byte
	flags       uint32
	copyOld     bool
	oldOffset   uint64
	newData     []byte
	blobLen     uint64
	stateOffset uint64
}

func buildV2PCKOps(oldPCK *pckFile, oldData []byte, newPCK *pckFile, newData []byte) ([]adapter.Op, uint64, error) {
	oldByPath := make(map[string]pckEntry, len(oldPCK.Entries))
	for _, e := range oldPCK.Entries {
		oldByPath[e.Path] = e
	}
	planned := make([]plannedFile, 0, len(newPCK.Entries))
	for _, ne := range newPCK.Entries {
		pf := plannedFile{path: ne.Path, size: ne.Size, md5: ne.MD5, flags: ne.Flags}
		if oe, ok := oldByPath[ne.Path]; ok && oe.Size == ne.Size && oe.MD5 == ne.MD5 {
			oldOff, oldLen, err := oldBlobRange(oldPCK, oldData, oe)
			if err != nil {
				return nil, 0, err
			}
			pf.copyOld = true
			pf.oldOffset = oldOff
			pf.blobLen = oldLen
			if oe.Flags&packFileEncrypted != 0 {
				pf.flags |= packFileEncrypted
			} else {
				pf.flags &^= packFileEncrypted
			}
		} else {
			blob, err := newBlob(ne, newData, newPCK)
			if err != nil {
				return nil, 0, err
			}
			pf.newData = blob
			pf.blobLen = uint64(len(blob))
		}
		planned = append(planned, pf)
	}

	dirSize := uint64(4)
	for _, pf := range planned {
		dirSize += uint64(4 + paddedPathLen(pf.path) + 36)
	}
	fileBase := align16U64(0x60 + dirSize)
	dataOffset := fileBase
	for i := range planned {
		planned[i].flags &^= packFileRemoval | packFileDelta
		planned[i].stateOffset = dataOffset - fileBase
		dataOffset += planned[i].blobLen
	}
	pckSize := align16U64(dataOffset)

	var head bytes.Buffer
	if newPCK.Start+0x60 > len(newData) {
		return nil, 0, fmt.Errorf("new PCK header truncated")
	}
	header := append([]byte(nil), newData[newPCK.Start:newPCK.Start+0x60]...)
	outFileBase := fileBase
	if newPCK.Flags&packFlagRelFileBase == 0 {
		outFileBase = uint64(newPCK.Start) + fileBase
	}
	binary.LittleEndian.PutUint64(header[0x18:], outFileBase)
	head.Write(header)
	_ = binary.Write(&head, binary.LittleEndian, uint32(len(planned)))
	for _, pf := range planned {
		raw := []byte(pf.path)
		for len(raw)%4 != 0 {
			raw = append(raw, 0)
		}
		_ = binary.Write(&head, binary.LittleEndian, uint32(len(raw)))
		head.Write(raw)
		_ = binary.Write(&head, binary.LittleEndian, pf.stateOffset)
		_ = binary.Write(&head, binary.LittleEndian, pf.size)
		head.Write(pf.md5[:])
		_ = binary.Write(&head, binary.LittleEndian, pf.flags)
	}
	for uint64(head.Len()) < fileBase {
		head.WriteByte(0)
	}

	ops := []adapter.Op{{Kind: adapter.OpLiteral, Data: head.Bytes(), Length: uint64(head.Len())}}
	for _, pf := range planned {
		if pf.copyOld {
			ops = append(ops, adapter.Op{Kind: adapter.OpCopy, OldOffset: pf.oldOffset, Length: pf.blobLen})
		} else {
			ops = append(ops, adapter.Op{Kind: adapter.OpLiteral, Data: pf.newData, Length: pf.blobLen})
		}
	}
	if pad := pckSize - dataOffset; pad > 0 {
		ops = append(ops, adapter.Op{Kind: adapter.OpLiteral, Data: make([]byte, pad), Length: pad})
	}
	return ops, pckSize, nil
}

func oldBlobRange(pck *pckFile, data []byte, e pckEntry) (uint64, uint64, error) {
	base := uint64(pck.Start) + pck.FileBase
	abs := base + e.Offset
	if e.StoredSize == 0 {
		if e.Flags&packFileEncrypted != 0 {
			return 0, 0, fmt.Errorf("old encrypted blob has no stored size: %s", e.Path)
		}
		e.StoredSize = e.Size
	}
	if abs+e.StoredSize > uint64(len(data)) || abs+e.StoredSize < abs {
		return 0, 0, fmt.Errorf("old blob out of range: %s", e.Path)
	}
	if e.Flags&packFileEncrypted != 0 {
		b, err := readEncryptedBlock(data, abs, uint64(pck.End))
		if err != nil {
			return 0, 0, fmt.Errorf("old encrypted blob: %s: %w", e.Path, err)
		}
		if b.Size != e.Size || b.MD5 != e.MD5 {
			return 0, 0, fmt.Errorf("old encrypted blob metadata mismatch: %s", e.Path)
		}
		return abs, b.StoredSize, nil
	}
	return abs, e.Size, nil
}

func newBlob(e pckEntry, data []byte, pck *pckFile) ([]byte, error) {
	base := uint64(pck.Start) + pck.FileBase
	abs := base + e.Offset
	if e.Flags&packFileEncrypted != 0 {
		b, err := readEncryptedBlock(data, abs, uint64(pck.End))
		if err != nil {
			return nil, fmt.Errorf("new encrypted blob %s: %w", e.Path, err)
		}
		if b.Size != e.Size || b.MD5 != e.MD5 {
			return nil, fmt.Errorf("new encrypted blob metadata mismatch: %s", e.Path)
		}
		return append([]byte(nil), data[abs:abs+b.StoredSize]...), nil
	}
	if abs+e.Size > uint64(len(data)) || abs+e.Size < abs {
		return nil, fmt.Errorf("new blob out of range: %s", e.Path)
	}
	return append([]byte(nil), data[abs:abs+e.Size]...), nil
}

func paddedPathLen(path string) int {
	n := len([]byte(path))
	if rem := n % 4; rem != 0 {
		n += 4 - rem
	}
	return n
}

func align16U64(n uint64) uint64 {
	if rem := n % 16; rem != 0 {
		n += 16 - rem
	}
	return n
}

func hashOps(h *blake3.Hasher, oldData []byte, ops []adapter.Op) (uint64, error) {
	var written uint64
	for _, op := range ops {
		switch op.Kind {
		case adapter.OpCopy:
			if op.OldOffset+op.Length < op.OldOffset || op.OldOffset+op.Length > uint64(len(oldData)) {
				return 0, fmt.Errorf("copy out of range")
			}
			_, _ = h.Write(oldData[op.OldOffset : op.OldOffset+op.Length])
			written += op.Length
		case adapter.OpLiteral:
			if uint64(len(op.Data)) != op.Length {
				return 0, fmt.Errorf("literal length mismatch")
			}
			_, _ = h.Write(op.Data)
			written += op.Length
		default:
			return 0, fmt.Errorf("unsupported op kind %d", op.Kind)
		}
	}
	return written, nil
}
