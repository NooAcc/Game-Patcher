package patcher

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testJobs(n int) []treeJob {
	jobs := make([]treeJob, 0, n)
	for i := 0; i < n; i++ {
		action := ActionUpdate
		switch i % 3 {
		case 1:
			action = ActionAdd
		case 2:
			action = ActionDelete
		}
		jobs = append(jobs, treeJob{
			path:    fmt.Sprintf("dir/file%02d.bin", i),
			action:  action,
			oldMeta: fileMeta{size: uint64(100 + i)},
			newMeta: fileMeta{size: uint64(200 + i)},
		})
	}
	return jobs
}

func TestSelectionModelDefaultsToAllSelected(t *testing.T) {
	jobs := testJobs(3)
	m := newSelectionModel(jobs, 10)
	if m.count() != 3 {
		t.Fatalf("默认应全选，实际 %d", m.count())
	}
	if m.rows != 3 {
		t.Fatalf("行数应被限制为条目数 3，实际 %d", m.rows)
	}
	if len(m.chosen()) != 3 {
		t.Fatalf("chosen 应返回全部条目")
	}
}

func TestSelectionModelToggleAndToggleAll(t *testing.T) {
	m := newSelectionModel(testJobs(3), 2)
	m.toggle()
	if m.count() != 2 {
		t.Fatalf("空格应取消当前项，实际选中 %d", m.count())
	}
	m.toggleAll()
	if m.count() != 3 {
		t.Fatalf("未全选时 A 应全选，实际 %d", m.count())
	}
	m.toggleAll()
	if m.count() != 0 {
		t.Fatalf("全选时 A 应全不选，实际 %d", m.count())
	}
}

func TestSelectionModelMoveScrollsViewport(t *testing.T) {
	m := newSelectionModel(testJobs(10), 5)
	for i := 0; i < 5; i++ {
		m.move(1)
	}
	if m.cursor != 5 || m.offset != 1 {
		t.Fatalf("向下移动后 cursor=%d offset=%d，期望 5/1", m.cursor, m.offset)
	}
	for i := 0; i < 20; i++ {
		m.move(1)
	}
	if m.cursor != 9 || m.offset != 5 {
		t.Fatalf("到底后 cursor=%d offset=%d，期望 9/5", m.cursor, m.offset)
	}
	for i := 0; i < 30; i++ {
		m.move(-1)
	}
	if m.cursor != 0 || m.offset != 0 {
		t.Fatalf("回顶后 cursor=%d offset=%d，期望 0/0", m.cursor, m.offset)
	}
}

func TestSelectionRender(t *testing.T) {
	jobs := testJobs(25)
	m := newSelectionModel(jobs, 5)
	lines := m.render(100)
	if len(lines) != 9 { // 标题 + 分隔线 + 5 行 + 分隔线 + 底部提示
		t.Fatalf("渲染行数 = %d，期望 9", len(lines))
	}
	if !strings.Contains(lines[2], "[x]") || !strings.Contains(lines[2], jobs[0].path) {
		t.Fatalf("首行渲染异常: %q", lines[2])
	}
	m.move(10)
	lines = m.render(100)
	if !strings.Contains(lines[2], jobs[6].path) {
		t.Fatalf("滚动后首行应为 %s，实际 %q", jobs[6].path, lines[2])
	}
	if got := m.itemLine(0, 10); len([]rune(got)) > 10 {
		t.Fatalf("超长路径未截断: %q", got)
	}
}

func TestSelectionProgramModelConfirmAndCancel(t *testing.T) {
	m := newSelectionProgramModel(testJobs(3), 80, 20)
	m, _ = m.update(tea.KeyMsg{Type: tea.KeySpace})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.chosen) != 2 || m.chosen[0].path != "dir/file01.bin" {
		t.Fatalf("确认后 chosen=%+v", m.chosen)
	}

	cancelled := newSelectionProgramModel(testJobs(3), 80, 20)
	cancelled, _ = cancelled.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !cancelled.cancelled {
		t.Fatal("q 应取消选择")
	}
}

func TestSelectionProgramModelRequiresAtLeastOne(t *testing.T) {
	m := newSelectionProgramModel(testJobs(3), 80, 20)
	m.core.toggleAll()
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.chosen != nil {
		t.Fatalf("零选择不应确认: %+v", m.chosen)
	}
	if !strings.Contains(m.core.notice, "至少选择") {
		t.Fatalf("notice = %q", m.core.notice)
	}

	m, _ = m.update(tea.KeyMsg{Type: tea.KeySpace})
	m, _ = m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.chosen) != 1 || m.chosen[0].path != "dir/file00.bin" {
		t.Fatalf("重新选择后 chosen=%+v", m.chosen)
	}
}

func TestSelectionProgramViewContainsStyledTitle(t *testing.T) {
	m := newSelectionProgramModel(testJobs(3), 80, 20)
	view := m.View()
	if !strings.Contains(view, "选择要包含在补丁中的文件") {
		t.Fatalf("view missing title: %q", view)
	}
	if !strings.Contains(view, "Enter") {
		t.Fatalf("view missing footer: %q", view)
	}
}
