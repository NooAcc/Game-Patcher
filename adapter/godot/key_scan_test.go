package godot

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"game-patcher/adapter"
)

func TestGodotAdapterDiscoversKeyBeyondFirst4KWindows(t *testing.T) {
	key := testKey(t)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	newPCK := buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	pe := makeTestPELargeWithKey(key)
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
		t.Fatalf("expected key beyond 4 KiB of windows to be discovered: %#v", got)
	}
}

func TestGodotAdapterDoesNotEmbedDiscoveredKey(t *testing.T) {
	key := testKey(t)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	newPCK := buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	pe := makeTestPELargeWithKey(key)
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
	if !got.Accepted || got.Delta == nil {
		t.Fatalf("expected accepted delta: %#v", got)
	}
	for _, op := range got.Delta.Ops {
		if bytes.Contains(op.Data, key) {
			t.Fatal("discovered key leaked into patch literals")
		}
	}
}

func makeTestPELargeWithKey(key []byte) []byte {
	b := make([]byte, 0x6000)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	binary.LittleEndian.PutUint16(b[0x86:], 1)
	binary.LittleEndian.PutUint16(b[0x94:], 0xe0)
	sec := 0x178
	copy(b[sec:sec+8], ".rdata")
	binary.LittleEndian.PutUint32(b[sec+8:], 0x5000) // VirtualSize
	binary.LittleEndian.PutUint32(b[sec+12:], 0x1000)
	binary.LittleEndian.PutUint32(b[sec+16:], 0x5000) // SizeOfRawData
	binary.LittleEndian.PutUint32(b[sec+20:], 0x200)  // PointerToRawData
	copy(b[0x4000:0x4020], key)
	return b
}
