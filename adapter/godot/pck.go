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
	"strings"
)

const (
	pckMagic = "GDPC"

	packFlagDirEncrypted = 1 << 0
	packFlagRelFileBase  = 1 << 1
	packFlagSparseBundle = 1 << 2
	packFlagKnownMask    = packFlagDirEncrypted | packFlagRelFileBase | packFlagSparseBundle

	packFileEncrypted = 1 << 0
	packFileRemoval   = 1 << 1
	packFileDelta     = 1 << 2
	packFileKnownMask = packFileEncrypted | packFileRemoval | packFileDelta
)

// ErrKeyRequired 表示 PCK 目录已加密但调用方没有提供 key。
var ErrKeyRequired = errors.New("godot: pck directory key required")

// pckFile 是解析后的 Godot PCK。
type pckFile struct {
	Start     int
	Version   uint32
	Major     uint32
	Minor     uint32
	Patch     uint32
	Flags     uint32
	FileBase  uint64 // 相对 PCK 起始位置的 file_base
	DirOffset uint64 // 相对 PCK 起始位置的目录位置
	Entries   []pckEntry
}

// pckEntry 是一个 PCK 目录条目。
type pckEntry struct {
	Path   string
	Offset uint64 // 相对 file_base 的文件偏移
	Size   uint64 // 明文大小
	MD5    [16]byte
	Flags  uint32
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
		// 兼容直接传入 32 个原始字符的调用方。
		return []byte(s), nil
	}
	return nil, fmt.Errorf("AES key must be 32 raw bytes or 64 hex characters")
}

// findPCKStart 在 data 中寻找一个可解析的 GDPC 头。
// 代码段中可能出现 GDPC 字符串，因此必须校验版本、flags、file_base 和 dir_offset。
func findPCKStart(data []byte) (int, error) {
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

// parsePCK 解析 data 中的 Godot PCK。key 仅在目录加密时需要。
func parsePCK(data []byte, key []byte) (*pckFile, error) {
	start, err := findPCKStart(data)
	if err != nil {
		return nil, err
	}
	rawFileBase := binary.LittleEndian.Uint64(data[start+0x18:])
	h := &pckFile{
		Start:   start,
		Version: binary.LittleEndian.Uint32(data[start+4:]),
		Major:   binary.LittleEndian.Uint32(data[start+8:]),
		Minor:   binary.LittleEndian.Uint32(data[start+12:]),
		Patch:   binary.LittleEndian.Uint32(data[start+16:]),
		Flags:   binary.LittleEndian.Uint32(data[start+0x14:]),
	}
	if h.Version >= 3 || h.Flags&packFlagRelFileBase != 0 {
		h.FileBase = rawFileBase
	} else {
		if rawFileBase < uint64(start) {
			return nil, fmt.Errorf("godot: absolute file_base points before PCK")
		}
		h.FileBase = rawFileBase - uint64(start)
	}

	var dirStart int
	if h.Version == 3 || h.Version == 4 {
		h.DirOffset = binary.LittleEndian.Uint64(data[start+0x20:])
		dirStart = start + int(h.DirOffset)
	} else {
		h.DirOffset = 0x60
		dirStart = start + 0x60
	}

	if dirStart+4 > len(data) {
		return nil, fmt.Errorf("godot: directory offset out of range")
	}
	fileCount := binary.LittleEndian.Uint32(data[dirStart:])
	if fileCount > 1<<24 {
		return nil, fmt.Errorf("godot: unreasonable file count %d", fileCount)
	}

	dirData := data[dirStart+4:]
	if h.Flags&packFlagDirEncrypted != 0 {
		if len(key) == 0 {
			return nil, ErrKeyRequired
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("godot: AES key must be 32 bytes, got %d", len(key))
		}
		if dirStart+4+16+8+16 > len(data) {
			return nil, fmt.Errorf("godot: encrypted directory header is truncated")
		}
		p := dirStart + 4
		wantMD5 := data[p : p+16]
		plainLen := binary.LittleEndian.Uint64(data[p+16:])
		if plainLen > uint64(len(data)-p-24) {
			return nil, fmt.Errorf("godot: encrypted directory length out of range")
		}
		iv := data[p+24 : p+40]
		ctStart := p + 40
		paddedLen := plainLen
		if rem := paddedLen % 16; rem != 0 {
			paddedLen += 16 - rem
		}
		if uint64(ctStart)+paddedLen > uint64(len(data)) {
			return nil, fmt.Errorf("godot: encrypted directory ciphertext is truncated")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		plain := make([]byte, paddedLen)
		cipher.NewCFBDecrypter(block, iv).XORKeyStream(plain, data[ctStart:ctStart+int(paddedLen)])
		plain = plain[:plainLen]
		got := md5.Sum(plain)
		if !bytes.Equal(got[:], wantMD5) {
			return nil, fmt.Errorf("godot: encrypted directory MD5 mismatch (wrong key or corrupt data)")
		}
		dirData = plain
	}

	entries, err := parseEntries(dirData, int(fileCount))
	if err != nil {
		return nil, err
	}
	h.Entries = entries
	return h, nil
}

func parseEntries(data []byte, count int) ([]pckEntry, error) {
	entries := make([]pckEntry, 0, count)
	seen := make(map[string]struct{}, count)
	off := 0
	for i := 0; i < count; i++ {
		if off+4 > len(data) {
			return nil, fmt.Errorf("godot: entry %d path length out of range", i)
		}
		rawLen := binary.LittleEndian.Uint32(data[off:])
		off += 4
		if rawLen == 0 || rawLen > 4096 || off+int(rawLen)+36 > len(data) {
			return nil, fmt.Errorf("godot: entry %d invalid path length %d", i, rawLen)
		}
		rawPath := data[off : off+int(rawLen)]
		off += int(rawLen)
		pathBytes := bytes.TrimRight(rawPath, "\x00")
		path := string(pathBytes)
		if path == "" {
			return nil, fmt.Errorf("godot: entry %d has empty path", i)
		}
		if _, ok := seen[path]; ok {
			return nil, fmt.Errorf("godot: duplicate entry path %q", path)
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
		if flags&^uint32(packFileKnownMask) != 0 {
			return nil, fmt.Errorf("godot: entry %q has unknown flags %#x", path, flags)
		}
		entries = append(entries, pckEntry{
			Path:   path,
			Offset: ofs,
			Size:   size,
			MD5:    digest,
			Flags:  flags,
		})
	}
	return entries, nil
}
