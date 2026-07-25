package main

import (
	"os"

	"game-patcher/patcher"
)

// game-patcher-upgrader 是一个极简的升级工具。
// 它只做一件事：从自身嵌入的数据中提取补丁并执行升级。
// 没有参数解析、没有交互模式、没有用法说明。
// 生成的升级工具 EXE = 此二进制 + 补丁数据 + [可选]恢复工具
func main() {
	selfPath, _ := os.Executable()
	patcher.RunEmbedded(selfPath)
}
