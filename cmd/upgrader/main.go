package main

import (
	"flag"
	"fmt"
	"os"

	// 注册内置引擎适配器：应用端需要它们来重建被重写的容器文件。
	_ "game-patcher/adapter/all"

	"game-patcher/patcher"
)

// game-patcher-upgrader 是生成的升级工具模板。
// 它只做一件事：从自身尾部读取 GPBIN4 补丁数据并执行升级。
// 无论成功、取消还是失败，退出前都会等待用户按回车，避免双击运行时窗口闪退。
func main() {
	os.Exit(run())
}

func run() int {
	// -target 仅用于合并补丁出现"多目标同时匹配"歧义时手动指定目标，
	// 普通补丁与无参数双击场景都不受影响。
	target := flag.String("target", "", "合并补丁的目标游戏标识（仅在自动识别出现歧义时需要）")
	flag.Parse()

	selfPath, err := os.Executable()
	if err != nil {
		fmt.Printf("❌ 获取自身路径失败: %v\n", err)
		patcher.Pause()
		return 1
	}
	if err := patcher.RunEmbedded(selfPath, patcher.RunOptions{Target: *target}); err != nil {
		fmt.Printf("\n❌ %v\n", err)
		patcher.Pause()
		return 1
	}
	patcher.Pause()
	return 0
}