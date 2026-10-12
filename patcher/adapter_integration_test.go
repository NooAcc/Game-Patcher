package patcher

import (
	"os"
	"path/filepath"
	"testing"

	"game-patcher/adapter"
)

type patcherFakeAdapter struct{}

func (patcherFakeAdapter) Name() string { return "patcher-fake" }
func (patcherFakeAdapter) Match(adapter.Context) adapter.MatchResult {
	return adapter.MatchResult{Matched: true, Confidence: 100}
}
func (patcherFakeAdapter) Build(ctx adapter.BuildContext) (adapter.BuildResult, error) {
	return adapter.BuildResult{Accepted: true, Delta: &adapter.FileDelta{
		OldHash: ctx.OldHash,
		NewHash: [32]byte{1},
		OldSize: ctx.OldSize,
		NewSize: 4 + 5,
		Ops: []adapter.Op{
			{Kind: adapter.OpCopy, OldOffset: 0, Length: 4},
			{Kind: adapter.OpLiteral, Data: []byte("hello"), Length: 5},
		},
	}}, nil
}

func TestBuildChunkStepEntriesUsesAdapter(t *testing.T) {
	adapter.Reset()
	defer adapter.Reset()
	adapter.Register(patcherFakeAdapter{})

	oldDir := t.TempDir()
	newDir := t.TempDir()
	oldBytes := []byte("abcd-old")
	newBytes := []byte("hello")
	if err := os.WriteFile(filepath.Join(oldDir, "game.bin"), oldBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "game.bin"), newBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	oldHash, err := HashFile(filepath.Join(oldDir, "game.bin"))
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := HashFile(filepath.Join(newDir, "game.bin"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := []treeJob{{
		path:    "game.bin",
		action:  ActionUpdate,
		oldMeta: fileMeta{size: uint64(len(oldBytes)), hash: oldHash},
		newMeta: fileMeta{size: uint64(len(newBytes)), hash: newHash},
	}}
	pool := newBlobPool()
	entries, err := buildChunkStepEntries(oldDir, newDir, jobs, pool, adapter.Options{Values: map[string]string{
		adapter.OptionSelected: "patcher-fake",
	}})
	if err != nil {
		t.Fatalf("build entries: %v", err)
	}
	if len(entries) != 1 || len(entries[0].Ops) != 2 {
		t.Fatalf("adapter ops not used: %#v", entries)
	}
	if entries[0].Ops[0].Kind != OpCopy || entries[0].Ops[0].Length != 4 {
		t.Fatalf("first op = %#v", entries[0].Ops[0])
	}
	if entries[0].Ops[1].Kind != OpPoolRef || entries[0].Ops[1].Length != 5 {
		t.Fatalf("second op = %#v", entries[0].Ops[1])
	}
	if len(pool.blobs) != 1 || string(pool.blobs[0].Data) != "hello" {
		t.Fatalf("pool = %#v", pool.blobs)
	}
}
