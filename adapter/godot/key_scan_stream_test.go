package godot

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"game-patcher/adapter"
)

func TestFindKeyInDataFindsKeyAfterLargeHighEntropyData(t *testing.T) {
	key := testKey(t)
	pe := makeHighEntropyTestPEWithKey(key, 1<<20)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	oldData := append(append([]byte(nil), pe...), oldPCK...)
	probe, err := keyProbe(oldData)
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	if _, ok := findKeyInData(oldData, probe, &scanned); !ok {
		t.Fatal("key after a 1 MiB high-entropy data prefix was not considered")
	}
}

func TestGodotAdapterDiscoversKeyAfterLargeHighEntropyData(t *testing.T) {
	key := testKey(t)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	newPCK := buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	pe := makeHighEntropyTestPEWithKey(key, 1<<20)
	oldFile := appendTestPCKFooter(t, oldPCK, pe)
	newFile := appendTestPCKFooter(t, newPCK, pe)
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.exe")
	newPath := filepath.Join(dir, "new.exe")
	if err := os.WriteFile(oldPath, oldFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newFile, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := New().Build(adapter.BuildContext{Context: adapter.Context{
		OldPath: oldPath, NewPath: newPath, RelPath: "game.exe",
		OldSize: uint64(len(oldFile)), NewSize: uint64(len(newFile)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Accepted {
		t.Fatalf("expected auto key discovery after 1 MiB of high-entropy data: %#v", got)
	}
}

func makeHighEntropyTestPEWithKey(key []byte, keyOffset int) []byte {
	dataStart := 0x200
	size := keyOffset + 0x2000
	b := make([]byte, size)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	binary.LittleEndian.PutUint16(b[0x86:], 1)
	binary.LittleEndian.PutUint16(b[0x94:], 0xe0)
	sec := 0x178
	copy(b[sec:sec+8], ".rdata")
	binary.LittleEndian.PutUint32(b[sec+8:], uint32(size-dataStart))
	binary.LittleEndian.PutUint32(b[sec+12:], 0x1000)
	binary.LittleEndian.PutUint32(b[sec+16:], uint32(size-dataStart))
	binary.LittleEndian.PutUint32(b[sec+20:], uint32(dataStart))
	x := uint32(0x9e3779b9)
	for i := dataStart; i < keyOffset; i++ {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	copy(b[keyOffset:keyOffset+32], key)
	return b
}
