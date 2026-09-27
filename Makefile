.PHONY: all deps vet test test-short cli restorer upgrader clean info

# 默认目标：构建 Windows 版本（程序仅在 Windows 使用）
all: restorer upgrader cli
	@echo ""
	@echo "🎉 全部构建完成！文件位于 build/ 目录"

# 安装/整理依赖
deps:
	@echo "📦 整理依赖..."
	@go mod tidy

# 静态检查
vet:
	@echo "🔎 go vet..."
	@go vet ./...

# 全部测试（含真实 ASAR 集成测试；test/ 缺失时自动跳过）
test:
	@echo "🧪 运行全部测试..."
	@go test ./...

# 仅快速单元测试（跳过 306MB ASAR 集成测试）
test-short:
	@echo "⚡ 运行快速测试..."
	@go test -short ./...

# ── 恢复工具 ────────────────────────────────────────────────

restorer:
	@echo ""
	@echo "🔨 构建恢复工具..."
	go build -ldflags="-s -w" -o build/restorer-win64.exe ./cmd/restorer/
	@echo "✅ 恢复工具构建完成"

# ── 升级工具基础程序 ────────────────────────────────────────

upgrader:
	@echo ""
	@echo "🔨 构建升级工具基础程序..."
	go build -ldflags="-s -w" -o build/game-patcher-upgrader-win64.exe ./cmd/upgrader/
	@echo "✅ 升级工具基础程序构建完成"

# ── CLI 版本 ────────────────────────────────────────────────

cli:
	@echo ""
	@echo "🔨 构建 CLI 版本..."
	go build -ldflags="-s -w" -o build/game-patcher-cli-win64.exe ./cmd/cli/
	@echo "✅ CLI 构建完成"

# ── 清理 ─────────────────────────────────────────────────────

clean:
	rm -rf build/
	@echo "🧹 清理完成"

# ── 信息 ─────────────────────────────────────────────────────

info:
	@echo "=== 构建产物 ==="
	@echo ""
	@echo "  build/game-patcher-cli-win64.exe       - 补丁生成 CLI"
	@echo "  build/game-patcher-upgrader-win64.exe  - 升级工具基础程序"
	@echo "  build/restorer-win64.exe               - 恢复工具"
	@echo ""
	@echo "=== 使用方法 ==="
	@echo ""
	@echo "  目录模式: game-patcher-cli-win64.exe -old <旧目录> -new <新目录>"
	@echo "  单文件模式: game-patcher-cli-win64.exe -old <旧文件> -new <新文件> [-target <目标相对路径>]"
	@echo "  交互模式: game-patcher-cli-win64.exe -shell"
	@echo "  测试: make test / make test-short"
	@echo ""
	@echo "  生成的升级工具为二进制增量补丁，对变化文件只写入差异数据。"
