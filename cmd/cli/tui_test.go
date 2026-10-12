package main

import (
	"bufio"
	"io"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"game-patcher/adapter"
)

func TestWizardValidatesOldDirectoryAndAdvances(t *testing.T) {
	oldDir := tempWorkDir(t)
	m := newWizardModel(filepath.Join("tools", "game-patcher-cli.exe"))
	m.oldInput.SetValue(oldDir)

	next, _ := m.submit()
	if next.step != stepNewPath {
		t.Fatalf("step = %v, want stepNewPath", next.step)
	}
	want, _ := filepath.Abs(oldDir)
	if next.oldPath != want {
		t.Fatalf("oldPath = %q, want %q", next.oldPath, want)
	}
	if next.err != "" {
		t.Fatalf("unexpected error: %s", next.err)
	}
}

func TestWizardShowsValidationErrorForMissingOldDirectory(t *testing.T) {
	m := newWizardModel(filepath.Join("tools", "game-patcher-cli.exe"))
	m.oldInput.SetValue(filepath.Join(t.TempDir(), "missing"))

	next, _ := m.submit()
	if next.step != stepOldPath {
		t.Fatalf("step = %v, want stepOldPath", next.step)
	}
	if !strings.Contains(next.err, "路径不存在") {
		t.Fatalf("error = %q, want path-not-found message", next.err)
	}
}

func TestWizardSkipsGodotKeyWhenAdaptersDisabled(t *testing.T) {
	m := wizardAtAdapterStep(t)
	m.adapterList.Select(0)

	next, _ := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next.step != stepOutput {
		t.Fatalf("step = %v, want stepOutput", next.step)
	}
	if next.adapterName != adapter.SelectedNone {
		t.Fatalf("adapterName = %q, want none", next.adapterName)
	}
}

func TestWizardRequestsGodotKeyWhenGodotSelected(t *testing.T) {
	m := wizardAtAdapterStep(t)
	m.adapterList.Select(1)

	next, _ := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next.step != stepGodotKey {
		t.Fatalf("step = %v, want stepGodotKey", next.step)
	}
	if next.adapterName != "godot" {
		t.Fatalf("adapterName = %q, want godot", next.adapterName)
	}
	next.keyInput.SetValue("01293b8d0b5d4aec182f82456c5e3c2269702c1b527648c4a5c274232b40ad23")
	next, _ = next.submit()
	if next.step != stepOutput {
		t.Fatalf("step = %v, want stepOutput", next.step)
	}
	if got := next.config().GodotKey; got != "01293b8d0b5d4aec182f82456c5e3c2269702c1b527648c4a5c274232b40ad23" {
		t.Fatalf("godot key = %q", got)
	}
}

func TestWizardUsesDefaultOutputPath(t *testing.T) {
	selfPath := filepath.Join("tools", "game-patcher-cli.exe")
	m := wizardAtOutputStep(t, selfPath)
	m.outputInput.SetValue("")

	next, _ := m.submit()
	want := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	if next.output != want {
		t.Fatalf("output = %q, want %q", next.output, want)
	}
	if next.step != stepConfirm {
		t.Fatalf("step = %v, want stepConfirm", next.step)
	}
}

func TestWizardEscFromOutputReturnsToAdapter(t *testing.T) {
	m := wizardAtOutputStep(t, filepath.Join("tools", "game-patcher-cli.exe"))
	m.adapterName = adapter.SelectedNone

	next, _ := m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.step != stepAdapter {
		t.Fatalf("step = %v, want stepAdapter", next.step)
	}
}

func wizardAtAdapterStep(t *testing.T) wizardModel {
	t.Helper()
	oldDir := tempWorkDir(t)
	newDir := tempWorkDir(t)
	m := newWizardModel(filepath.Join("tools", "game-patcher-cli.exe"))
	m.oldInput.SetValue(oldDir)
	m, _ = m.submit()
	m.newInput.SetValue(newDir)
	m, _ = m.submit()
	if m.step != stepPrevPatches {
		t.Fatalf("step = %v, want stepPrevPatches", m.step)
	}
	m, _ = m.submit()
	if m.step != stepAdapter {
		t.Fatalf("step = %v, want stepAdapter; old=%q focused=%v new=%q err=%q", m.step, m.oldInput.Value(), m.oldInput.Focused(), m.newInput.Value(), m.err)
	}
	return m
}

