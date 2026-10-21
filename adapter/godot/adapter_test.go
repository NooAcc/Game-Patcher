package godot

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"game-patcher/adapter"
)

func align16(n int) int {
	if n%16 == 0 {
		return n
	}
	return n + 16 - n%16
}

func encryptBlob(t *testing.T, key, plain []byte, iv []byte) []byte {
	t.Helper()
	digest := md5.Sum(plain)
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
	var out bytes.Buffer
	out.Write(digest[:])
	_ = binary.Write(&out, binary.LittleEndian, uint64(len(plain)))
	out.Write(iv)
	out.Write(ct)
	return out.Bytes()
}

type fileSpec struct {
	path string
	data []byte
}

func buildOldV4Encrypted(t *testing.T, key []byte, files []fileSpec) []byte {
	t.Helper()
	fileBase := 0x70
	var data bytes.Buffer
	dirs := make([]struct {
		path string
		ofs  uint64
		size uint64
		sum  [16]byte
		blob []byte
	}, len(files))
	for i, f := range files {
		iv := []byte{byte(i), byte(i + 1), byte(i + 2), byte(i + 3), 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
		blob := encryptBlob(t, key, f.data, iv)
		dirs[i].path = f.path
		dirs[i].ofs = uint64(data.Len())
		dirs[i].size = uint64(len(f.data))
		dirs[i].sum = md5.Sum(f.data)
		dirs[i].blob = blob
		data.Write(blob)
	}
	var dir bytes.Buffer
	for _, d := range dirs {
		appendEntry(t, &dir, d.path, d.ofs, d.size, d.sum, packFileEncrypted)
	}
	dirOffset := fileBase + data.Len()
	// encrypted directory: file_count 明文 + md5 + length + iv + ciphertext
	var enc bytes.Buffer
	sum := md5.Sum(dir.Bytes())
	enc.Write(sum[:])
	_ = binary.Write(&enc, binary.LittleEndian, uint64(dir.Len()))
	dirIV := []byte("fedcba9876543210")
	enc.Write(dirIV)
	paddedDir := append([]byte(nil), dir.Bytes()...)
	for len(paddedDir)%16 != 0 {
		paddedDir = append(paddedDir, 0)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes: %v", err)
	}
	ct := make([]byte, len(paddedDir))
	cipher.NewCFBEncrypter(block, dirIV).XORKeyStream(ct, paddedDir)
	enc.Write(ct)

	var out bytes.Buffer
	out.WriteString("GDPC")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4))
	_ = binary.Write(&out, binary.LittleEndian, uint32(4))
	_ = binary.Write(&out, binary.LittleEndian, uint32(7))
	_ = binary.Write(&out, binary.LittleEndian, uint32(2))
	_ = binary.Write(&out, binary.LittleEndian, uint32(packFlagDirEncrypted|packFlagRelFileBase))
	_ = binary.Write(&out, binary.LittleEndian, uint64(fileBase))
	_ = binary.Write(&out, binary.LittleEndian, uint64(dirOffset))
	for out.Len() < fileBase {
		out.WriteByte(0)
	}
	out.Write(data.Bytes())
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(files)))
	out.Write(enc.Bytes())
	return out.Bytes()
}

func buildNewV2Plain(t *testing.T, files []fileSpec) []byte {
	t.Helper()
	dirStart := 0x60
	var dir bytes.Buffer
	var dataOff uint64
	for _, f := range files {
		appendEntry(t, &dir, f.path, dataOff, uint64(len(f.data)), md5.Sum(f.data), 0)
		dataOff += uint64(len(f.data))
	}
	fileBase := align16(dirStart + 4 + dir.Len())
	var out bytes.Buffer
	out.WriteString("GDPC")
	_ = binary.Write(&out, binary.LittleEndian, uint32(2))
	_ = binary.Write(&out, binary.LittleEndian, uint32(4))
	_ = binary.Write(&out, binary.LittleEndian, uint32(7))
	_ = binary.Write(&out, binary.LittleEndian, uint32(2))
	_ = binary.Write(&out, binary.LittleEndian, uint32(packFlagRelFileBase))
	_ = binary.Write(&out, binary.LittleEndian, uint64(fileBase))
	for i := 0; i < 16; i++ {
		_ = binary.Write(&out, binary.LittleEndian, uint32(0))
	}
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(files)))
	out.Write(dir.Bytes())
	for out.Len() < fileBase {
		out.WriteByte(0)
	}
	for _, f := range files {
		out.Write(f.data)
	}
	return out.Bytes()
}

