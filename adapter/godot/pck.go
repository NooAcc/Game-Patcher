package godot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	pckMagic = "GDPC"

	packFlagDirEncrypted = 1 << 0
	packFlagRelFileBase  = 1 << 1
	packFlagSparseBundle = 1 << 2
	packFlagSupported    = packFlagDirEncrypted | packFlagRelFileBase
	packFlagKnownMask    = packFlagDirEncrypted | packFlagRelFileBase | packFlagSparseBundle

	packFileEncrypted = 1 << 0
	packFileRemoval   = 1 << 1
	packFileDelta     = 1 << 2
)

const (
	encryptedBlockHeaderSize = 40
	maxDirectoryBytes        = 64 << 20
	minEntryBytes            = 44
)

var ErrKeyRequired = errors.New("godot: pck directory key required")

type pckFile struct {
	Start       int
	End         int
	FooterStart int
	Trailer     []byte
	Version     uint32
	Major       uint32
	Minor       uint32
	Patch       uint32
	Flags       uint32
	FileBase    uint64
	DirOffset   uint64
	DirStart    int
	DirEnd      int
	Entries     []pckEntry
}

type pckEntry struct {
	Path       string
	Offset     uint64
	Size       uint64
	StoredSize uint64
	MD5        [16]byte
	Flags      uint32
}

type encryptedBlock struct {
	Offset     uint64
	Size       uint64
	StoredSize uint64
	MD5        [16]byte
	IV         [16]byte
}

func parseHexKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if len(s) == 64 {
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("invalid AES key hex: %w", err)
		}
		return b, nil
	}
	if len(s) == 32 {
		return []byte(s), nil
	}
	return nil, fmt.Errorf("AES key must be 32 raw bytes or 64 hex characters")
}

func findPCKStart(data []byte) (int, error) {
	if len(data) >= 0x28 && string(data[:4]) == pckMagic && validPCKHeader(data, 0) {
		return 0, nil
	}
	if len(data) >= 12 && string(data[len(data)-4:]) == pckMagic {
		size := binary.LittleEndian.Uint64(data[len(data)-12 : len(data)-4])
		if size >= 0x64 && size <= uint64(len(data)-12) {
			start := len(data) - 12 - int(size)
			limit := len(data) - 12
			if validPCKHeader(data, start) && trailingEmbeddedLayout(data, start, limit, size) {
				return start, nil
			}
		}
	}
	// 兼容 Godot 曾使用的 PE section 布局；只在 section 起点附近查找。
	if len(data) >= 0x40 && data[0] == 'M' && data[1] == 'Z' {
		e := int(binary.LittleEndian.Uint32(data[0x3c:]))
		if e >= 0 && e+24 <= len(data) {
			nsec := int(binary.LittleEndian.Uint16(data[e+6:]))
			optSize := int(binary.LittleEndian.Uint16(data[e+20:]))
			secOff := e + 24 + optSize
			for i := 0; i < nsec; i++ {
				off := secOff + i*40
				if off+40 > len(data) {
					break
				}
				name := strings.TrimRight(string(data[off:off+8]), "\x00")
				if !strings.EqualFold(name, "pck") {
					continue
				}
				raw := int(binary.LittleEndian.Uint32(data[off+20:]))
				if raw >= 0 && raw+4 <= len(data) && string(data[raw:raw+4]) == pckMagic && validPCKHeader(data, raw) {
					return raw, nil
				}
			}
		}
	}
	magic := []byte(pckMagic)
	for off := 0; off+0x28 <= len(data); {
		rel := bytes.Index(data[off:], magic)
		if rel < 0 {
			break
		}
		start := off + rel
		if validPCKHeader(data, start) {
			return start, nil
		}
		off = start + 1
	}
	return 0, fmt.Errorf("godot: GDPC header not found")
}

func trailingEmbeddedLayout(data []byte, start, limit int, size uint64) bool {
	if size < 0x64 || start < 0 || uint64(start)+size > uint64(limit) {
		return false
	}
	return true
}

