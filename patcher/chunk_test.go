package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestChunkPayloadEncodeDecodeRoundTrip(t *testing.T) {
	pool := newBlobPool()
	idxA := pool.intern([]byte("hello-pool-a"))
	idxB := pool.intern([]byte("hello-pool-b"))
	pool.intern([]byte("hello-pool-a")) // 重复内容应复用同一块

	cp := NewChunkPayload(
		[]ChainStep{{SourceIndex: 1, Entries: []Entry{{
			Path: "x.bin", Action: ActionUpdate,
			OldHash: HashBytes([]byte("old")), NewHash: HashBytes([]byte("new")),
			OldSize: 3, NewSize: 3,
			Ops: []DeltaOp{
				{Kind: OpCopy, OldOffset: 0, Length: 2},
				{Kind: OpCopyFrom, SrcPath: "other.bin", OldOffset: 5, Length: 4},
				{Kind: OpPoolRef, PoolIndex: idxA, Length: uint64(len("hello-pool-a"))},
				{Kind: OpPoolRef, PoolIndex: idxB, Length: uint64(len("hello-pool-b"))},
			},
		}}}},
		pool.blobs,
	)
	if len(pool.blobs) != 2 {
		t.Fatalf("相同内容应复用同一个块，池大小 = %d，期望 2", len(pool.blobs))
	}

	var buf bytes.Buffer
	if err := EncodeRelease(&buf, &Release{PatchVersion: 3, Payload: cp}); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRelease(buf.Bytes())
	if err != nil {
		t.Fatalf("DecodeRelease 失败: %v", err)
	}
	gc := got.Payload
	if got.PatchVersion != 3 || len(gc.Steps) != 1 || len(gc.Pool) != 2 {
		t.Fatalf("往返结果不符合预期: v%d steps=%d pool=%d", got.PatchVersion, len(gc.Steps), len(gc.Pool))
	}
	ops := gc.Steps[0].Entries[0].Ops
	if len(ops) != 4 {
		t.Fatalf("操作数量 = %d，期望 4", len(ops))
	}
	if ops[1].Kind != OpCopyFrom || ops[1].SrcPath != "other.bin" || ops[1].OldOffset != 5 || ops[1].Length != 4 {
		t.Fatalf("跨文件复制操作未正确往返: %+v", ops[1])
	}
	if ops[2].Kind != OpPoolRef || ops[2].PoolIndex != idxA || ops[2].Length != uint64(len("hello-pool-a")) {
		t.Fatalf("块池引用未正确往返: %+v", ops[2])
	}
	if !bytes.Equal(gc.Pool[idxA].Data, pool.blobs[idxA].Data) || gc.Pool[idxA].Hash != pool.blobs[idxA].Hash {
		t.Fatal("块池内容往返不一致")
	}
}

func TestChunkPayloadSelfCheckRejectsBadPoolRef(t *testing.T) {
	cp := NewChunkPayload(
		[]ChainStep{{SourceIndex: 1, Entries: []Entry{{
			Path: "x.bin", Action: ActionAdd, NewSize: 4,
			Ops: []DeltaOp{{Kind: OpPoolRef, PoolIndex: 7, Length: 4}},
		}}}},
		nil,
	)
	if err := cp.SelfCheck(); err == nil {
		t.Fatal("越界的块池引用应被拒绝")
	}

	cp2 := NewChunkPayload(
		[]ChainStep{{SourceIndex: 1, Entries: []Entry{{
			Path: "x.bin", Action: ActionAdd,
			Ops: []DeltaOp{{Kind: OpCopyFrom, SrcPath: "../escape", Length: 4}},
		}}}},
		nil,
	)
	if err := cp2.SelfCheck(); err == nil {
		t.Fatal("非法的跨文件源路径应被拒绝")
	}
}

// 同一段内"删除的文件"内容应被新增文件以跨文件复制复用：重命名大文件不再整块存储。
func TestChunkBackendReusesDeletedFileChunks(t *testing.T) {
	root := tempWorkDir(t)
	v1 := filepath.Join(root, "v1")
	v2 := filepath.Join(root, "v2")

	payloadBytes := randomBytes(120<<10, 71)
	writeTestFile(t, v1, "old_name.bin", payloadBytes)
	writeTestFile(t, v1, "keep.txt", []byte("keep"))
	writeTestFile(t, v2, "new_name.bin", payloadBytes)
	writeTestFile(t, v2, "keep.txt", []byte("keep"))

	// 被删除的 old_name.bin 提供块，新增文件直接复用，因此不会再产生字面量。
	pool := newBlobPool()
	cp := buildTestPayload(t, []string{v1, v2}, pool)

	crossCount := 0
	poolRefCount := 0
	for i := range cp.Steps {
		for j := range cp.Steps[i].Entries {
			for k := range cp.Steps[i].Entries[j].Ops {
				switch cp.Steps[i].Entries[j].Ops[k].Kind {
				case OpCopyFrom:
					crossCount++
				case OpPoolRef:
					poolRefCount++
				}
			}
		}
	}
	if crossCount == 0 {
		t.Fatal("重命名场景应产生跨文件复制操作")
	}
	t.Logf("重命名场景: 跨文件复制 %d 处, 新增字面量 %d 处, 块池 %d 块", crossCount, poolRefCount, len(cp.Pool))
	if poolRefCount != 0 {
		t.Fatalf("重命名场景不应再产生字面量，实际 %d 处", poolRefCount)
	}

	// 验证重建结果正确。
	gameDir := filepath.Join(root, "game")
	copyTree(t, v1, gameDir)
	if _, err := ApplyRelease(gameDir, filepath.Join(gameDir, backupDirName), cp.StagesFrom(0)); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	assertFilesMatch(t, gameDir, v2)
}

