package godot

import (
	"crypto/md5"
	"encoding/binary"
	"testing"
)

func TestParsePCKRejectsOversizedEncryptedDirectory(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	data := buildPCKFixture(t, 2, packFlagDirEncrypted|packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain), flags: packFileEncrypted}},
		blob, true, key)
	binary.LittleEndian.PutUint64(data[0x60+4+16:], 64<<20+1)
	if _, err := parsePCK(data, key); err == nil {
		t.Fatal("oversized encrypted directory must be rejected")
	}
}

func TestParsePCKRejectsEncryptedDirectorySizeAboveLimitBeforeAllocation(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	data := buildPCKFixture(t, 2, packFlagDirEncrypted|packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain), flags: packFileEncrypted}},
		blob, true, key)
	binary.LittleEndian.PutUint64(data[0x60+4+16:], maxDirectoryBytes+1)
	if _, err := parsePCK(data, key); err == nil {
		t.Fatal("directory size above the processing limit must be rejected")
	}
}

func TestParsePCKRejectsTruncatedPlainDirectory(t *testing.T) {
	data := buildV2(t)
	data = data[:len(data)-1]
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("truncated plain directory must be rejected")
	}
}

func TestParsePCKRejectsOversizedPlainDirectory(t *testing.T) {
	data := buildV2(t)
	binary.LittleEndian.PutUint32(data[0x60:], 1<<24+1)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("oversized plain directory count must be rejected before allocation")
	}
}

func TestParsePCKRejectsPathLengthAfterCountLimit(t *testing.T) {
	data := buildV2(t)
	binary.LittleEndian.PutUint32(data[0x60:], 1<<24+1)
	binary.LittleEndian.PutUint32(data[0x64:], 4097)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("excessive directory metadata must be rejected")
	}
}