func validPCKHeader(data []byte, start int) bool {
	if start < 0 || start+0x28 > len(data) {
		return false
	}
	version := binary.LittleEndian.Uint32(data[start+4:])
	if version < 2 || version > 4 {
		return false
	}
	major := binary.LittleEndian.Uint32(data[start+8:])
	minor := binary.LittleEndian.Uint32(data[start+12:])
	patch := binary.LittleEndian.Uint32(data[start+16:])
	if major < 3 || major > 16 || minor > 1000 || patch > 1000 {
		return false
	}
	flags := binary.LittleEndian.Uint32(data[start+0x14:])
	if flags&^uint32(packFlagKnownMask) != 0 {
		return false
	}
	if flags&packFlagSparseBundle != 0 {
		return false
	}
	fileBase := binary.LittleEndian.Uint64(data[start+0x18:])
	if version >= 3 || flags&packFlagRelFileBase != 0 {
		if fileBase > uint64(len(data)-start) {
			return false
		}
	} else if fileBase > uint64(len(data)) {
		return false
	}
	if version == 3 || version == 4 {
		dirOff := binary.LittleEndian.Uint64(data[start+0x20:])
		if dirOff < 0x28 || dirOff > uint64(len(data)-start) {
			return false
		}
	} else if version == 2 {
		if start+0x60 > len(data) {
			return false
		}
	}
	return true
}

func parsePCK(data []byte, key []byte) (*pckFile, error) {
	start, err := findPCKStart(data)
	if err != nil {
		return nil, err
	}
	h := &pckFile{
		Start:       start,
		End:         len(data),
		FooterStart: len(data),
		Version:     binary.LittleEndian.Uint32(data[start+4:]),
		Major:       binary.LittleEndian.Uint32(data[start+8:]),
		Minor:       binary.LittleEndian.Uint32(data[start+12:]),
		Patch:       binary.LittleEndian.Uint32(data[start+16:]),
		Flags:       binary.LittleEndian.Uint32(data[start+0x14:]),
	}
	if start > 0 && len(data) >= 12 && string(data[len(data)-4:]) == pckMagic {
		size := binary.LittleEndian.Uint64(data[len(data)-12 : len(data)-4])
		if size < 0x64 || uint64(start)+size > uint64(len(data)-12) {
			return nil, fmt.Errorf("godot: malformed embedded PCK footer")
		}
		h.End = start + int(size)
		h.FooterStart = h.End
		h.Trailer = append([]byte(nil), data[h.End:]...)
	}
	if h.Flags&packFlagSparseBundle != 0 {
		return nil, fmt.Errorf("godot: sparse bundle PCK is not supported")
	}
	if h.Flags&^uint32(packFlagSupported) != 0 {
		return nil, fmt.Errorf("godot: unsupported PCK flags %#x", h.Flags)
	}
	rawFileBase := binary.LittleEndian.Uint64(data[start+0x18:])
	if h.Version >= 3 || h.Flags&packFlagRelFileBase != 0 {
		h.FileBase = rawFileBase
	} else {
		if rawFileBase < uint64(start) {
			return nil, fmt.Errorf("godot: absolute file_base points before PCK")
		}
		h.FileBase = rawFileBase - uint64(start)
	}
	if h.Version == 3 || h.Version == 4 {
		h.DirOffset = binary.LittleEndian.Uint64(data[start+0x20:])
		h.DirStart = start + int(h.DirOffset)
	} else {
		h.DirOffset = 0x60
		h.DirStart = start + 0x60
	}
	if h.DirStart < start+0x28 || h.DirStart+4 > h.End {
		return nil, fmt.Errorf("godot: directory offset out of range")
	}
	fileCount := binary.LittleEndian.Uint32(data[h.DirStart:])
	if fileCount > 1_000_000 {
		return nil, fmt.Errorf("godot: unreasonable file count %d", fileCount)
	}
	if uint64(fileCount)*minEntryBytes+4 > maxDirectoryBytes {
		return nil, fmt.Errorf("godot: directory metadata exceeds %d MiB limit", maxDirectoryBytes>>20)
	}
	dirDataStart := h.DirStart + 4
	var dirData []byte
	if h.Flags&packFlagDirEncrypted != 0 {
		if len(key) == 0 {
			return nil, ErrKeyRequired
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("godot: AES key must be 32 bytes, got %d", len(key))
		}
		block, err := readEncryptedBlock(data, uint64(dirDataStart), uint64(h.End))
		if err != nil {
			return nil, fmt.Errorf("godot: encrypted directory: %w", err)
		}
		if block.Size > maxDirectoryBytes {
			return nil, fmt.Errorf("godot: encrypted directory exceeds %d MiB limit", maxDirectoryBytes>>20)
		}
		dirData = make([]byte, block.Size)
		if err := decryptEncryptedBlock(data, block, key, func(p []byte) error {
			copy(dirData, p)
			return nil
		}); err != nil {
			return nil, fmt.Errorf("godot: encrypted directory: %w", err)
		}
		h.DirEnd = dirDataStart + int(block.StoredSize)
	} else {
		dirEnd := dirDataStart + maxDirectoryBytes
		if dirEnd > h.End {
			dirEnd = h.End
		}
		dirData = data[dirDataStart:dirEnd]
	}
	entries, consumed, err := parseEntries(dirData, int(fileCount))
	if err != nil {
		if h.Flags&packFlagDirEncrypted == 0 {
			return nil, fmt.Errorf("godot: plain directory truncated or exceeds %d MiB limit", maxDirectoryBytes>>20)
		}
		return nil, err
	}
	if h.Flags&packFlagDirEncrypted != 0 {
		if consumed != len(dirData) {
			return nil, fmt.Errorf("godot: encrypted directory has %d trailing bytes", len(dirData)-consumed)
		}
	} else {
		h.DirEnd = dirDataStart + consumed
	}
	if h.DirEnd < dirDataStart || h.DirEnd > h.End {
		return nil, fmt.Errorf("godot: directory range out of bounds")
	}
	h.Entries = entries
	if err := validateEntryRanges(h, data, key); err != nil {
		return nil, err
	}
	return h, nil
}