// 跨段重复出现的新增内容应在共享块池中只存一份。
func TestChunkBackendDedupsAcrossSteps(t *testing.T) {
	root := tempWorkDir(t)
	v1 := filepath.Join(root, "v1")
	v2 := filepath.Join(root, "v2")
	v3 := filepath.Join(root, "v3")

	shared := randomBytes(64<<10, 72)
	writeTestFile(t, v1, "keep.txt", []byte("keep"))
	writeTestFile(t, v2, "keep.txt", []byte("keep"))
	writeTestFile(t, v2, "first.bin", shared)
	writeTestFile(t, v3, "keep.txt", []byte("keep"))
	writeTestFile(t, v3, "first.bin", shared)
	writeTestFile(t, v3, "second.bin", shared)

	pool := newBlobPool()
	cp := buildTestPayload(t, []string{v1, v2, v3}, pool)

	matches := 0
	want := HashBytes(shared)
	for i := range cp.Pool {
		if cp.Pool[i].Hash == want {
			matches++
		}
	}
	if matches != 1 {
		t.Fatalf("共享内容在块池中应只出现 1 次，实际 %d 次（池大小 %d）", matches, len(cp.Pool))
	}

	gameDir := filepath.Join(root, "game")
	copyTree(t, v1, gameDir)
	if _, err := ApplyRelease(gameDir, filepath.Join(gameDir, backupDirName), cp.StagesFrom(0)); err != nil {
		t.Fatalf("跨段去重后应用失败: %v", err)
	}
	assertFilesMatch(t, gameDir, v3)
}

// 多源自动检测：v1、v2 都应能一步升到 v3。
func TestChunkBackendDetectAndApplyFromEachVersion(t *testing.T) {
	root := tempWorkDir(t)
	v1 := filepath.Join(root, "v1")
	v2 := filepath.Join(root, "v2")
	v3 := filepath.Join(root, "v3")

	writeTestFile(t, v1, "a.bin", []byte("alpha"))
	writeTestFile(t, v1, "gone.bin", []byte("only v1"))
	writeTestFile(t, v2, "a.bin", []byte("alpha beta"))
	writeTestFile(t, v2, "mid.bin", []byte("mid"))
	writeTestFile(t, v3, "a.bin", []byte("alpha beta gamma"))
	writeTestFile(t, v3, "mid.bin", []byte("mid"))
	writeTestFile(t, v3, "last.bin", []byte("only v3"))

	cp := buildTestPayload(t, []string{v1, v2, v3}, nil)
	rel := &Release{PatchVersion: 2, Payload: cp}

	for _, tc := range []struct {
		start string
		want  int
	}{{v1, 0}, {v2, 1}, {v3, 2}} {
		gameDir := filepath.Join(tempWorkDir(t), "game")
		copyTree(t, tc.start, gameDir)
		idx, _, err := DetectSource(gameDir, rel)
		if err != nil {
			t.Fatal(err)
		}
		if idx != tc.want {
			t.Fatalf("从 %s 检测到下标 %d，期望 %d", filepath.Base(tc.start), idx, tc.want)
		}
		if idx == len(cp.Sources())-1 {
			continue
		}
		if _, err := ApplyRelease(gameDir, filepath.Join(gameDir, backupDirName), cp.StagesFrom(idx)); err != nil {
			t.Fatalf("从 %s 应用失败: %v", filepath.Base(tc.start), err)
		}
		assertFilesMatch(t, gameDir, v3)
	}
}

// 通过 CreatePatchWithOptions 走完整的 EXE 打包 → 尾部读取 → 应用链路。
func TestCreatePatchExecutableLayoutChunk(t *testing.T) {
	dir := tempWorkDir(t)
	baseExe := writeTestFile(t, dir, "base.exe", randomBytes(2048, 73))
	v1 := filepath.Join(dir, "v1")
	v2 := filepath.Join(dir, "v2")
	oldData := randomBytes(80<<10, 74)
	newData := append(append([]byte(nil), oldData...), []byte("tail")...)
	writeTestFile(t, v1, "app.bin", oldData)
	writeTestFile(t, v2, "app.bin", newData)

	out := filepath.Join(dir, "update.exe")
	if err := CreatePatchWithOptions(CreatePatchOptions{
		BaseExe: baseExe, OldDir: v1, NewDir: v2, Output: out,
	}); err != nil {
		t.Fatalf("构建失败: %v", err)
	}

	rel, err := readEmbeddedRelease(out)
	if err != nil {
		t.Fatalf("读取嵌入补丁失败: %v", err)
	}
	if rel.Payload == nil || len(rel.Payload.Steps) != 1 {
		t.Fatalf("补丁结构不符合预期: %+v", rel.Payload)
	}

	gameDir := filepath.Join(dir, "game")
	copyTree(t, v1, gameDir)
	if _, err := ApplyRelease(gameDir, filepath.Join(gameDir, backupDirName), rel.Payload.StagesFrom(0)); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(gameDir, "app.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newData) {
		t.Fatal("重建内容与目标不一致")
	}
}
