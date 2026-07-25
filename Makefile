.PHONY: all cli restorer upgrader clean deps info

# 默认目标：构建 Windows 版本（程序仅在 Windows 使用）
all: deps restorer upgrader cli
	@echo ""
	@echo "🎉 全部构建完成！文件位于 build/ 目录"

# 安装依赖
deps:
	@echo "📦 安装依赖..."
	@GOPROXY=https://goproxy.cn,direct go mod tidy

# ── 恢复工具 ────────────────────────────────────────────────

restorer: deps
	@echo ""
	@echo "🔨 构建恢复工具..."
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o build/restorer-win64.exe ./cmd/restorer/
	@echo "✅ 恢复工具构建完成"

# ── 升级工具基础程序 ────────────────────────────────────────

upgrader: deps
	@echo ""
	@echo "🔨 构建升级工具基础程序..."
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o build/game-patcher-upgrader-win64.exe ./cmd/upgrader/
	@echo "✅ 升级工具基础程序构建完成"

# ── CLI 版本 ────────────────────────────────────────────────

cli: deps
	@echo ""
	@echo "🔨 构建 CLI 版本 (Windows)..."
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o build/game-patcher-cli-win64.exe ./cmd/cli/
	@echo "✅ CLI 构建完成"

# ── 清理 ─────────────────────────────────────────────────────

clean:
	rm -rf build/
	@echo "🧹 清理完成"

# ── 信息 ─────────────────────────────────────────────────────

info:
	@echo "=== 构建产物 ==="
	@echo ""
	@echo "  build/game-patcher-cli-win64.exe       - Windows CLI 版本"
	@echo "  build/game-patcher-upgrader-win64.exe  - 升级工具基础程序"
	@echo "  build/restorer-win64.exe               - Windows 恢复工具"
	@echo ""
	@echo "=== 前置要求 ==="
	@echo ""
	@echo "  Go 1.21+       : https://go.dev/dl/"
	@echo ""
	@echo "=== 使用方法 ==="
	@echo ""
	@echo "  make all       - 构建全部 Windows 版本"
	@echo "  make cli       - 仅构建 CLI 版本"
	@echo ""
	@echo "  参数模式: game-patcher-cli-win64.exe -old <旧版本> -new <新版本>"
	@echo "  交互模式: game-patcher-cli-win64.exe -shell"
