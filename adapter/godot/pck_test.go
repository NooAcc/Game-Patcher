package godot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"testing"
)

const (
	testKeyHex = "01293b8d0b5d4aec182f82456c5e3c2269702c1b527648c4a5c274232b40ad23"
)

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

// appendEntry 按 Godot PCK 目录格式写入一个条目。path 会补 NUL 到 4 字节对齐，
// 并把补齐后的长度写入 pathLen（与 Godot 导出的实际形态一致）。
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

func directoryBytes(t *testing.T, entries int) []byte {
	t.Helper()
	var dir bytes.Buffer
	for i := 0; i < entries; i++ {
		path := "a.txt"
		if i == 1 {
			path = "dir/b.bin"
		}
		digest := md5.Sum([]byte{byte(i), byte(i + 1), byte(i + 2)})
		appendEntry(t, &dir, path, uint64(i*16), uint64(i+5), digest, 0)
	}
	return dir.Bytes()
}

func buildV2(t *testing.T) []byte {
	t.Helper()
	entries := 2
	var b bytes.Buffer
	b.WriteString("GDPC")
	appendU32(&b, 2) // pack format version
	appendU32(&b, 4)
	appendU32(&b, 7)
	appendU32(&b, 2)
	appendU32(&b, 2) // REL_FILEBASE
	appendU64(&b, 0x60)
	for i := 0; i < 16; i++ {
		appendU32(&b, 0)
	}
	appendU32(&b, uint32(entries))
	b.Write(directoryBytes(t, entries))
	return b.Bytes()
}

func buildV4Encrypted(t *testing.T, key []byte) []byte {
	t.Helper()
	entries := 2
	dir := directoryBytes(t, entries)
	var encDir bytes.Buffer
	digest := md5.Sum(dir)
	encDir.Write(digest[:])
	appendU64(&encDir, uint64(len(dir)))
	iv := []byte("0123456789abcdef")
	encDir.Write(iv)
	padded := append([]byte(nil), dir...)
	for len(padded)%16 != 0 {
		padded = append(padded, 0)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	stream := cipher.NewCFBEncrypter(block, iv)
	ct := make([]byte, len(padded))
	stream.XORKeyStream(ct, padded)
	encDir.Write(ct)

	var b bytes.Buffer
	b.WriteString("GDPC")
	appendU32(&b, 4)
	appendU32(&b, 4)
	appendU32(&b, 7)
	appendU32(&b, 2)
	appendU32(&b, 3) // DIR_ENCRYPTED | REL_FILEBASE
	appendU64(&b, 0x70)
	dirOffset := uint64(0x40)
	appendU64(&b, dirOffset)
	for b.Len() < int(dirOffset) {
		b.WriteByte(0)
	}
	appendU32(&b, uint32(entries))
	b.Write(encDir.Bytes())
	return b.Bytes()
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
	if pck.Entries[1].Path != "dir/b.bin" || pck.Entries[1].Size != 6 || pck.Entries[1].Offset != 16 {
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
