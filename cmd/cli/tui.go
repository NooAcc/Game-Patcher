package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"game-patcher/adapter"
	"game-patcher/internal/cliutil"
	"game-patcher/patcher"
)

type wizardStep int

const (
	stepOldPath wizardStep = iota
	stepNewPath
	stepPrevPatches
	stepAdapter
	stepGodotKey
	stepOutput
	stepConfirm
)

type interactiveConfig struct {
	OldPath   string
	NewPath   string
	PrevPaths []string
	Adapter   string
	GodotKey  string
	Output    string
}

type wizardModel struct {
	selfPath string
	step     wizardStep
	width    int
	height   int
	err      string
	done     bool
	cancel   bool

	oldInput    textinput.Model
	newInput    textinput.Model
	prevInput   textinput.Model
	keyInput    textinput.Model
	outputInput textinput.Model
	adapterList list.Model

	oldPath       string
	newPath       string
	prevPaths     []string
	adapterName   string
	adapterChosen bool
	godotKey      string
	output        string
}

type adapterListItem struct {
	label       string
	value       string
	description string
}

func (i adapterListItem) Title() string       { return i.label }
func (i adapterListItem) Description() string { return i.description }
func (i adapterListItem) FilterValue() string { return i.label }

var (
	tuiAccent = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#C4B5FD"}
	tuiCyan   = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	tuiGreen  = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#86EFAC"}
	tuiRed    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"}
	tuiMuted  = lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"}
	tuiBorder = lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#334155"}

	tuiTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiAccent)
	tuiSubtitleStyle = lipgloss.NewStyle().
				Foreground(tuiMuted)
	tuiBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(tuiBorder).
			Padding(1, 2)
	tuiLabelStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiCyan)
	tuiSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(tuiAccent)
	tuiMutedStyle = lipgloss.NewStyle().
			Foreground(tuiMuted)
	tuiSuccessStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiGreen)
	tuiErrorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(tuiRed)
)

func newWizardModel(selfPath string) wizardModel {
	oldInput := textinput.New()
	oldInput.Placeholder = `例如 D:\Games\MyGame\v1.0`
	oldInput.Prompt = "› "
	oldInput.CharLimit = 4096

	newInput := textinput.New()
	newInput.Placeholder = `例如 D:\Games\MyGame\v1.1`
	newInput.Prompt = "› "
	newInput.CharLimit = 4096

	prevInput := textinput.New()
	prevInput.Placeholder = "留空继续；输入补丁文件路径后按 Enter 添加"
	prevInput.Prompt = "› "
	prevInput.CharLimit = 4096

	keyInput := textinput.New()
	keyInput.Placeholder = "64 位 hex key 或包含 key 的文件路径（留空跳过）"
	keyInput.Prompt = "› "
	keyInput.CharLimit = 4096

	defaultOutput := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	outputInput := textinput.New()
	outputInput.Placeholder = defaultOutput
	outputInput.Prompt = "› "
	outputInput.CharLimit = 4096

	adapterItems := adapterMenuItems()
	items := make([]list.Item, 0, len(adapterItems))
	for _, item := range adapterItems {
		items = append(items, adapterListItem{
			label:       item.Label,
			value:       item.Value,
			description: adapterItemDescription(item.Value),
		})
	}
	adapterList := list.New(items, list.NewDefaultDelegate(), 72, 10)
	adapterList.Title = "选择适配器"
	adapterList.SetShowTitle(false)
	adapterList.SetShowStatusBar(false)
	adapterList.SetShowPagination(false)
	adapterList.SetShowHelp(false)
	adapterList.SetFilteringEnabled(false)
	adapterList.DisableQuitKeybindings()
	adapterList.Select(0)

	m := wizardModel{
		selfPath:    selfPath,
		step:        stepOldPath,
		width:       90,
		height:      30,
		oldInput:    oldInput,
		newInput:    newInput,
		prevInput:   prevInput,
		keyInput:    keyInput,
		outputInput: outputInput,
		adapterList: adapterList,
		adapterName: adapter.SelectedNone,
		output:      defaultOutput,
	}
	m.focusStep()
	m.resize()
	return m
}

func adapterItemDescription(value string) string {
	if value == adapter.SelectedNone {
		return "使用通用 CDC 字节差分；不会自动猜测引擎"
	}
	for _, a := range adapter.DefaultRegistry.Adapters() {
		if a.Name() == value {
			return adapter.DisplayName(a)
		}
	}
	return "已注册适配器"
}

