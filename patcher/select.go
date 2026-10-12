package patcher

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// ErrCancelled 表示用户主动取消了当前操作。
var ErrCancelled = errors.New("用户已取消")

const (
	selectorMinRows = 5
	selectorMaxRows = 20
	selectorFooter  = "↑/↓ 移动   空格 选择/取消   A 全选/反选   Enter 生成   Q 取消"
)

type keyKind int

const (
	keyNone keyKind = iota
	keyUp
	keyDown
	keySpace
	keyEnter
	keyToggleAll
	keyCancel
)

// selectionModel 是文件选择界面的纯逻辑模型，与终端无关，便于单元测试。
type selectionModel struct {
	jobs     []treeJob
	selected []bool
	cursor   int
	offset   int
	rows     int
	notice   string
}

func newSelectionModel(jobs []treeJob, rows int) *selectionModel {
	if rows < selectorMinRows {
		rows = selectorMinRows
	}
	if rows > selectorMaxRows {
		rows = selectorMaxRows
	}
	if rows > len(jobs) {
		rows = len(jobs)
	}
	if rows < 1 {
		rows = 1
	}
	m := &selectionModel{jobs: jobs, selected: make([]bool, len(jobs)), rows: rows}
	for i := range m.selected {
		m.selected[i] = true
	}
	return m
}

