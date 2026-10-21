package godot

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"testing"
)

func TestParsePCKRejectsSparseBundle(t *testing.T) {
	data := buildV2(t)
	binary.LittleEndian.PutUint32(data[0x14:], packFlagRelFileBase|packFlagSparseBundle)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("sparse bundle must be rejected")
	}
}

func setFirstEntryFlags(t *testing.T, data []byte, flags uint32) {
	t.Helper()
	var dir bytes.Buffer
	payload := []byte("\xa1\xa1\xa1\xa1\xa1")
	appendEntry(t, &dir, "a.txt", 0, 5, md5.Sum(payload), 0)
	pos := bytes.Index(data, dir.Bytes())
	if pos < 0 {
		t.Fatal("fixture entry missing")
	}
	binary.LittleEndian.PutUint32(data[pos+len(dir.Bytes())-4:], flags)
}

func TestParsePCKRejectsRemovalAndDeltaMembers(t *testing.T) {
	for _, flags := range []uint32{packFileRemoval, packFileDelta} {
		data := buildV2(t)
		setFirstEntryFlags(t, data, flags)
		if _, err := parsePCK(data, nil); err == nil {
			t.Fatalf("member flags %#x must be rejected", flags)
		}
	}
}

func TestParsePCKRecordsStoredSize(t *testing.T) {
	key := testKey(t)
	plain := []byte("1234567890123456789")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.bin", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain), flags: packFileEncrypted}},
		blob, false, nil)
	pck, err := parsePCK(data, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(pck.Entries) != 1 {
		t.Fatalf("entries=%d", len(pck.Entries))
	}
	if pck.Entries[0].StoredSize != uint64(len(blob)) {
		t.Fatalf("stored=%d want=%d", pck.Entries[0].StoredSize, len(blob))
	}
}

func TestParsePCKRejectsMemberOutsidePayload(t *testing.T) {
	plain := []byte("hello")
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 1000, size: 5, digest: md5.Sum(plain)}}, plain, false, nil)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("out-of-range member must be rejected")
	}
}

func TestParsePCKRejectsMemberOverlap(t *testing.T) {
	payload := []byte("0123456789")
	data := buildPCKFixture(t, 2, packFlagRelFileBase, []packFixtureEntry{
		{path: "a.txt", offset: 0, size: 8, digest: md5.Sum(payload[:8])},
		{path: "b.txt", offset: 4, size: 6, digest: md5.Sum(payload[4:])},
	}, payload, false, nil)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("overlapping members must be rejected")
	}
}

func TestParsePCKRejectsMemberOverlappingDirectory(t *testing.T) {
	plain := []byte("hello")
	entry := packFixtureEntry{path: "a.txt", offset: uint64(0x60 + 9), size: 5, digest: md5.Sum(plain)}
	data := buildPCKFixture(t, 3, packFlagRelFileBase, []packFixtureEntry{entry}, []byte("123456789"), false, nil)
	if _, err := parsePCK(data, nil); err == nil {
		t.Fatal("member overlapping directory must be rejected")
	}
}

func TestParsePCKRejectsEncryptedMemberMD5Mismatch(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum([]byte("diff!")), flags: packFileEncrypted}},
		blob, false, nil)
	if _, err := parsePCK(data, key); err == nil {
		t.Fatal("mismatched encrypted member digest must be rejected")
	}
}

func TestParsePCKRejectsEncryptedHeaderSizeMismatch(t *testing.T) {
	var blob bytes.Buffer
	blob.Write(make([]byte, 16))
	_ = binary.Write(&blob, binary.LittleEndian, uint64(99))
	blob.Write(bytes.Repeat([]byte{1}, 16))
	blob.Write(make([]byte, 32))
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.bin", offset: 0, size: 5, digest: md5.Sum([]byte("hello")), flags: packFileEncrypted}},
		blob.Bytes(), false, nil)
	if _, err := parsePCK(data, testKey(t)); err == nil {
		t.Fatal("encrypted header size mismatch must be rejected")
	}
}

func TestParsePCKRejectsEmptyEncryptedDirectory(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello")
	blob := encryptedBlob(plain, key, []byte("0123456789abcdef"))
	data := buildPCKFixture(t, 2, packFlagRelFileBase,
		[]packFixtureEntry{{path: "a.txt", offset: 0, size: uint64(len(plain)), digest: md5.Sum(plain), flags: packFileEncrypted}},
		blob, true, key)
	binary.LittleEndian.PutUint64(data[0x60+4+16:], 0)
	if _, err := parsePCK(data, key); err == nil {
		t.Fatal("invalid encrypted directory must be rejected")
	}
}