func (m *wizardModel) resize() {
	width := m.width - 12
	if width < 40 {
		width = 40
	}
	if width > 88 {
		width = 88
	}
	for _, input := range []*textinput.Model{&m.oldInput, &m.newInput, &m.prevInput, &m.keyInput, &m.outputInput} {
		input.Width = width
	}
	m.adapterList.SetSize(width, 8)
}

func (m *wizardModel) focusStep() {
	m.oldInput.Blur()
	m.newInput.Blur()
	m.prevInput.Blur()
	m.keyInput.Blur()
	m.outputInput.Blur()
	switch m.step {
	case stepOldPath:
		m.oldInput.Focus()
	case stepNewPath:
		m.newInput.Focus()
	case stepPrevPatches:
		m.prevInput.Focus()
	case stepGodotKey:
		m.keyInput.Focus()
	case stepOutput:
		m.outputInput.Focus()
	}
}

func (m wizardModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m wizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next, cmd
}

func (m wizardModel) update(msg tea.Msg) (wizardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancel = true
			return m, tea.Quit
		case "esc":
			return m.goBack()
		case "enter":
			if m.step == stepAdapter {
				return m.chooseAdapter()
			}
			return m.submit()
		}
	}

	switch m.step {
	case stepAdapter:
		var cmd tea.Cmd
		m.adapterList, cmd = m.adapterList.Update(msg)
		return m, cmd
	case stepOldPath:
		var cmd tea.Cmd
		m.oldInput, cmd = m.oldInput.Update(msg)
		return m, cmd
	case stepNewPath:
		var cmd tea.Cmd
		m.newInput, cmd = m.newInput.Update(msg)
		return m, cmd
	case stepPrevPatches:
		var cmd tea.Cmd
		m.prevInput, cmd = m.prevInput.Update(msg)
		return m, cmd
	case stepGodotKey:
		var cmd tea.Cmd
		m.keyInput, cmd = m.keyInput.Update(msg)
		return m, cmd
	case stepOutput:
		var cmd tea.Cmd
		m.outputInput, cmd = m.outputInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m wizardModel) submit() (wizardModel, tea.Cmd) {
	switch m.step {
	case stepOldPath:
		path, err := resolveWizardDir(m.oldInput.Value(), "旧版本")
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.oldPath = path
		m.err = ""
		m.step = stepNewPath
	case stepNewPath:
		path, err := resolveWizardDir(m.newInput.Value(), "新版本")
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.newPath = path
		m.err = ""
		m.step = stepPrevPatches
	case stepPrevPatches:
		raw := cliutil.CleanPath(m.prevInput.Value())
		if raw == "" {
			m.err = ""
			m.step = stepAdapter
			m.focusStep()
			return m, nil
		}
		path, err := resolveWizardFile(raw, "旧补丁")
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.prevPaths = append(m.prevPaths, path)
		m.prevInput.SetValue("")
		m.err = ""
		return m, nil
	case stepGodotKey:
		key, err := resolveWizardGodotKey(m.keyInput.Value())
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.godotKey = key
		m.err = ""
		m.step = stepOutput
	case stepOutput:
		output, err := resolveWizardOutput(m.outputInput.Value(), m.selfPath)
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.output = output
		m.err = ""
		m.step = stepConfirm
	case stepConfirm:
		m.done = true
		return m, tea.Quit
	}
	m.focusStep()
	return m, nil
}

func (m wizardModel) chooseAdapter() (wizardModel, tea.Cmd) {
	item, ok := m.adapterList.SelectedItem().(adapterListItem)
	if !ok {
		m.err = "未选择适配器"
		return m, nil
	}
	m.adapterName = item.value
	m.adapterChosen = true
	m.err = ""
	if item.value == "godot" {
		m.step = stepGodotKey
	} else {
		m.godotKey = ""
		m.step = stepOutput
	}
	m.focusStep()
	return m, nil
}

func (m wizardModel) goBack() (wizardModel, tea.Cmd) {
	m.err = ""
	switch m.step {
	case stepOldPath:
		m.cancel = true
		return m, tea.Quit
	case stepNewPath:
		m.step = stepOldPath
	case stepPrevPatches:
		m.step = stepNewPath
	case stepAdapter:
		m.step = stepPrevPatches
	case stepGodotKey:
		m.step = stepAdapter
	case stepOutput:
		if m.adapterName == "godot" {
			m.step = stepGodotKey
		} else {
			m.step = stepAdapter
		}
	case stepConfirm:
		m.step = stepOutput
	}
	m.focusStep()
	return m, nil
}

