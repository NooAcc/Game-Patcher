package main

import (
	"bufio"
	"sort"

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
	adapters := adapter.DefaultRegistry.Adapters()
	// 注册顺序由包的初始化顺序决定（Go 未规定独立包之间的顺序），
	// 这里按名称排序，保证交互列表稳定。
	sort.Slice(adapters, func(i, j int) bool { return adapters[i].Name() < adapters[j].Name() })

	items := make([]adaptMenuItem, 0, len(adapters)+1)
	items = append(items, adaptMenuItem{Label: "禁用适配器（强制通用 CDC）", Value: adapter.SelectedNone})
	for _, a := range adapters {
		items = append(items, adaptMenuItem{Label: adapter.DisplayName(a), Value: a.Name()})
	}
	return items
}
