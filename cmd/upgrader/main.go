package main

import (
	"fmt"
	"os"

	"game-patcher/patcher"
)

// game-patcher-upgrader 是生成的升级工具模板。
// 它只做一件事：从自身尾部读取 GPBIN4 链式补丁并执行升级。
// 无论成功、取消还是失败，退出前都会等待用户按回车，避免双击运行时窗口闪退。
func main() {
	os.Exit(run())
}

func run() int {
	selfPath, err := os.Executable()
	if err != nil {
		fmt.Printf("❌ 获取自身路径失败: %v\n", err)
		patcher.Pause()
		return 1
	}
	if err := patcher.RunEmbedded(selfPath); err != nil {
		fmt.Printf("\n❌ %v\n", err)
		patcher.Pause()
		return 1
	}
	patcher.Pause()
	return 0
}
