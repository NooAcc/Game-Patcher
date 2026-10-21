package godot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"strings"
)

const maxKeyDiscoveryScan = 256 << 20
const keyScanChunk = 64 << 10
const keyOverlap = 31

// findGodotKey attempts to recover a 32-byte AES key from the old EXE/PCK.
// A candidate is accepted only after the full encrypted-directory MD5 passes.
func findGodotKey(oldData []byte, oldPath string) ([]byte, error) {
	probe, err := keyProbe(oldData)
	if err != nil {
		return nil, err
	}
	scanned := 0
	if key, ok := findKeyInData(oldData, probe, &scanned); ok {
		return key, nil
	}
	return nil, fmt.Errorf("no valid AES key found in %s (scan limit %d MiB)", oldPath, maxKeyDiscoveryScan>>20)
}

// findKeyInData scans PE data sections first, then executable sections. A
// window overlaps by 31 bytes so a key crossing a chunk boundary is considered.
// scanned is a shared total budget in bytes across all carriers.
func findKeyInData(data []byte, probe keyProbeRange, scanned *int) ([]byte, bool) {
	if len(data) == 0 {
		return nil, false
	}
	regions := keyScanRegions(data)
	for _, region := range regions {
		for start := region.start; start < region.end; start += keyScanChunk {
			end := start + keyScanChunk + keyOverlap
			if end > region.end {
				end = region.end
			}
			if *scanned+end-start > maxKeyDiscoveryScan {
				return nil, false
			}
			*scanned += end - start
			for off := start; off+32 <= end; off++ {
				key := data[off : off+32]
				if verifyEncryptedDirectory(data, probe, key) {
					return append([]byte(nil), key...), true
				}
			}
			if end == region.end {
				break
			}
		}
	}
	return nil, false
}

type keyScanRegion struct {
	start int
	end   int
}

// keyScanRegions prioritizes non-executable PE sections and excludes the PCK
// section. Non-PE carriers are still scanned conservatively.
func keyScanRegions(data []byte) []keyScanRegion {
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return []keyScanRegion{{start: 0, end: len(data)}}
	}
	e := int(binary.LittleEndian.Uint32(data[0x3c:]))
	if e < 0 || e+24 > len(data) {
		return []keyScanRegion{{start: 0, end: len(data)}}
	}
	nsec := int(binary.LittleEndian.Uint16(data[e+6:]))
	if nsec <= 0 || nsec > 96 {
		return []keyScanRegion{{start: 0, end: len(data)}}
	}
	optSize := int(binary.LittleEndian.Uint16(data[e+20:]))
	secOff := e + 24 + optSize
	if secOff < 0 || secOff+nsec*40 > len(data) {
		return []keyScanRegion{{start: 0, end: len(data)}}
	}
	var writableRegions []keyScanRegion
	var dataRegions []keyScanRegion
	var executable []keyScanRegion
	for i := 0; i < nsec; i++ {
		off := secOff + i*40
		name := strings.TrimRight(string(data[off:off+8]), "\x00")
		raw := int(binary.LittleEndian.Uint32(data[off+20:]))
		size := int(binary.LittleEndian.Uint32(data[off+16:]))
		if raw < 0 || size <= 0 || raw >= len(data) {
			continue
		}
		if raw+size > len(data) {
			size = len(data) - raw
		}
		region := keyScanRegion{start: raw, end: raw + size}
		flags := binary.LittleEndian.Uint32(data[off+36:])
		if strings.EqualFold(name, "pck") {
			continue
		}
		if flags&0x20000000 != 0 {
			executable = append(executable, region)
		} else if flags&0x80000000 != 0 {
			writableRegions = append(writableRegions, region)
		} else {
			dataRegions = append(dataRegions, region)
		}
	}
	regions := append(writableRegions, dataRegions...)
	regions = append(regions, executable...)
	if len(regions) == 0 {
		return []keyScanRegion{{start: 0, end: len(data)}}
	}
	return regions
}

type keyProbeRange struct {
	offset int
	size   uint64
	md5    [16]byte
}

func keyProbe(data []byte) (keyProbeRange, error) {
	start, err := findPCKStart(data)
	if err != nil {
		return keyProbeRange{}, err
	}
	version := binary.LittleEndian.Uint32(data[start+4:])
	flags := binary.LittleEndian.Uint32(data[start+0x14:])
	if flags&packFlagDirEncrypted == 0 {
		return keyProbeRange{}, fmt.Errorf("PCK directory is not encrypted")
	}
	dirStart := start + 0x60
	if version == 3 || version == 4 {
		dirStart = start + int(binary.LittleEndian.Uint64(data[start+0x20:]))
	}
	header := dirStart + 4
	if header+encryptedBlockHeaderSize > len(data) {
		return keyProbeRange{}, fmt.Errorf("encrypted directory header is truncated")
	}
	var p keyProbeRange
	p.offset = header
	copy(p.md5[:], data[header:header+16])
	p.size = binary.LittleEndian.Uint64(data[header+16 : header+24])
	return p, nil
}

func verifyEncryptedDirectory(data []byte, probe keyProbeRange, key []byte) bool {
	if probe.offset < 0 || probe.size > maxDirectoryBytes || uint64(probe.offset)+40 > uint64(len(data)) {
		return false
	}
	padded := probe.size
	if rem := padded % 16; rem != 0 {
		padded += 16 - rem
	}
	if uint64(probe.offset)+40+padded > uint64(len(data)) {
		return false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return false
	}
	iv := data[probe.offset+24 : probe.offset+40]
	// Wrong candidates usually fail at the first path-length field. This
	// cheap check avoids a full directory decrypt for almost every window.
	if probe.size >= 4 {
		var prefix [4]byte
		cipher.NewCFBDecrypter(block, iv).XORKeyStream(prefix[:], data[probe.offset+40:probe.offset+44])
		pathLen := binary.LittleEndian.Uint32(prefix[:])
		if pathLen == 0 || pathLen > 4096 || uint64(pathLen)+36 > probe.size {
			return false
		}
	}
	plain := make([]byte, padded)
	cipher.NewCFBDecrypter(block, iv).XORKeyStream(plain, data[probe.offset+40:probe.offset+40+int(padded)])
	sum := md5.Sum(plain[:probe.size])
	return bytes.Equal(sum[:], probe.md5[:])
}