func (m wizardModel) config() interactiveConfig {
	return interactiveConfig{
		OldPath:   m.oldPath,
		NewPath:   m.newPath,
		PrevPaths: append([]string(nil), m.prevPaths...),
		Adapter:   m.adapterName,
		GodotKey:  m.godotKey,
		Output:    m.output,
	}
}

func resolveWizardDir(raw, label string) (string, error) {
	raw = cliutil.CleanPath(raw)
	if raw == "" {
		return "", fmt.Errorf("%s文件夹不能为空", label)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("%s路径无效: %w", label, err)
	}
	info, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("%s路径不存在: %s", label, abs)
	}
	if err != nil {
		return "", fmt.Errorf("%s路径无效: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s必须是文件夹: %s", label, abs)
	}
	return abs, nil
}

func resolveWizardFile(raw, label string) (string, error) {
	raw = cliutil.CleanPath(raw)
	if raw == "" {
		return "", fmt.Errorf("%s路径不能为空", label)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("%s路径无效: %w", label, err)
	}
	info, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("%s文件不存在: %s", label, abs)
	}
	if err != nil {
		return "", fmt.Errorf("%s路径无效: %w", label, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s必须是文件: %s", label, abs)
	}
	return abs, nil
}

func resolveWizardOutput(raw, selfPath string) (string, error) {
	defaultOut := filepath.Join(filepath.Dir(selfPath), "game-updater.exe")
	raw = cliutil.CleanPath(raw)
	if raw == "" {
		return defaultOut, nil
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("输出路径无效: %w", err)
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		abs = filepath.Join(abs, "game-updater.exe")
	}
	return abs, nil
}

func resolveWizardGodotKey(raw string) (string, error) {
	raw = cliutil.CleanPath(raw)
	if raw == "" {
		return "", nil
	}
	if info, err := os.Stat(raw); err == nil && !info.IsDir() {
		return raw, nil
	}
	key, err := parseGodotHexKey(raw)
	if err != nil {
		return "", err
	}
	return key, nil
}

