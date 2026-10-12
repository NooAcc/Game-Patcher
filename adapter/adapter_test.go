package adapter

import (
	"errors"
	"testing"
)

type fakeAdapter struct {
	name       string
	match      MatchResult
	build      BuildResult
	buildErr   error
	buildCalls int
}

func (f *fakeAdapter) Name() string { return f.name }

func (f *fakeAdapter) Match(Context) MatchResult { return f.match }

func (f *fakeAdapter) Build(BuildContext) (BuildResult, error) {
	f.buildCalls++
	return f.build, f.buildErr
}

func TestRegistryMatchUsesExplicitlySelectedAdapter(t *testing.T) {
	r := NewRegistry()
	low := &fakeAdapter{name: "low", match: MatchResult{Matched: true, Confidence: 10}}
	high := &fakeAdapter{name: "high", match: MatchResult{Matched: true, Confidence: 90}}
	none := &fakeAdapter{name: "none", match: MatchResult{Matched: false, Confidence: 100}}
	r.Register(low)
	r.Register(none)
	r.Register(high)

	got := r.Match(Context{RelPath: "game.exe", Options: Options{Values: map[string]string{
		OptionSelected: "high",
	}}})
	if len(got) != 1 {
		t.Fatalf("expected only selected adapter, got %d: %#v", len(got), got)
	}
	if got[0].Adapter.Name() != "high" {
		t.Fatalf("unexpected selected adapter: %s", got[0].Adapter.Name())
	}
}

func TestRegistryBuildFallsBackWhenNoAdapterMatches(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeAdapter{name: "none", match: MatchResult{Matched: false}})

	got, err := r.Build(BuildContext{Context: Context{RelPath: "game.exe"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Accepted {
		t.Fatalf("expected fallback, got accepted result %#v", got)
	}
}

func TestRegistryBuildUsesExplicitlySelectedAdapter(t *testing.T) {
	r := NewRegistry()
	accepted := &fakeAdapter{
		name:  "accepted",
		match: MatchResult{Matched: true, Confidence: 50},
		build: BuildResult{Accepted: true, Delta: &FileDelta{NewSize: 42}},
	}
	r.Register(accepted)

	got, err := r.Build(BuildContext{Context: Context{RelPath: "game.exe", Options: Options{Values: map[string]string{
		OptionSelected: "accepted",
	}}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Accepted || got.Delta == nil || got.Delta.NewSize != 42 {
		t.Fatalf("expected accepted delta from selected adapter, got %#v", got)
	}
	if accepted.buildCalls != 1 {
		t.Fatalf("unexpected build calls: accepted=%d", accepted.buildCalls)
	}
}

func TestRegistryBuildReturnsAdapterError(t *testing.T) {
	want := errors.New("boom")
	r := NewRegistry()
	r.Register(&fakeAdapter{
		name:     "broken",
		match:    MatchResult{Matched: true, Confidence: 100},
		buildErr: want,
	})

	_, err := r.Build(BuildContext{Context: Context{RelPath: "game.exe", Options: Options{Values: map[string]string{
		OptionSelected: "broken",
	}}}})
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v", want, err)
	}
}

func TestRegistryMatchFiltersSelectedAdapter(t *testing.T) {
	r := NewRegistry()
	low := &fakeAdapter{name: "low", match: MatchResult{Matched: true, Confidence: 10}}
	high := &fakeAdapter{name: "high", match: MatchResult{Matched: true, Confidence: 90}}
	r.Register(low)
	r.Register(high)

	got := r.Match(Context{RelPath: "game.exe", Options: Options{Values: map[string]string{
		OptionSelected: "high",
	}}})
	if len(got) != 1 || got[0].Adapter.Name() != "high" {
		t.Fatalf("expected only high adapter, got %#v", got)
	}
}

func TestRegistryMatchDisablesAdaptersWhenSelectedNone(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeAdapter{name: "godot", match: MatchResult{Matched: true, Confidence: 100}})

	got := r.Match(Context{RelPath: "game.exe", Options: Options{Values: map[string]string{
		OptionSelected: SelectedNone,
	}}})
	if len(got) != 0 {
		t.Fatalf("expected no adapters when disabled, got %#v", got)
	}
}

func TestRegistryMatchEmptySelectionDisablesAdapters(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeAdapter{name: "godot", match: MatchResult{Matched: true, Confidence: 100}})

	got := r.Match(Context{RelPath: "game.exe"})
	if len(got) != 0 {
		t.Fatalf("expected no adapters without explicit selection, got %#v", got)
	}
}
