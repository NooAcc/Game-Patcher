package godot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"testing"
)

const testKeyHex = "01293b8d0b5d4aec182f82456c5e3c2269702c1b527648c4a5c274232b40ad23"

func testKey(t *testing.T) []byte {
	t.Helper()
	b, err := parseHexKey(testKeyHex)
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return b
}

func appendU32(b *bytes.Buffer, v uint32) { _ = binary.Write(b, binary.LittleEndian, v) }
func appendU64(b *bytes.Buffer, v uint64) { _ = binary.Write(b, binary.LittleEndian, v) }

func appendEntry(t *testing.T, b *bytes.Buffer, path string, ofs, size uint64, digest [16]byte, flags uint32) {
	t.Helper()
	raw := []byte(path)
	for len(raw)%4 != 0 {
		raw = append(raw, 0)
	}
	appendU32(b, uint32(len(raw)))
	b.Write(raw)
	appendU64(b, ofs)
	appendU64(b, size)
	b.Write(digest[:])
	appendU32(b, flags)
}

func encryptedBlob(plain, key, iv []byte) []byte {
	digest := md5.Sum(plain)
	padded := append([]byte(nil), plain...)
	for len(padded)%16 != 0 {
		padded = append(padded, 0)
	}
	block, _ := aes.NewCipher(key)
	ct := make([]byte, len(padded))
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(ct, padded)
	var out bytes.Buffer
	out.Write(digest[:])
	_ = binary.Write(&out, binary.LittleEndian, uint64(len(plain)))
	out.Write(iv)
	out.Write(ct)
	return out.Bytes()
}

type packFixtureEntry struct {
	path   string
	offset uint64
	size   uint64
	digest [16]byte
	flags  uint32
}

// buildPCKFixture builds a legal v2/v3/v4 pack. v2 puts the directory at
// 0x60 and data after it; v3/v4 put data at 0x60 and the directory after it.
func buildPCKFixture(t *testing.T, version, flags uint32, entries []packFixtureEntry, payload []byte, encryptedDir bool, key []byte) []byte {
	t.Helper()
	var dir bytes.Buffer
	for _, e := range entries {
		appendEntry(t, &dir, e.path, e.offset, e.size, e.digest, e.flags)
	}
	var b bytes.Buffer
	b.WriteString("GDPC")
	appendU32(&b, version)
	appendU32(&b, 4)
	appendU32(&b, 7)
	appendU32(&b, 2)
	appendU32(&b, flags)
	if version == 2 {
		if flags&packFlagRelFileBase == 0 {
			t.Fatal("fixture: v2 requires relative file base")
		}
		dirSize := dir.Len()
		if encryptedDir {
			dirSize = len(encryptedDirectoryBlock(t, dir.Bytes(), key))
		}
		fileBase := alignTest16(0x60 + 4 + dirSize)
		appendU64(&b, uint64(fileBase))
		for i := 0; i < 16; i++ {
			appendU32(&b, 0)
		}
		appendU32(&b, uint32(len(entries)))
		if encryptedDir {
			b.Write(encryptedDirectoryBlock(t, dir.Bytes(), key))
		} else {
			b.Write(dir.Bytes())
		}
		for b.Len() < fileBase {
			b.WriteByte(0)
		}
		b.Write(payload)
		return b.Bytes()
	}
	dirOffset := alignTest16(0x60 + len(payload))
	appendU64(&b, 0x60)
	appendU64(&b, uint64(dirOffset))
	for b.Len() < 0x60 {
		b.WriteByte(0)
	}
	b.Write(payload)
	for b.Len() < dirOffset {
		b.WriteByte(0)
	}
	appendU32(&b, uint32(len(entries)))
	if encryptedDir {
		b.Write(encryptedDirectoryBlock(t, dir.Bytes(), key))
	} else {
		b.Write(dir.Bytes())
	}
	return b.Bytes()
}

func encryptedDirectoryBlock(t *testing.T, plain, key []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	sum := md5.Sum(plain)
	out.Write(sum[:])
	appendU64(&out, uint64(len(plain)))
	iv := []byte("0123456789abcdef")
	out.Write(iv)
	padded := append([]byte(nil), plain...)
	for len(padded)%16 != 0 {
		padded = append(padded, 0)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	ct := make([]byte, len(padded))
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(ct, padded)
	out.Write(ct)
	return out.Bytes()
}

func alignTest16(n int) int {
	if n%16 == 0 {
		return n
	}
	return n + 16 - n%16
}

func buildV2(t *testing.T) []byte {
	t.Helper()
	payload := []byte("\xa1\xa1\xa1\xa1\xa1\xb2\xb2\xb2\xb2\xb2\xb2")
	return buildPCKFixture(t, 2, packFlagRelFileBase, []packFixtureEntry{
		{path: "a.txt", offset: 0, size: 5, digest: md5.Sum(payload[:5])},
		{path: "dir/b.bin", offset: 5, size: 6, digest: md5.Sum(payload[5:])},
	}, payload, false, nil)
}

func buildV4Encrypted(t *testing.T, key []byte) []byte {
	t.Helper()
	payload := []byte("\x31\x31\x31\x31\x31\x32\x32\x32\x32\x32\x32")
	return buildPCKFixture(t, 4, packFlagDirEncrypted|packFlagRelFileBase, []packFixtureEntry{
		{path: "a.txt", offset: 0, size: 5, digest: md5.Sum(payload[:5])},
		{path: "dir/b.bin", offset: 5, size: 6, digest: md5.Sum(payload[5:])},
	}, payload, true, key)
}

func TestParsePCKV2PlainDirectory(t *testing.T) {
	pck, err := parsePCK(buildV2(t), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pck.Version != 2 || pck.Flags != 2 {
		t.Fatalf("unexpected header: version=%d flags=%#x", pck.Version, pck.Flags)
	}
	if len(pck.Entries) != 2 {
		t.Fatalf("entries=%d, want 2", len(pck.Entries))
	}
	if pck.Entries[0].Path != "a.txt" || pck.Entries[0].Size != 5 || pck.Entries[0].Offset != 0 {
		t.Fatalf("entry 0 = %#v", pck.Entries[0])
	}
	if pck.Entries[1].Path != "dir/b.bin" || pck.Entries[1].Size != 6 || pck.Entries[1].Offset != 5 {
		t.Fatalf("entry 1 = %#v", pck.Entries[1])
	}
}

func TestParsePCKV4EncryptedDirectory(t *testing.T) {
	key := testKey(t)
	pck, err := parsePCK(buildV4Encrypted(t, key), key)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pck.Version != 4 || pck.Flags != 3 {
		t.Fatalf("unexpected header: version=%d flags=%#x", pck.Version, pck.Flags)
	}
	if len(pck.Entries) != 2 || pck.Entries[1].Path != "dir/b.bin" {
		t.Fatalf("entries=%#v", pck.Entries)
	}
}

func TestParsePCKV4EncryptedRejectsWrongKey(t *testing.T) {
	key := testKey(t)
	wrong := append([]byte(nil), key...)
	wrong[0] ^= 0xff
	if _, err := parsePCK(buildV4Encrypted(t, key), wrong); err == nil {
		t.Fatal("expected wrong-key error")
	}
}

func TestFindPCKStartSkipsFalsePositive(t *testing.T) {
	blob := append([]byte("xGDPCnot-a-header"), buildV2(t)...)
	start, err := findPCKStart(blob)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if start != len("xGDPCnot-a-header") {
		t.Fatalf("start=%d, want %d", start, len("xGDPCnot-a-header"))
	}
}
