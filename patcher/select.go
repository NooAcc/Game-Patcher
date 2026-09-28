package patcher

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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

// readSelectorKey 从输入流读取一个按键。方向键在 Windows 控制台以 ESC [ A/B 到达。
func readSelectorKey(r *bufio.Reader) (keyKind, error) {
	b, err := r.ReadByte()
	if err != nil {
		return keyNone, err
	}
	switch b {
	case ' ':
		return keySpace, nil
	case '\r', '\n':
		return keyEnter, nil
	case 'a', 'A':
		return keyToggleAll, nil
	case 'q', 'Q', 3: // Ctrl+C 在 raw 模式下以 0x03 到达
		return keyCancel, nil
	case 'k', 'K':
		return keyUp, nil
	case 'j', 'J':
		return keyDown, nil
	case 0x1b:
		if r.Buffered() == 0 {
			return keyCancel, nil
		}
		next, err := r.ReadByte()
		if err != nil || (next != '[' && next != 'O') {
			return keyCancel, nil
		}
		code, err := r.ReadByte()
		if err != nil {
			return keyNone, err
		}
		switch code {
		case 'A':
			return keyUp, nil
		case 'B':
			return keyDown, nil
		default:
			return keyNone, nil
		}
	default:
		return keyNone, nil
	}
}

// selectorScreen 负责重绘：每次把上一次输出的行数上移后覆盖。
type selectorScreen struct {
	w     io.Writer
	lines int
}

func (s *selectorScreen) draw(lines []string) {
	var b strings.Builder
	if s.lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", s.lines)
	}
	for _, line := range lines {
		b.WriteString("\r\x1b[2K")
		b.WriteString(line)
		b.WriteString("\n")
	}
	for i := len(lines); i < s.lines; i++ {
		b.WriteString("\r\x1b[2K\n")
	}
	s.lines = len(lines)
	_, _ = io.WriteString(s.w, b.String())
}

// runSelector 运行选择循环，输入输出可替换，便于测试。
func runSelector(jobs []treeJob, in io.Reader, out io.Writer, width, rows int) ([]treeJob, error) {
	m := newSelectionModel(jobs, rows)
	screen := &selectorScreen{w: out}
	_, _ = io.WriteString(out, "\x1b[?25l")
	defer func() { _, _ = io.WriteString(out, "\x1b[?25h") }()
	screen.draw(m.render(width))

	reader := bufio.NewReader(in)
	for {
		key, err := readSelectorKey(reader)
		if err != nil {
			return nil, err
		}
		switch key {
		case keyUp:
			m.move(-1)
		case keyDown:
			m.move(1)
		case keySpace:
			m.toggle()
		case keyToggleAll:
			m.toggleAll()
		case keyCancel:
			return nil, ErrCancelled
		case keyEnter:
			if m.count() == 0 {
				m.notice = "至少选择一个文件"
				break
			}
			return m.chosen(), nil
		}
		screen.draw(m.render(width))
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

	oldState, err := term.MakeRaw(inFd)
	if err != nil {
		return nil, fmt.Errorf("无法进入交互选择模式: %w", err)
	}
	restored := false
	restore := func() {
		if !restored {
			_ = term.Restore(inFd, oldState)
			restored = true
		}
	}
	defer restore()

	chosen, selErr := runSelector(jobs, os.Stdin, os.Stdout, width, height-5)
	restore()
	fmt.Fprintln(os.Stdout)
	if selErr != nil {
		return nil, selErr
	}
	if excluded := len(jobs) - len(chosen); excluded > 0 {
		fmt.Printf("✅ 已选择 %d 个文件，排除 %d 个文件。\n", len(chosen), excluded)
	} else {
		fmt.Printf("✅ 已选择全部 %d 个变更文件。\n", len(chosen))
	}
	return chosen, nil
}
