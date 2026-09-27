# 🎮 Game-Patcher — 二进制增量升级包工具

对比游戏的两个版本，生成**单文件升级工具**。对发生变化的文件只写入**二进制差异数据**（COPY 旧文件区段 + LITERAL 新数据），玩家双击升级工具即可完成升级，无需安装运行时。

## 特性

- **二进制增量修补** — 变化文件不再整文件打包，只嵌入差异数据
- **单文件分发** — 升级工具本身就是一个可执行文件，零运行时依赖
- **内容定义分块（CDC）** — 对插入、删除、整体位移不敏感
- **并行哈希与差异计算** — 目录模式自动并行处理
- **源版本校验** — 升级前逐个校验旧文件 BLAKE3，不匹配立即中止
- **原子写入** — 临时文件重建 + 哈希校验通过后才替换目标
- **自动备份与回滚** — 备份旧文件并附带恢复工具，失败时尽力自动回滚
- **安全解析** — 补丁格式全字段边界校验，路径穿越防护

## 实测效果

以 `test/` 中一对真实 `app.asar`（作为游戏目录内的一个变化文件）为基准：

| 项目 | 数值 |
| --- | --- |
| 旧 `app.asar` | 320,966,965 B（约 306 MB） |
| 新 `app.asar` | 321,150,175 B（约 306 MB） |
| 生成的补丁数据 | **约 1.37 MB（0.449%）** |
| 差异计算耗时 | 约 1.7 s |
| 升级（重建 + 校验） | 约 0.1–1 s |

## 快速开始

### 前置要求

- Go 1.25+（`go.mod` 要求 `go 1.25.0`）
- 目标平台：Windows（其他平台亦可构建运行）

### 构建

```bash
make all      # 构建全部：cli + upgrader + restorer
make cli      # 仅构建 CLI
make test     # 全部测试（含真实 ASAR 集成测试，test/ 缺失时自动跳过）
make test-short
```

产物位于 `build/`。

### 生成升级工具

`-old` 与 `-new` 必须是两个版本的游戏目录：

```bash
game-patcher-cli-win64.exe -old ./v1.0 -new ./v1.1 -out ./game-updater.exe
```

**交互模式**：

```bash
game-patcher-cli-win64.exe -shell
```

> 生成的 EXE 需要 `game-patcher-upgrader-win64.exe` 作为基础程序、可选地需要
> `restorer-win64.exe` 作为恢复工具。默认在 CLI 同目录自动查找，也可用
> `-upgrader` / `-restorer` 显式指定。

把生成的升级工具放到**游戏根目录**，直接运行即可。

## 升级流程

1. 从自身尾部读取并解析 `GPBIN3` 补丁数据；
2. 显示变更统计，请求确认；
3. 校验每个 update/delete 目标文件的 BLAKE3 与补丁记录的旧哈希一致，不一致立即中止；
4. 把旧文件备份到 `_backup_before_patch/`，写入 `restore.json`，释放恢复工具；
5. 对每个目标：COPY 旧文件区段 + 写入 LITERAL 数据 → 临时文件 → 大小/哈希校验 → 原子替换；
6. 删除 delete 条目并清理空目录；
7. 全量校验新文件哈希，输出结果；失败时尽力自动回滚。

## 恢复旧版本

升级后备份位于目标目录下的 `_backup_before_patch/`，其中包含 `restorer.exe` 与 `restore.json`。进入该目录运行恢复工具即可：

```bash
cd _backup_before_patch
restorer.exe
```

恢复工具以“备份目录的父目录”为游戏根，逐项：
- `add` → 删除升级时新增的文件；
- `update` / `delete` → 从备份恢复，并校验恢复后的 BLAKE3 与升级前一致。

## 二进制补丁格式（GPBIN3）

生成的升级工具是：

```
┌────────────────────────────────────────────┐
│  upgrader 基础 EXE                          │
├────────────────────────────────────────────┤
│  GPBIN3 补丁块                              │
│    "GPBIN3"       6B                        │
│    version        1B (=3)                   │
│    entryCount     uint32 LE                 │
│    Entry[]                                  │
├────────────────────────────────────────────┤
│  patchLen         uint64 LE                 │
│  "GPBIN3END!"     10B                       │
├────────────────────────────────────────────┤
│  [可选] restorer 二进制                      │
│  restorerLen      uint64 LE                 │
│  "GPBIN3RST!"     10B                       │
└────────────────────────────────────────────┘
```

每个 `Entry`：

```
pathLen uint32 + path
action  uint8 (1=add 2=update 3=delete)
oldHash [32]byte BLAKE3
newHash [32]byte BLAKE3
oldSize uint64
newSize uint64
opCount uint32
Op[]
```

每个 `Op`：

- `COPY`：`oldOffset uint64 + length uint64`，从旧文件复制区段；
- `LITERAL`：`comp uint8 + rawLen uint64 + dataLen uint64 + data`，`comp=0` 原始、`comp=1` flate 压缩。

## 差异算法

- **内容定义分块（CDC）**：Gear hash，min 2 KB / target 4 KB / max 16 KB；
- 旧文件按内容分块建立 `BLAKE3 → (offset,size)` 索引；
- 新文件按相同规则分块，命中索引则生成 `COPY`，否则累计为 `LITERAL`；
- 相邻且旧/新位置都连续的 `COPY` 自动合并；
- LITERAL 段最大 4 MB，使用标准库 `compress/flate` 压缩，压缩无收益时存原始数据。

该方案内存有界（索引 + 分块缓冲），可流式处理大文件；对整段位移的 ASAR 包也能保持 99% 以上的 COPY 命中率。

## 项目结构

```
├── cmd/
│   ├── cli/          # 补丁生成 CLI
│   ├── upgrader/     # 升级工具模板
│   └── restorer/     # 恢复工具
├── patcher/
│   ├── format.go     # GPBIN3 格式、边界安全编解码
│   ├── delta.go      # CDC 分块、差异构建与应用
│   ├── patch.go      # 打包、尾部定位、备份、升级
│   ├── restore.go    # 恢复清单与回滚
│   ├── hash.go       # BLAKE3 流式哈希
│   ├── safepath.go   # 路径穿越防护
│   └── fsutil.go     # 原子写入、空目录清理
├── test/             # 本地测试数据（已 gitignore，不入库）
└── Makefile
```

## 测试

```bash
go test -short ./...   # 单元测试
go test ./...          # 含 test/app.asar(.orig) 的真实集成与端到端 EXE 测试
```

`test/` 目录不在版本库中（文件约 300 MB）。若缺少测试数据，集成测试会自动跳过。

## 兼容性

- 当前版本仅支持 `GPBIN3` 目录树补丁格式。
- **不兼容**旧的 `GPBIN2`（含已移除的单文件模式）与 `GAMEPATCH1` 产物；旧补丁与旧恢复清单均不再支持。
- 已移除单文件补丁生成模式：`-old` / `-new` 必须是目录。
- Windows 上运行中的升级工具不能作为目标文件本身被替换。

## 技术栈

- Go 1.25（纯 Go，无 CGO）
- BLAKE3：`github.com/zeebo/blake3`
- 压缩：标准库 `compress/flate`
- 目标平台：Windows amd64（代码同样可在其他平台构建运行）

## 许可证

本项目采用 [MIT License](LICENSE)。
