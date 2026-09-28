package patcher

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
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

func TestReadSelectorKey(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("\x1b[A\x1b[B \rq"))
	want := []keyKind{keyUp, keyDown, keySpace, keyEnter, keyCancel}
	for i, w := range want {
		got, err := readSelectorKey(r)
		if err != nil {
			t.Fatalf("第 %d 个按键读取失败: %v", i, err)
		}
		if got != w {
			t.Fatalf("第 %d 个按键 = %d，期望 %d", i, got, w)
		}
	}
}

func TestRunSelectorConfirmAndCancel(t *testing.T) {
	jobs := testJobs(3)
	chosen, err := runSelector(jobs, strings.NewReader(" \r"), io.Discard, 80, 10)
	if err != nil {
		t.Fatalf("确认流程失败: %v", err)
	}
	if len(chosen) != 2 || chosen[0].path != jobs[1].path {
		t.Fatalf("取消首个文件后应剩 2 项，实际 %+v", chosen)
	}

	if _, err := runSelector(jobs, strings.NewReader("q"), io.Discard, 80, 10); !errors.Is(err, ErrCancelled) {
		t.Fatalf("按 Q 应返回 ErrCancelled，实际 %v", err)
	}
}

func TestRunSelectorRequiresAtLeastOne(t *testing.T) {
	jobs := testJobs(3)
	// A 取消全选 -> Enter 提示 -> 空格选中当前项 -> Enter 确认
	chosen, err := runSelector(jobs, strings.NewReader("a\r \r"), io.Discard, 80, 10)
	if err != nil {
		t.Fatalf("选择流程失败: %v", err)
	}
	if len(chosen) != 1 || chosen[0].path != jobs[0].path {
		t.Fatalf("应只选中当前项，实际 %+v", chosen)
	}
}

func TestRunSelectorHidesCursor(t *testing.T) {
	var out strings.Builder
	if _, err := runSelector(testJobs(2), strings.NewReader("\r"), &out, 80, 10); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[?25l") || !strings.Contains(got, "\x1b[?25h") {
		t.Fatal("选择界面应隐藏并在退出时恢复光标")
	}
}
