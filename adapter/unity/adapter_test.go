package unity

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"game-patcher/adapter"
)

// memWriteSeeker 是内存中的可定位写入器，便于测试生成容器。
type memWriteSeeker struct {
	b   []byte
	off int64
}

func (w *memWriteSeeker) Write(p []byte) (int, error) {
	if need := w.off + int64(len(p)); need > int64(len(w.b)) {
		grown := make([]byte, need)
		copy(grown, w.b)
		w.b = grown
	}
	copy(w.b[w.off:], p)
	w.off += int64(len(p))
	return len(p), nil
}

func (w *memWriteSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.off = off
	case io.SeekCurrent:
		w.off += off
	case io.SeekEnd:
		w.off = int64(len(w.b)) + off
	}
	return w.off, nil
}

func buildContainer(t *testing.T, stream []byte, blockSize int, nodes []nodeInfo) []byte {
	t.Helper()
	m := &templateMeta{
		version:       8,
		unityVersion:  "5.x.x",
		unityRevision: "2022.3.62f2",
		flags:         0x243,
		blockSize:     blockSize,
		streamSize:    int64(len(stream)),
		nodes:         nodes,
	}
	w := &memWriteSeeker{}
	if err := encodeContainer(w, bytes.NewReader(stream), m); err != nil {
		t.Fatalf("encodeContainer: %v", err)
	}
	return w.b
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readAll(t *testing.T, df adapter.DecodedFile) []byte {
	t.Helper()
	buf := make([]byte, df.Size())
	if _, err := df.ReadAt(buf, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	return buf
}

func TestContainerRoundTrip(t *testing.T) {
	stream := bytes.Repeat([]byte("unityfs-block-payload-"), 40000)
	nodes := []nodeInfo{
		{offset: 0, size: 200000, flags: 4, path: "level0"},
		{offset: 200000, size: int64(len(stream)) - 200000, flags: 0, path: "level0.resS"},
	}
	data := buildContainer(t, stream, 131072, nodes)

	path := writeTemp(t, "data.unity3d", data)
	c, err := openContainer(path)
	if err != nil {
		t.Fatalf("openContainer: %v", err)
	}
	defer c.Close()

	if c.version != 8 || c.unityRevision != "2022.3.62f2" {
		t.Fatalf("header mismatch: %+v", c)
	}
	if len(c.nodes) != 2 || c.nodes[0].path != "level0" {
		t.Fatalf("node mismatch: %+v", c.nodes)
	}
	got := readAll(t, c.decoded())
	if !bytes.Equal(got, stream) {
		t.Fatalf("decoded stream mismatch: got %d bytes", len(got))
	}
}

func TestAdapterMatchAndBuild(t *testing.T) {
	oldStream := bytes.Repeat([]byte("AAAA-old-content-"), 20000)
	newStream := append(append([]byte{}, oldStream[:50000]...), []byte("BRAND-NEW-DATA")...)
	newStream = append(newStream, oldStream[50000:]...)

	oldNodes := []nodeInfo{{offset: 0, size: int64(len(oldStream)), flags: 4, path: "level0"}}
	newNodes := []nodeInfo{{offset: 0, size: int64(len(newStream)), flags: 4, path: "level0"}}

	oldPath := writeTemp(t, "old.unity3d", buildContainer(t, oldStream, 65536, oldNodes))
	newPath := writeTemp(t, "new.unity3d", buildContainer(t, newStream, 65536, newNodes))

	a := &Adapter{}
	m := a.Match(adapter.Context{OldPath: oldPath, NewPath: newPath})
	if !m.Matched {
		t.Fatalf("expected match, got %+v", m)
	}

	res, err := a.Build(adapter.BuildContext{Context: adapter.Context{
		OldPath: oldPath, NewPath: newPath, RelPath: "data.unity3d",
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !res.Accepted || res.Rewrite == nil {
		t.Fatalf("expected accepted rewrite, got %+v", res)
	}

	df, err := a.Decode(newPath)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	defer df.Close()
	if got := readAll(t, df); !bytes.Equal(got, newStream) {
		t.Fatalf("decoded new stream mismatch")
	}
}

func TestAdapterRejectsNonUnityFS(t *testing.T) {
	oldPath := writeTemp(t, "a.bin", []byte("not a unity container at all"))
	newPath := writeTemp(t, "b.bin", []byte("also not a unity container"))
	a := &Adapter{}
	if m := a.Match(adapter.Context{OldPath: oldPath, NewPath: newPath}); m.Matched {
		t.Fatalf("expected no match, got %+v", m)
	}
}

func TestAdapterFallsBackOnUnsupportedVersion(t *testing.T) {
	// 构造一个版本号过低的头部：Match 必须拒绝，交给通用 CDC。
	head := append([]byte(signature), 0, 0, 0, 6)
	head = append(head, []byte("5.x.x\x00")...)
	head = append(head, []byte("2017.4.0f1\x00")...)
	head = append(head, make([]byte, 20)...)
	path := writeTemp(t, "old.unity3d", head)
	a := &Adapter{}
	if m := a.Match(adapter.Context{OldPath: path, NewPath: path}); m.Matched {
		t.Fatalf("expected unsupported version to be rejected")
	}
}