func (m wizardModel) View() string {
	if m.done || m.cancel {
		return ""
	}
	contentWidth := m.width - 16
	if contentWidth < 42 {
		contentWidth = 42
	}
	if contentWidth > 84 {
		contentWidth = 84
	}

	header := tuiTitleStyle.Render("🎮 Game Patcher") + "\n" +
		tuiSubtitleStyle.Render("把两个游戏版本之间的差异，压缩成一个可独立运行的升级工具")
	rail := m.renderStepRail(contentWidth)
	body := m.renderStepBody(contentWidth)
	footer := m.renderFooter()

	panel := tuiBoxStyle.Width(contentWidth).Render(strings.Join([]string{header, "", rail, "", body, "", footer}, "\n"))
	if m.err != "" {
		panel += "\n" + tuiErrorStyle.Render("⚠ "+m.err)
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(panel)
}

func (m wizardModel) renderStepRail(width int) string {
	type railStep struct {
		label string
		key   bool
	}
	steps := []railStep{{"目录", false}, {"补丁", false}, {"适配器", false}, {"Key", true}, {"输出", false}, {"确认", false}}
	var parts []string
	for i, s := range steps {
		label := fmt.Sprintf("%d %s", i+1, s.label)
		if s.key && m.adapterChosen && m.adapterName != "godot" {
			parts = append(parts, tuiMutedStyle.Render(label))
			continue
		}
		active := m.currentRailIndex() == i
		if active {
			parts = append(parts, tuiSelectedStyle.Render("◆ "+label))
		} else {
			parts = append(parts, tuiMutedStyle.Render("◇ "+label))
		}
	}
	rail := strings.Join(parts, "   ")
	return lipgloss.NewStyle().Width(width).Render(rail)
}

func (m wizardModel) currentRailIndex() int {
	switch m.step {
	case stepOldPath, stepNewPath:
		return 0
	case stepPrevPatches:
		return 1
	case stepAdapter:
		return 2
	case stepGodotKey:
		return 3
	case stepOutput:
		return 4
	case stepConfirm:
		return 5
	default:
		return 0
	}
}

func (m wizardModel) renderStepBody(width int) string {
	switch m.step {
	case stepOldPath:
		return tuiLabelStyle.Render("旧版本文件夹") + "\n" + m.oldInput.View()
	case stepNewPath:
		return tuiLabelStyle.Render("新版本文件夹") + "\n" + m.newInput.View()
	case stepPrevPatches:
		var b strings.Builder
		b.WriteString(tuiLabelStyle.Render("附加旧补丁"))
		b.WriteString("\n")
		if len(m.prevPaths) == 0 {
			b.WriteString(tuiMutedStyle.Render("尚未添加。留空继续可生成单段补丁。"))
		} else {
			for _, p := range m.prevPaths {
				b.WriteString(tuiSuccessStyle.Render("✓ "))
				b.WriteString(filepath.Base(p))
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
		b.WriteString(m.prevInput.View())
		return b.String()
	case stepAdapter:
		return tuiLabelStyle.Render("选择适配器") + "\n" + m.adapterList.View()
	case stepGodotKey:
		return tuiLabelStyle.Render("Godot PCK AES-256 Key") + "\n" +
			tuiMutedStyle.Render("留空则跳过；也可以填写包含 64 位 hex key 的文件路径。") + "\n" +
			m.keyInput.View()
	case stepOutput:
		defaultOut := filepath.Join(filepath.Dir(m.selfPath), "game-updater.exe")
		return tuiLabelStyle.Render("输出升级工具") + "\n" +
			tuiMutedStyle.Render("默认: "+defaultOut) + "\n" +
			m.outputInput.View()
	case stepConfirm:
		return m.renderSummary(width)
	default:
		return ""
	}
}

func (m wizardModel) renderSummary(width int) string {
	var b strings.Builder
	b.WriteString(tuiSuccessStyle.Render("准备生成升级工具"))
	b.WriteString("\n\n")
	b.WriteString(tuiLabelStyle.Render("旧版本") + "  " + m.oldPath + "\n")
	b.WriteString(tuiLabelStyle.Render("新版本") + "  " + m.newPath + "\n")
	b.WriteString(tuiLabelStyle.Render("附 加") + "  ")
	if len(m.prevPaths) == 0 {
		b.WriteString("无（单段补丁）")
	} else {
		b.WriteString(fmt.Sprintf("%d 份", len(m.prevPaths)))
	}
	b.WriteString("\n")
	b.WriteString(tuiLabelStyle.Render("适配器") + "  " + adapterSelectionDisplay(m.adapterName) + "\n")
	if m.adapterName == "godot" {
		b.WriteString(tuiLabelStyle.Render("Godot Key") + "  ")
		if m.godotKey == "" {
			b.WriteString(tuiMutedStyle.Render("未提供（可能在构建时回退 CDC）"))
		} else if _, err := os.Stat(m.godotKey); err == nil {
			b.WriteString(filepath.Base(m.godotKey))
		} else {
			b.WriteString(m.godotKey)
		}
		b.WriteString("\n")
	}
	b.WriteString(tuiLabelStyle.Render("输 出") + "  " + m.output + "\n")
	b.WriteString("\n")
	b.WriteString(tuiSubtitleStyle.Render("确认无误后按 Enter 开始构建。"))
	return b.String()
}

func (m wizardModel) renderFooter() string {
	if m.step == stepConfirm {
		return tuiMutedStyle.Render("Enter 开始构建   Esc 返回   Ctrl+C 取消")
	}
	if m.step == stepPrevPatches {
		return tuiMutedStyle.Render("Enter 添加/继续   Esc 返回   Ctrl+C 取消")
	}
	if m.step == stepAdapter {
		return tuiMutedStyle.Render("↑/↓ 或 j/k 选择   Enter 确认   Esc 返回   Ctrl+C 取消")
	}
	return tuiMutedStyle.Render("Enter 下一步   Esc 返回/取消   Ctrl+C 取消   支持直接粘贴带引号路径")
}

func runInteractiveWizard(selfPath string) (interactiveConfig, error) {
	final, err := runWizardProgram(newWizardModel(selfPath), tea.WithAltScreen())
	if err != nil {
		return interactiveConfig{}, err
	}
	if final.cancel {
		return interactiveConfig{}, patcher.ErrCancelled
	}
	if final.err != "" {
		return interactiveConfig{}, errors.New(final.err)
	}
	return final.config(), nil
}

func runWizardProgram(m wizardModel, opts ...tea.ProgramOption) (wizardModel, error) {
	program := tea.NewProgram(m, opts...)
	finalModel, err := program.Run()
	if err != nil {
		return wizardModel{}, fmt.Errorf("TUI 运行失败: %w", err)
	}
	final, ok := finalModel.(wizardModel)
	if !ok {
		return wizardModel{}, errors.New("TUI 返回了未知模型")
	}
	return final, nil
}