func parseEntries(data []byte, count int) ([]pckEntry, int, error) {
	entries := make([]pckEntry, 0, count)
	seen := make(map[string]struct{}, count)
	off := 0
	for i := 0; i < count; i++ {
		if off+4 > len(data) {
			return nil, 0, fmt.Errorf("godot: entry %d path length out of range", i)
		}
		rawLen := binary.LittleEndian.Uint32(data[off:])
		off += 4
		if rawLen == 0 || rawLen > 4096 || off+int(rawLen)+36 > len(data) {
			return nil, 0, fmt.Errorf("godot: entry %d invalid path length %d", i, rawLen)
		}
		rawPath := data[off : off+int(rawLen)]
		off += int(rawLen)
		pathBytes := bytes.TrimRight(rawPath, "\x00")
		path := string(pathBytes)
		if path == "" {
			return nil, 0, fmt.Errorf("godot: entry %d has empty path", i)
		}
		if bytes.Contains(pathBytes, []byte{0}) {
			return nil, 0, fmt.Errorf("godot: entry %d has embedded NUL in path", i)
		}
		if _, ok := seen[path]; ok {
			return nil, 0, fmt.Errorf("godot: duplicate entry path %q", path)
		}
		seen[path] = struct{}{}
		ofs := binary.LittleEndian.Uint64(data[off:])
		size := binary.LittleEndian.Uint64(data[off+8:])
		off += 16
		var digest [16]byte
		copy(digest[:], data[off:off+16])
		off += 16
		flags := binary.LittleEndian.Uint32(data[off:])
		off += 4
		if flags&packFileRemoval != 0 {
			return nil, 0, fmt.Errorf("godot: entry %q uses unsupported removal flag %#x", path, flags)
		}
		if flags&packFileDelta != 0 {
			return nil, 0, fmt.Errorf("godot: entry %q uses unsupported delta flag %#x", path, flags)
		}
		if flags&^uint32(packFileEncrypted) != 0 {
			return nil, 0, fmt.Errorf("godot: entry %q has unknown flags %#x", path, flags)
		}
		entries = append(entries, pckEntry{
			Path:   path,
			Offset: ofs,
			Size:   size,
			MD5:    digest,
			Flags:  flags,
		})
	}
	return entries, off, nil
}

