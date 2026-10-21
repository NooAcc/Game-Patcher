package godot

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"testing"
)

func TestParsePCKV3PlainDirectory(t *testing.T) {
	payload := []byte("hello")
	data := buildPCKFixture(t, 3, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(payload)), digest: md5.Sum(payload)}},
		payload, false, nil)
	pck, err := parsePCK(data, nil)
	if err != nil {
		t.Fatalf("parse v3: %v", err)
	}
	if pck.Version != 3 || len(pck.Entries) != 1 || pck.Entries[0].Path != "a.txt" {
		t.Fatalf("unexpected v3 pack: %#v", pck)
	}
}

func TestParsePCKV2EncryptedDirectory(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	data := buildPCKFixture(t, 2, packFlagDirEncrypted|packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain), flags: packFileEncrypted}},
		blob, true, key)
	pck, err := parsePCK(data, key)
	if err != nil {
		t.Fatalf("parse encrypted v2: %v", err)
	}
	if pck.Version != 2 || pck.Flags != (packFlagDirEncrypted|packFlagRelFileBase) {
		t.Fatalf("unexpected header: %#v", pck)
	}
	if len(pck.Entries) != 1 || pck.Entries[0].StoredSize != uint64(len(blob)) {
		t.Fatalf("unexpected entry: %#v", pck.Entries)
	}
}

func TestParsePCKV2TrailingDataPreservesTrailer(t *testing.T) {
	plain := []byte("hello")
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain)}},
		plain, false, nil)
	trailer := []byte{0xde, 0xad, 0xbe, 0xef}
	var footer [12]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(len(data)))
	copy(footer[8:], pckMagic)
	data = append(append([]byte("MZ-prefix"), data...), trailer...)
	data = append(data, footer[:]...)
	pck, err := parsePCK(data, nil)
	if err != nil {
		t.Fatalf("parse with trailer: %v", err)
	}
	want := append(append([]byte(nil), trailer...), footer[:]...)
	if !bytes.Equal(pck.Trailer, want) {
		t.Fatalf("trailer=%x want=%x", pck.Trailer, want)
	}
}

func TestParsePCKRejectsMalformedEmbeddedFooter(t *testing.T) {
	body := buildV2(t)
	data := append([]byte("MZ-prefix"), body...)
	var footer [12]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(len(body)+1))
	copy(footer[8:], pckMagic)
	data = append(data, footer[:]...)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("malformed embedded footer must not produce a valid pack")
	}
}
