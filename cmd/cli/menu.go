package main

import (
	"bufio"

	"game-patcher/adapter"
)

type adaptMenuItem struct {
	Label string
	Value string
}

// promptAdapter 是非 TTY 降级流程的适配器选择入口。
// 真正交互式终端由 Bubble Tea 向导处理；这里保持 none，避免自动化环境误用引擎适配器。
func promptAdapter(_ *bufio.Reader) (string, error) {
	return adapter.SelectedNone, nil
}

func adapterSelectionDisplay(value string) string {
	switch value {
	case "", adapter.SelectedNone:
		return "禁用（通用 CDC）"
	}
	for _, a := range adapter.DefaultRegistry.Adapters() {
		if a.Name() == value {
			return adapter.DisplayName(a)
		}
	}
	return value
}

func adapterMenuItems() []adaptMenuItem {
	items := make([]adaptMenuItem, 0, len(adapter.DefaultRegistry.Adapters())+1)
	items = append(items, adaptMenuItem{Label: "禁用适配器（强制通用 CDC）", Value: adapter.SelectedNone})
	for _, a := range adapter.DefaultRegistry.Adapters() {
		items = append(items, adaptMenuItem{Label: adapter.DisplayName(a), Value: a.Name()})
	}
	return items
}
