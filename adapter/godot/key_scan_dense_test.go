package godot

import (
	"encoding/binary"
	"testing"
)

func TestKeyScanRegionsFindsKeyAfterDenseData(t *testing.T) {
	key := testKey(t)
	pe := makeDenseTestPEWithKey(key, 0x4000)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	oldData := append(append([]byte(nil), pe...), oldPCK...)
	probe, err := keyProbe(oldData)
	if err != nil {
		t.Fatal(err)
	}
	for _, region := range keyScanRegions(oldData) {
		if region.start <= 0x4000 && region.end >= 0x4020 {
			scanned := 0
			if _, ok := findKeyInData(oldData, probe, &scanned); !ok {
				t.Fatal("key after a dense 16 KiB data prefix was not found")
			}
			return
		}
	}
	t.Fatal("dense-data key location was not covered by any scan region")
}

func makeDenseTestPEWithKey(key []byte, keyOffset int) []byte {
	size := keyOffset + 0x2000
	b := make([]byte, size)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	binary.LittleEndian.PutUint16(b[0x86:], 1)
	binary.LittleEndian.PutUint16(b[0x94:], 0xe0)
	sec := 0x178
	copy(b[sec:sec+8], ".rdata")
	binary.LittleEndian.PutUint32(b[sec+8:], uint32(size-0x200))
	binary.LittleEndian.PutUint32(b[sec+12:], 0x1000)
	binary.LittleEndian.PutUint32(b[sec+16:], uint32(size-0x200))
	binary.LittleEndian.PutUint32(b[sec+20:], 0x200)
	// Fill with non-overlapping pseudo-random data so duplicate suppression
	// cannot hide a key that appears after the prefix.
	x := uint32(0x9e3779b9)
	for i := 0x200; i < keyOffset; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	copy(b[keyOffset:keyOffset+32], key)
	return b
}
