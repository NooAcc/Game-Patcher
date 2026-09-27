package main

import (
	"os"

	"game-patcher/patcher"
)

// game-patcher-upgrader 是生成的升级工具模板。
// 它只做一件事：从自身尾部读取 GPBIN2 补丁并执行升级。
func main() {
	selfPath, err := os.Executable()
	if err != nil {
		patcher.Fatal("❌ 获取自身路径失败: %v", err)
	}
	if err := patcher.RunEmbedded(selfPath); err != nil {
		patcher.Fatal("❌ %v", err)
	}
}
