# 🎮 游戏升级包制作工具

对比两个版本的游戏文件夹，生成**单文件升级工具**。玩家双击即可完成升级，无需安装任何运行时。

## 特性

- **增量升级** — 只打包差异文件，生成的升级包体积极小
- **零依赖分发** — 生成的升级工具本身就是可执行文件
- **BLAKE3 + SIMD** — 哈希性能比 SHA-256 快 260 倍
- **并行处理** — 多线程扫描、哈希、验证
- **自动备份** — 升级前备份旧文件，附带恢复工具

## 下载

预编译的 Windows 程序发布在 [GitHub Releases](https://github.com/NooAcc/game-patcher/releases)。
推送 `v*` 版本标签时，GitHub Actions 会自动构建三个程序、生成 SHA-256 校验文件并创建 Release。

## 快速开始

### 前置要求

- Go 1.21+
- Windows（目标平台）

### 构建

```bash
make all        # 构建所有版本
make cli        # 仅 CLI
```

构建产物位于 `build/` 目录。

### 使用方法

#### 参数模式

```bash
game-patcher-cli-win64.exe -old <旧版本> -new <新版本> [-out <输出>]
```

示例：

```bash
game-patcher-cli-win64.exe -old ./v1.0 -new ./v1.1 -out ./game-updater.exe
```

`-out` 可省略，默认输出到当前目录的 `game-updater.exe`。

#### 交互模式

```bash
game-patcher-cli-win64.exe -shell
```

程序会依次询问旧版本、新版本文件夹路径和输出位置，输入完成后自动执行。

> **注意：** 使用前需将 `game-patcher-upgrader-win64.exe` 和 `restorer-win64.exe`
> 放在与 CLI 同一目录下，程序会自动查找并嵌入。

## 项目结构

```
游戏补丁生成/
├── cmd/
│   ├── cli/          # CLI 入口（创建补丁）
│   │   └── main.go
│   ├── upgrader/     # 升级工具入口（应用补丁，极简无参数）
│   │   └── main.go
│   └── restorer/     # 恢复工具入口（回退升级）
│       └── main.go
├── patcher/          # 核心逻辑库
│   └── patcher.go
├── build/            # 构建产物
├── Makefile
├── go.mod
└── go.sum
```

## 架构设计

三个独立程序，职责分离：

| 程序 | 作用 | 运行环境 |
|------|------|----------|
| `game-patcher-cli` | 对比目录、生成升级包 | 开发者机器 |
| `game-patcher-upgrader` | 极简升级工具，作为生成 EXE 的基础 | 玩家机器 |
| `restorer` | 回退升级，恢复旧版本 | 玩家机器 |

生成的升级包结构：

```
┌─────────────────────────────────────┐
│  upgrader 二进制（极简，无参数解析） │
├─────────────────────────────────────┤
│  补丁数据 (GAMEPATCH1 + manifest)   │
├─────────────────────────────────────┤
│  [可选] 恢复工具 (RESTOREBIN!)      │
└─────────────────────────────────────┘
```

## 升级流程

```
创建升级工具:
  1. 并行扫描旧/新版本目录 (BLAKE3 哈希)
  2. 对比差异: 新增 / 修改 / 删除
  3. upgrader 二进制 + 差异数据 → 单文件升级工具

运行升级工具:
  1. 读取嵌入的补丁数据
  2. 备份旧文件到 _backup_before_patch/
  3. 应用变更 (add / update / delete)
  4. 并行验证所有文件完整性
  5. 提示恢复方法
```

## 恢复旧版本

升级后，旧文件备份在 `_backup_before_patch/` 目录中，内含恢复工具：

```bash
# 进入备份目录
cd _backup_before_patch/

# 运行恢复工具
restorer.exe       # Windows
```

## 补丁数据格式

```
┌──────────────────────────────────────────────────┐
│  upgrader 二进制                                  │
├──────────────────────────────────────────────────┤
│  "GAMEPATCH1"         (10B 魔术标记)              │
│  manifest_length      (4B, uint32 LE)            │
│  manifest JSON        (操作清单)                  │
│  file_count           (4B, uint32 LE)            │
│  ┌─────────────────────────────────────────────┐ │
│  │ path_len(4B) + path + data_len(8B) + data   │ │  ← 重复 N 个文件
│  └─────────────────────────────────────────────┘ │
├──────────────────────────────────────────────────┤
│  patch_data_length    (8B, uint64 LE)            │
│  "PATCHTAIL!"         (10B 尾标记)               │
├──────────────────────────────────────────────────┤
│  [可选] restorer 二进制数据                       │
│  restorer_data_length (8B, uint64 LE)            │
│  "RESTOREBIN!"        (10B 恢复工具标记)          │
└──────────────────────────────────────────────────┘
```

## 技术栈

- **语言**: Go 1.25
- **哈希**: BLAKE3 (github.com/zeebo/blake3) + AVX2/SSE4.1 SIMD
- **大文件**: mmap 零拷贝 (≥1MB)
- **小文件**: 256KB 缓冲区池复用

## 许可证

本项目采用 [MIT License](LICENSE)。