func readEncryptedBlockHeader(data []byte, offset uint64) (encryptedBlock, error) {
	if offset > uint64(len(data)) || offset+encryptedBlockHeaderSize > uint64(len(data)) {
		return encryptedBlock{}, fmt.Errorf("encrypted block header is truncated")
	}
	var b encryptedBlock
	b.Offset = offset
	copy(b.MD5[:], data[offset:offset+16])
	b.Size = binary.LittleEndian.Uint64(data[offset+16 : offset+24])
	copy(b.IV[:], data[offset+24:offset+40])
	if b.Size > 1<<40 {
		return encryptedBlock{}, fmt.Errorf("encrypted block length is unreasonable")
	}
	padded := b.Size
	if rem := padded % 16; rem != 0 {
		padded += 16 - rem
	}
	if padded > ^uint64(0)-encryptedBlockHeaderSize {
		return encryptedBlock{}, fmt.Errorf("encrypted block length overflow")
	}
	b.StoredSize = encryptedBlockHeaderSize + padded
	return b, nil
}

func readEncryptedBlock(data []byte, offset, limit uint64) (encryptedBlock, error) {
	b, err := readEncryptedBlockHeader(data, offset)
	if err != nil {
		return encryptedBlock{}, err
	}
	if b.StoredSize > limit || b.Offset+b.StoredSize < b.Offset || b.Offset+b.StoredSize > limit {
		return encryptedBlock{}, fmt.Errorf("encrypted block is outside payload")
	}
	return b, nil
}

// decryptEncryptedBlock 逐次校验整个明文的 MD5，再交给调用方消费。
// 对加密成员，密文头中的摘要必须与目录条目一致，不能只信任其中一个。
func decryptEncryptedBlock(data []byte, b encryptedBlock, key []byte, consume func([]byte) error) error {
	if len(key) != 32 {
		return fmt.Errorf("godot: AES key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	padded := b.StoredSize - encryptedBlockHeaderSize
	plain := make([]byte, padded)
	cipher.NewCFBDecrypter(block, b.IV[:]).XORKeyStream(plain, data[b.Offset+encryptedBlockHeaderSize:b.Offset+b.StoredSize])
	got := md5.Sum(plain[:b.Size])
	if !bytes.Equal(got[:], b.MD5[:]) {
		return fmt.Errorf("godot: encrypted block MD5 mismatch (wrong key or corrupt data)")
	}
	if consume != nil {
		return consume(plain[:b.Size])
	}
	return nil
}

func validateEntryRanges(h *pckFile, data []byte, key []byte) error {
	base := uint64(h.Start) + h.FileBase
	type span struct{ start, end uint64 }
	spans := make([]span, 0, len(h.Entries))
	for i := range h.Entries {
		e := &h.Entries[i]
		abs := base + e.Offset
		if abs < base {
			return fmt.Errorf("godot: entry %q offset overflow", e.Path)
		}
		if e.Flags&packFileEncrypted != 0 {
			b, err := readEncryptedBlock(data, abs, uint64(h.End))
			if err != nil {
				return fmt.Errorf("godot: entry %q: %w", e.Path, err)
			}
			if b.Size != e.Size {
				return fmt.Errorf("godot: entry %q encrypted size mismatch", e.Path)
			}
			if b.MD5 != e.MD5 {
				return fmt.Errorf("godot: entry %q encrypted digest mismatch", e.Path)
			}
			e.StoredSize = b.StoredSize
			if len(key) == 32 {
				if err := decryptEncryptedBlock(data, b, key, nil); err != nil {
					return fmt.Errorf("godot: entry %q: %w", e.Path, err)
				}
			}
		} else {
			e.StoredSize = e.Size
			if abs+e.Size < abs || abs+e.Size > uint64(h.End) {
				return fmt.Errorf("godot: entry %q blob out of range", e.Path)
			}
			got := md5.Sum(data[abs : abs+e.Size])
			if got != e.MD5 {
				return fmt.Errorf("godot: entry %q plaintext digest mismatch", e.Path)
			}
		}
		spans = append(spans, span{abs, abs + e.StoredSize})
	}
	dirStart := uint64(h.DirStart)
	dirEnd := uint64(h.DirEnd)
	for _, s := range spans {
		if s.start < dirEnd && s.end > dirStart {
			return fmt.Errorf("godot: PCK member overlaps directory")
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return fmt.Errorf("godot: PCK member payloads overlap")
		}
	}
	return nil
}
