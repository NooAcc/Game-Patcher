package godot

import (
	"crypto/md5"
	"testing"
)

func TestParsePCKRejectsCorruptEncryptedMemberCiphertext(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello world")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	// Corrupt the first ciphertext byte while leaving both directory and
	// inner-header digests unchanged. The trailing padding is not covered by
	// the MD5 and therefore cannot be authenticated by this format.
	blob[encryptedBlockHeaderSize] ^= 0xff
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain), flags: packFileEncrypted}},
		blob, false, nil)
	if _, err := parsePCK(data, key); err == nil {
		t.Fatal("corrupt encrypted member ciphertext must be rejected when key is available")
	}
}

func TestParsePCKRejectsPlainMemberMD5Mismatch(t *testing.T) {
	plain := []byte("hello")
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum([]byte("world"))}},
		plain, false, nil)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("plain member digest must cover the actual payload")
	}
}