func applyOpsForTest(t *testing.T, old []byte, ops []adapter.Op) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, op := range ops {
		switch op.Kind {
		case adapter.OpCopy:
			if op.OldOffset+op.Length > uint64(len(old)) {
				t.Fatalf("copy out of range: %#v old=%d", op, len(old))
			}
			out.Write(old[op.OldOffset : op.OldOffset+op.Length])
		case adapter.OpLiteral:
			if uint64(len(op.Data)) != op.Length {
				t.Fatalf("literal length mismatch: op=%#v data=%d", op, len(op.Data))
			}
			out.Write(op.Data)
		default:
			t.Fatalf("unexpected op kind %d", op.Kind)
		}
	}
	return out.Bytes()
}

func TestGodotAdapterBuildReusesUnchangedFiles(t *testing.T) {
	key := testKey(t)
	oldFiles := []fileSpec{
		{path: "a.txt", data: []byte("hello")},
		{path: "b.txt", data: []byte("world")},
	}
	newFiles := []fileSpec{
		{path: "a.txt", data: []byte("hello")},
		{path: "b.txt", data: []byte("WORLD")},
		{path: "c.txt", data: []byte("new")},
	}
	prefix := []byte("MZ-test-prefix")
	oldFile := append(append([]byte(nil), prefix...), buildOldV4Encrypted(t, key, oldFiles)...)
	newFile := append(append([]byte(nil), prefix...), buildNewV2Plain(t, newFiles)...)

	dir := t.TempDir()
	oldPath := filepath.Join(dir, "game.exe")
	newPath := filepath.Join(dir, "game2.exe")
	if err := os.WriteFile(oldPath, oldFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newFile, 0o644); err != nil {
		t.Fatal(err)
	}

	a := New()
	got, err := a.Build(adapter.BuildContext{Context: adapter.Context{
		OldPath: oldPath,
		NewPath: newPath,
		RelPath: "game.exe",
		OldSize: uint64(len(oldFile)),
		NewSize: uint64(len(newFile)),
		Options: adapter.Options{Values: map[string]string{"godot.key": testKeyHex}},
	}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !got.Accepted || got.Delta == nil {
		t.Fatalf("expected accepted delta, got %#v", got)
	}
	out := applyOpsForTest(t, oldFile, got.Delta.Ops)
	pck, err := parsePCK(out, key)
	if err != nil {
		t.Fatalf("parse generated pck: %v", err)
	}
	if len(pck.Entries) != 3 {
		t.Fatalf("entries=%d, want 3", len(pck.Entries))
	}
	byPath := map[string]pckEntry{}
	for _, e := range pck.Entries {
		byPath[e.Path] = e
	}
	if byPath["a.txt"].Flags&packFileEncrypted == 0 {
		t.Fatalf("a.txt should reuse encrypted old blob: %#v", byPath["a.txt"])
	}
	if byPath["b.txt"].Flags&packFileEncrypted != 0 || byPath["c.txt"].Flags&packFileEncrypted != 0 {
		t.Fatalf("changed/new files should be literal plaintext: %#v %#v", byPath["b.txt"], byPath["c.txt"])
	}
	// 校验逻辑内容：解析后的 MD5 必须与新版目录一致。
	var newPCK *pckFile
	newPCK, err = parsePCK(newFile, nil)
	if err != nil {
		t.Fatalf("parse new pck: %v", err)
	}
	newByPath := map[string]pckEntry{}
	for _, e := range newPCK.Entries {
		newByPath[e.Path] = e
	}
	for p, e := range byPath {
		if e.MD5 != newByPath[p].MD5 {
			t.Fatalf("logical md5 mismatch for %s: got %x want %x", p, e.MD5, newByPath[p].MD5)
		}
	}
}

func TestGodotAdapterBuildFallsBackWhenBarePCKHasNoDiscoverableKey(t *testing.T) {
	key := testKey(t)
	oldFile := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	newFile := buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.pck")
	newPath := filepath.Join(dir, "new.pck")
	if err := os.WriteFile(oldPath, oldFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newFile, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := New().Build(adapter.BuildContext{Context: adapter.Context{
		OldPath: oldPath, NewPath: newPath, RelPath: "game.pck",
		OldSize: uint64(len(oldFile)), NewSize: uint64(len(newFile)),
	}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Accepted {
		t.Fatalf("expected fallback when a bare encrypted PCK has no discoverable key, got %#v", got)
	}
	if got.Reason == "" {
		t.Fatal("fallback reason should explain that key discovery failed")
	}
}

func TestGodotAdapterBuildFallsBackForNonPCK(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.bin")
	newPath := filepath.Join(dir, "new.bin")
	if err := os.WriteFile(oldPath, []byte("not a pck"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("still not a pck"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := New().Build(adapter.BuildContext{Context: adapter.Context{
		OldPath: oldPath, NewPath: newPath, RelPath: "old.bin",
		OldSize: 9, NewSize: 15,
	}})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got.Accepted {
		t.Fatalf("expected fallback for non-pck, got %#v", got)
	}
}

func TestGodotAdapterDescription(t *testing.T) {
	d, ok := New().(adapter.Described)
	if !ok || d.Description() == "" {
		t.Fatal("godot adapter should implement Described")
	}
}

func TestGodotAdapterBuildsEmbeddedFooterPCK(t *testing.T) {
	oldFile := appendTestPCKFooter(t, buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("hello")}}), []byte("MZ-fake-exe-prefix"))
	newFile := appendTestPCKFooter(t, buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("WORLD")}}), []byte("MZ-fake-exe-prefix"))
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
		t.Fatalf("embedded PCK should be accepted: %#v", got)
	}
	out := applyOpsForTest(t, oldFile, got.Delta.Ops)
	pck, err := parsePCK(out, nil)
	if err != nil {
		t.Fatalf("parse rebuilt embedded PCK: %v", err)
	}
	if len(pck.Entries) != 1 || pck.Entries[0].Path != "a.txt" || string(out[int(pck.Start)+int(pck.FileBase):int(pck.Start)+int(pck.FileBase)+5]) != "WORLD" {
		t.Fatal("embedded PCK logical content mismatch")
	}
	if len(pck.Trailer) < 12 || string(pck.Trailer[len(pck.Trailer)-4:]) != pckMagic {
		t.Fatalf("embedded trailer not refreshed: %#v", pck.Trailer)
	}
	declared := binary.LittleEndian.Uint64(pck.Trailer[len(pck.Trailer)-12 : len(pck.Trailer)-4])
	if declared != uint64(pck.End-pck.Start) {
		t.Fatalf("embedded trailer declares %d, want %d", declared, pck.End-pck.Start)
	}
}

func TestGodotAdapterDiscoversKeyFromPE(t *testing.T) {
	key := testKey(t)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	newPCK := buildNewV2Plain(t, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	pe := makeTestPEWithKey(key)
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
		t.Fatalf("expected auto key discovery: %#v", got)
	}
}

func TestGodotAdapterRejectsEncryptedNewV2Directory(t *testing.T) {
	key := testKey(t)
	oldPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	newPCK := buildOldV4Encrypted(t, key, []fileSpec{{path: "a.txt", data: []byte("hello")}})
	pe := makeTestPEWithKey(key)
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
		Options: adapter.Options{Values: map[string]string{"godot.key": testKeyHex}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted {
		t.Fatalf("v4 new PCK must fall back: %#v", got)
	}
	if got.Reason == "" {
		t.Fatal("fallback reason should explain unsupported rewrite")
	}
}

func appendTestPCKFooter(t *testing.T, pck, prefix []byte) []byte {
	t.Helper()
	out := append(append([]byte(nil), prefix...), pck...)
	var footer [12]byte
	binary.LittleEndian.PutUint64(footer[:8], uint64(len(pck)))
	copy(footer[8:], "GDPC")
	return append(out, footer[:]...)
}

// makeTestPEWithKey 构造最小可解析 PE，密钥放在可读数据 section 中。
func makeTestPEWithKey(key []byte) []byte {
	b := make([]byte, 0x400)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	binary.LittleEndian.PutUint16(b[0x86:], 1)
	binary.LittleEndian.PutUint16(b[0x94:], 0xe0)
	sec := 0x178
	copy(b[sec:sec+8], ".rdata")
	binary.LittleEndian.PutUint32(b[sec+8:], 0x1000)  // VirtualSize
	binary.LittleEndian.PutUint32(b[sec+12:], 0x1000) // VirtualAddress
	binary.LittleEndian.PutUint32(b[sec+16:], 0x200)  // SizeOfRawData
	binary.LittleEndian.PutUint32(b[sec+20:], 0x200)  // PointerToRawData
	copy(b[0x200:0x200+32], key)
	return b
}