func (m *selectionModel) move(delta int) {
	m.notice = ""
	if len(m.jobs) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.jobs) {
		m.cursor = len(m.jobs) - 1
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.rows {
		m.offset = m.cursor - m.rows + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *selectionModel) toggle() {
	m.notice = ""
	if len(m.jobs) == 0 {
		return
	}
	m.selected[m.cursor] = !m.selected[m.cursor]
}

func (m *selectionModel) toggleAll() {
	m.notice = ""
	all := m.count() == len(m.jobs)
	for i := range m.selected {
		m.selected[i] = !all
	}
}

func (m *selectionModel) count() int {
	n := 0
	for _, ok := range m.selected {
		if ok {
			n++
		}
	}
	return n
}

func (m *selectionModel) chosen() []treeJob {
	out := make([]treeJob, 0, m.count())
	for i, ok := range m.selected {
		if ok {
			out = append(out, m.jobs[i])
		}
	}
	return out
}

func (m *selectionModel) render(width int) []string {
	lines := make([]string, 0, m.rows+4)
	lines = append(lines, fmt.Sprintf("选择要包含在补丁中的文件（共 %d 项，已选 %d 项）", len(m.jobs), m.count()))
	lines = append(lines, strings.Repeat("─", clampInt(width-1, 10, 200)))

	end := m.offset + m.rows
	if end > len(m.jobs) {
		end = len(m.jobs)
	}
	for i := m.offset; i < end; i++ {
		lines = append(lines, m.itemLine(i, width))
	}
	lines = append(lines, strings.Repeat("─", clampInt(width-1, 10, 200)))

	footer := selectorFooter
	if m.notice != "" {
		footer = "⚠️  " + m.notice
	}
	lines = append(lines, footer)
	return lines
}

func (m *selectionModel) itemLine(i, width int) string {
	job := &m.jobs[i]
	cursor := "  "
	if i == m.cursor {
		cursor = "❯ "
	}
	check := "[ ]"
	if m.selected[i] {
		check = "[x]"
	}
	action := "~"
	switch job.action {
	case ActionAdd:
		action = "+"
	case ActionDelete:
		action = "-"
	}
	line := fmt.Sprintf("%s%s %s %s   %s", cursor, check, action, job.path, formatJobSize(job))
	return truncateRunes(line, width)
}

func formatJobSize(job *treeJob) string {
	switch job.action {
	case ActionAdd:
		return FormatSize(int64(job.newMeta.size))
	case ActionDelete:
		return FormatSize(int64(job.oldMeta.size))
	default:
		return FormatSize(int64(job.oldMeta.size)) + " → " + FormatSize(int64(job.newMeta.size))
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncateRunes(s string, width int) string {
	if width <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

type selectionProgramModel struct {
	core      *selectionModel
	width     int
	height    int
	chosen    []treeJob
	cancelled bool
}

var (
	selectionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#C4B5FD"})
	selectionMetaStyle = lipgloss.NewStyle().
				Foreground(lipgloss.AdaptiveColor{Light: "#64748B", Dark: "#94A3B8"})
	selectionSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"})
	selectionAddedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#86EFAC"})
	selectionDeletedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"})
	selectionNoticeStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"})
	selectionBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.AdaptiveColor{Light: "#CBD5E1", Dark: "#334155"}).
				Padding(1, 2)
)

func newSelectionProgramModel(jobs []treeJob, width, height int) selectionProgramModel {
	if width < 60 {
		width = 60
	}
	rows := clampInt(height-8, selectorMinRows, selectorMaxRows)
	if rows > len(jobs) {
		rows = len(jobs)
	}
	if rows < 1 {
		rows = 1
	}
	return selectionProgramModel{
		core:   newSelectionModel(jobs, rows),
		width:  width,
		height: height,
	}
}

func (m selectionProgramModel) Init() tea.Cmd { return nil }

func (m selectionProgramModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	return next, cmd
}

func (m selectionProgramModel) update(msg tea.Msg) (selectionProgramModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			m.core.move(-1)
		case "down", "j":
			m.core.move(1)
		case " ":
			m.core.toggle()
		case "a", "A":
			m.core.toggleAll()
		case "enter":
			if m.core.count() == 0 {
				m.core.notice = "至少选择一个文件"
				return m, nil
			}
			m.chosen = m.core.chosen()
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *selectionProgramModel) resize() {
	rows := clampInt(m.height-8, selectorMinRows, selectorMaxRows)
	if rows > len(m.core.jobs) {
		rows = len(m.core.jobs)
	}
	if rows < 1 {
		rows = 1
	}
	m.core.rows = rows
	if m.core.cursor >= m.core.offset+m.core.rows {
		m.core.offset = m.core.cursor - m.core.rows + 1
	}
	if m.core.offset < 0 {
		m.core.offset = 0
	}
}

func (m selectionProgramModel) View() string {
	width := m.width - 8
	if width < 50 {
		width = 50
	}
	if width > 120 {
		width = 120
	}

	added, updated, deleted := m.actionCounts()
	meta := fmt.Sprintf("已选 %d/%d    新增 %d    修改 %d    删除 %d", m.core.count(), len(m.core.jobs), added, updated, deleted)
	lines := []string{
		selectionTitleStyle.Render("选择要包含在补丁中的文件"),
		selectionMetaStyle.Render(meta),
		strings.Repeat("─", clampInt(width-4, 20, 116)),
	}
	end := m.core.offset + m.core.rows
	if end > len(m.core.jobs) {
		end = len(m.core.jobs)
	}
	for i := m.core.offset; i < end; i++ {
		lines = append(lines, m.row(i, width-4))
	}
	lines = append(lines, strings.Repeat("─", clampInt(width-4, 20, 116)))
	if m.core.notice != "" {
		lines = append(lines, selectionNoticeStyle.Render("⚠ "+m.core.notice))
	} else {
		lines = append(lines, selectionMetaStyle.Render("↑/↓ 或 j/k 移动    Space 选择/取消    A 全选/反选    Enter 确认    Q 取消"))
	}
	return selectionBorderStyle.Width(width).Render(strings.Join(lines, "\n"))
}

func (m selectionProgramModel) actionCounts() (added, updated, deleted int) {
	for _, job := range m.core.jobs {
		switch job.action {
		case ActionAdd:
			added++
		case ActionDelete:
			deleted++
		default:
			updated++
		}
	}
	return
}

func (m selectionProgramModel) row(i, width int) string {
	job := &m.core.jobs[i]
	cursor := "  "
	if i == m.core.cursor {
		cursor = "❯ "
	}
	check := "○"
	if m.core.selected[i] {
		check = "●"
	}
	action := "~"
	switch job.action {
	case ActionAdd:
		action = "+"
	case ActionDelete:
		action = "-"
	}
	line := truncateRunes(fmt.Sprintf("%s%s %s %s   %s", cursor, check, action, job.path, formatJobSize(job)), width)
	if i == m.core.cursor {
		return selectionSelectedStyle.Render(line)
	}
	switch job.action {
	case ActionAdd:
		return selectionAddedStyle.Render(line)
	case ActionDelete:
		return selectionDeletedStyle.Render(line)
	default:
		return line
	}
}

// selectJobs 在有真实终端时让用户勾选变更文件；非交互终端自动全选。
func selectJobs(jobs []treeJob) ([]treeJob, error) {
	if len(jobs) == 0 {
		return jobs, nil
	}
	inFd := int(os.Stdin.Fd())
	outFd := int(os.Stdout.Fd())
	if !term.IsTerminal(inFd) || !term.IsTerminal(outFd) {
		fmt.Printf("ℹ️  非交互终端，默认包含全部 %d 个变更文件。\n", len(jobs))
		return jobs, nil
	}

	width, height, err := term.GetSize(outFd)
	if err != nil || width <= 0 {
		width = 100
	}
	if height <= 0 {
		height = 25
	}

	program := tea.NewProgram(newSelectionProgramModel(jobs, width, height), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return nil, fmt.Errorf("无法进入交互选择模式: %w", err)
	}
	final, ok := finalModel.(selectionProgramModel)
	if !ok {
		return nil, errors.New("文件选择界面返回了未知模型")
	}
	if final.cancelled {
		return nil, ErrCancelled
	}
	chosen := final.chosen
	if chosen == nil {
		chosen = jobs
	}
	fmt.Fprintln(os.Stdout)
	if excluded := len(jobs) - len(chosen); excluded > 0 {
		fmt.Printf("✅ 已选择 %d 个文件，排除 %d 个文件。\n", len(chosen), excluded)
	} else {
		fmt.Printf("✅ 已选择全部 %d 个变更文件。\n", len(chosen))
	}
	return chosen, nil
}