func wizardAtOutputStep(t *testing.T, selfPath string) wizardModel {
	t.Helper()
	m := wizardAtAdapterStep(t)
	m.adapterList.Select(0)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != stepOutput {
		t.Fatalf("step = %v, want stepOutput", m.step)
	}
	m.selfPath = selfPath
	return m
}

func TestAdapterMenuItemsPreferDisabledDefault(t *testing.T) {
	items := adapterMenuItems()
	if len(items) < 2 {
		t.Fatalf("menu items = %#v", items)
	}
	if items[0].Value != adapter.SelectedNone {
		t.Fatalf("first item = %#v, want none", items[0])
	}
	foundGodot := false
	for _, item := range items {
		if item.Value == "godot" {
			foundGodot = true
		}
		if item.Value == "auto" {
			t.Fatalf("auto must not appear in menu: %#v", items)
		}
	}
	if !foundGodot {
		t.Fatalf("godot missing from menu: %#v", items)
	}
}

func TestPlainInteractiveConfigDisablesAdapters(t *testing.T) {
	oldDir := tempWorkDir(t)
	newDir := tempWorkDir(t)
	selfPath := filepath.Join("tools", "game-patcher-cli.exe")
	input := oldDir + "\n" + newDir + "\n\n\n"

	r := bufio.NewReader(strings.NewReader(input))
	cfg, err := collectInteractiveConfigPlain(r, selfPath)
	if err != nil {
		t.Fatalf("collectInteractiveConfigPlain: %v", err)
	}
	if cfg.OldPath == "" || cfg.NewPath == "" {
		t.Fatalf("missing paths: %#v", cfg)
	}
	if cfg.Adapter != adapter.SelectedNone {
		t.Fatalf("adapter = %q, want none", cfg.Adapter)
	}
	wantOut := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	if cfg.Output != wantOut {
		t.Fatalf("output = %q, want %q", cfg.Output, wantOut)
	}
}

func TestWizardScriptedGodotFlow(t *testing.T) {
	oldDir := tempWorkDir(t)
	newDir := tempWorkDir(t)
	outPath := filepath.Join(t.TempDir(), "update.exe")
	key := "01293b8d0b5d4aec182f82456c5e3c2269702c1b527648c4a5c274232b40ad23"

	m := newWizardModel(filepath.Join("tools", "game-patcher-cli.exe"))
	m = sendWizardText(m, oldDir)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	m = sendWizardText(m, newDir)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != stepAdapter {
		t.Fatalf("step = %v, want stepAdapter; old=%q focused=%v new=%q err=%q", m.step, m.oldInput.Value(), m.oldInput.Focused(), m.newInput.Value(), m.err)
	}
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != stepGodotKey {
		t.Fatalf("step = %v, want stepGodotKey", m.step)
	}
	m = sendWizardText(m, key)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	m = sendWizardText(m, outPath)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.step != stepConfirm {
		t.Fatalf("step = %v, want stepConfirm", m.step)
	}
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.done {
		t.Fatal("wizard did not finish")
	}
	cfg := m.config()
	if cfg.Adapter != "godot" || cfg.GodotKey != key || cfg.Output != outPath {
		t.Fatalf("config = %#v", cfg)
	}
}

func sendWizardText(m wizardModel, text string) wizardModel {
	for _, r := range text {
		m, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestWizardBubbleTeaProgramWithScriptedInput(t *testing.T) {
	oldDir := tempWorkDir(t)
	newDir := tempWorkDir(t)
	outPath := filepath.Join(t.TempDir(), "update.exe")
	key := "01293b8d0b5d4aec182f82456c5e3c2269702c1b527648c4a5c274232b40ad23"
	input := strings.Join([]string{
		oldDir + "\r",
		newDir + "\r",
		"\r",
		"\x1b[B\r",
		key + "\r",
		outPath + "\r",
		"\r",
	}, "")

	m := newWizardModel(filepath.Join("tools", "game-patcher-cli.exe"))
	final, err := runWizardProgram(m, tea.WithInput(strings.NewReader(input)), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	if err != nil {
		t.Fatalf("runWizardProgram: %v", err)
	}
	cfg := final.config()
	if !final.done || cfg.Adapter != "godot" || cfg.GodotKey != key || cfg.Output != outPath {
		t.Fatalf("final=%#v config=%#v", final, cfg)
	}
}
