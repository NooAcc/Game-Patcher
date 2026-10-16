// Package cliutil 提供各 CLI 共用的路径与二进制查找工具。
//
// 抽出的动机：CLI 与 merge 工具都需要清洗用户粘贴的路径、解析逗号分隔的路径列表，
// 并在自身目录下查找配套二进制（upgrader / restorer）。集中一份可避免候选名单
// 与清洗规则在多处漂移。
package cliutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UpgraderCandidates 是升级工具基础程序的可选文件名，按优先级排列。
var UpgraderCandidates = []string{
	"game-patcher-upgrader-win64.exe",
	"game-patcher-upgrader-win64",
	"game-patcher-upgrader.exe",
	"game-patcher-upgrader",
	"upgrader.exe",
	"upgrader",
}

// RestorerCandidates 是恢复工具二进制的可选文件名，按优先级排列。
var RestorerCandidates = []string{
	"restorer-win64.exe",
	"restorer-win64",
	"restorer.exe",
	"restorer",
}

// CleanPath 规范化用户粘贴的路径。
//
// Windows 资源管理器的"复制文件/文件夹地址"得到的是带双引号的形式
// （"D:\Games\My Game"），直接粘贴会连引号一起进入程序。双引号在 Windows 路径中
// 本身非法，因此可以安全剥掉；同时去掉粘贴常见的首尾空白。
func CleanPath(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// SplitPathList 按逗号拆分列表，但忽略双引号内部的逗号
// （从资源管理器复制的路径可能形如 "D:\Games, Inc\v1"）。
func SplitPathList(s string) []string {
	parts := make([]string, 0, 1)
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(parts, cur.String())
}

// FindBinary 查找二进制：优先用户指定，否则在 selfPath 同目录按候选名查找。
// 返回空字符串表示未找到。
func FindBinary(selfPath, userSpecified string, candidates []string) string {
	userSpecified = CleanPath(userSpecified)
	if userSpecified != "" {
		if _, err := os.Stat(userSpecified); err != nil {
			fmt.Printf("⚠️  指定的文件不存在: %s\n", userSpecified)
			return ""
		}
		abs, _ := filepath.Abs(userSpecified)
		return abs
	}
	selfDir := filepath.Dir(selfPath)
	for _, name := range candidates {
		candidate := filepath.Join(selfDir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}