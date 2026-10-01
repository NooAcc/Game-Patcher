# 🎮 Game-Patcher — 二进制增量升级包工具

对比游戏的两个版本，生成**单文件升级工具**。对发生变化的文件只写入**二进制差异数据**（COPY 旧文件区段 + LITERAL 新数据），玩家双击升级工具即可完成升级，无需安装运行时。

## 特性

- **二进制增量修补** — 变化文件不再整文件打包，只嵌入差异数据
- **多版本补丁链** — 一个升级工具可携带多个连续版本的差异；应用时自动识别玩家当前版本，一步升级到最新版本
- **共享块池 + 跨文件复用** — 所有段的字面量去重存放，并复用被删除文件的块；重命名/移动场景体积显著更小
- **单文件分发** — 升级工具本身就是一个可执行文件，零运行时依赖
- **内容定义分块（CDC）** — 对插入、删除、整体位移不敏感
- **并行哈希与差异计算** — 只对选中的变更文件计算差异，自动并行处理
- **生成前勾选文件** — 扫描后列出全部变更文件，↑/↓ + 空格选择要包含的项，回车生成
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

如需在已有补丁基础上追加新版本，用 `-prev` 附加旧补丁（见 [多版本升级（补丁链）](#多版本升级补丁链)）。

扫描完成后会列出所有变更文件（默认全选），可先剔除不需要打包的文件：

```
❯ [x] ~ resources/app.asar      306.27 MB → 306.45 MB
  [x] + resources/new.bin               12.40 MB
  [ ] - resources/old-cache.dat          8.10 MB

↑/↓ 移动   空格 选择/取消   A 全选/反选   Enter 生成   Q 取消
```

- 被剔除的文件不会写入补丁：`update` 保持旧文件、`add` 不新增、`delete` 不删除；
- 非交互终端（管道 / CI）会自动包含全部变更文件，保持脚本可用。

**交互模式**：

直接双击 `game-patcher-cli-win64.exe`（不传任何参数）就会进入交互模式，按提示依次输入新旧版本目录即可：

```bash
game-patcher-cli-win64.exe            # 双击 / 无参数启动，默认交互模式
game-patcher-cli-win64.exe -shell     # 显式进入交互模式
```

> 双击启动时，程序结束前会提示「按回车键退出」，方便查看结果；
> 无参数但标准输入不是终端（管道 / CI）时仍打印用法并以非零状态退出。

> 生成的 EXE 需要 `game-patcher-upgrader-win64.exe` 作为基础程序、可选地需要
> `restorer-win64.exe` 作为恢复工具。默认在 CLI 同目录自动查找，也可用
> `-upgrader` / `-restorer` 显式指定。

把生成的升级工具放到**游戏根目录**，直接运行即可。

## 多版本升级（补丁链）

补丁以"链"的形式携带连续版本的差异：`v1→v2`、`v2→v3`……应用时自动识别玩家当前处于链上哪个版本，并从该版本起按序应用，一步到达最新版本。

```bash
# 1) 先用 v1、v2 生成 fix1（补丁版本号 1）
game-patcher-cli-win64.exe -old ./v1 -new ./v2 -out ./fix1.exe

# 2) 再用 v2、v3 生成 fix2，并附加 fix1（补丁版本号自动变为 2）
game-patcher-cli-win64.exe -old ./v2 -new ./v3 -prev ./fix1.exe -out ./fix2.exe
```

生成的 `fix2.exe` 同时包含 `v1→v2` 与 `v2→v3` 两段差异：

- 玩家是 **v1** → 依次应用两段，直接到 v3；
- 玩家是 **v2** → 只应用 `v2→v3`，到 v3；
- 玩家已是 **v3** → 提示"已经是最新版本"，不做任何修改；
- 都不是 → 列出每个候选版本的失败原因并中止（不修改任何文件）。

要点：

- `-prev` 可重复传入或用逗号分隔；多份旧补丁必须属于同一条版本链，否则报错。
- 生成时会做**接缝校验**：旧补丁的目标状态必须与本次 `-old` 目录一致，避免把链接错版本。
- 补丁版本号自动递增（无 `-prev` 为 1，否则为前缀版本号 +1）。
- 版本识别基于"补丁触及文件"的存在性与 BLAKE3，与原有按文件哈希校验的安全模型一致。

### 差异存储：共享块池 + 跨文件复用

补丁只有一个差异后端：所有段的字面量放进去重的**共享块池**，并可**跨文件复用**"本段被删除文件"的块。

- 重命名 / 移动大文件时，新文件直接复用旧文件的块，补丁体积几乎不增加。
- 同一份新增内容在多段中出现时，块池只存一份。
- 同段内先执行新增/修改、最后执行删除，以保证被删除文件可作为跨文件复用源。
- 代价：为"本段被删除的文件"建立块索引的成本按**删除字节量**支付，即使这些内容最终未被复用。

> 旧版的 `chain`（内联差异）后端已移除。用它生成的补丁会被明确拒绝并提示重新生成；
> 已发布的旧升级工具自带旧代码，仍可正常使用。

架构决策、取舍与被否方案见 [ADR-0001](docs/adr/0001-gpbin4-multi-version-patch-chain.md)。

## 升级流程

1. 从自身尾部读取并解析 `GPBIN4` 补丁数据；
2. **自动识别玩家当前版本**：按版本从早到晚逐个比对补丁触及文件的存在性与 BLAKE3，取第一个匹配的版本作为升级起点（已是最新版本则提示后退出）；
3. 显示检测到的源版本、目标版本与统计，请求确认；
4. 从该起点开始逐段应用差异：每段先校验源状态，再备份尚未备份过的文件（备份内容即链起点状态）；
5. 对每个目标：COPY 旧文件区段 + 写入 LITERAL 数据 → 临时文件 → 大小/哈希校验 → 原子替换；delete 条目则删除并清理空目录；
6. 全部段完成后按目标版本指纹整体校验；失败时用已累积的备份回滚到链起点。

## 恢复旧版本

升级后备份位于目标目录下的 `_backup_before_patch/`，其中包含 `restorer.exe` 与 `restore.json`。进入该目录运行恢复工具即可：

```bash
cd _backup_before_patch
restorer.exe
```

恢复工具以“备份目录的父目录”为游戏根，逐项：
- `add` → 删除升级时新增的文件；
- `update` / `delete` → 从备份恢复，并校验恢复后的 BLAKE3 与升级前一致。

## 二进制补丁格式（GPBIN4）

生成的升级工具是：

```
┌────────────────────────────────────────────┐
│  upgrader 基础 EXE                          │
├────────────────────────────────────────────┤
│  GPBIN4 制品                                │
│    "GPBIN4"       6B                        │
│    formatVersion  1B (=5)                   │
│    payloadKind    1B (=2)                   │
│    patchVersion   uint32 LE  补丁版本号      │
│    stepCount      uint32 LE                 │
│    Step[]         每段 sourceIndex + Entry[]│
│    chunkCount     uint32 LE  共享字面量块数   │
│    Blob[]         comp+rawHash+len+data     │
├────────────────────────────────────────────┤
│  blobLen          uint64 LE                 │
│  "GPBIN4END!"     10B                       │
├────────────────────────────────────────────┤
│  [可选] restorer 二进制                      │
│  restorerLen      uint64 LE                 │
│  "GPBIN4RST!"     10B                       │
└────────────────────────────────────────────┘
```

一个版本链由若干段组成，第 i 段表示"版本 i → 版本 i+1"的差异；每段由若干 `Entry` 组成。

**版本只用链内序号标识（v1、v2、v3…），制品中不记录任何版本名称。** 应用端用于识别版本的
指纹（各版本下被触及文件的存在性 / 大小 / BLAKE3）由各段的 old/new 哈希回放推导，不占存储。

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

每个 `Op`（`kind uint8` 开头）：

- `COPY`(=1)：`oldOffset uint64 + length uint64`，从**该文件**的旧内容复制区段；
- `LITERAL`(=2)：`comp uint8 + rawLen uint64 + dataLen uint64 + data`，内联字面量（应用器支持，当前构建器统一改用 `POOLREF`）；
- `COPYFROM`(=3)：`srcPath + oldOffset uint64 + length uint64`，从**同一版本树中的另一个文件**复制区段，用于复用"本段被删除文件"的块；
- `POOLREF`(=4)：`poolIndex uint32 + length uint64`，引用共享字面量池中的块。

每个 `Blob`：`comp uint8 + rawHash[32] + rawLen uint64 + dataLen uint64 + data`。
块池按 `rawHash`（原始内容的 BLAKE3）去重，因此同一份内容在多段中出现时只存一份。

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
│   ├── model.go      # FileState / VersionRef / Release / Payload 契约
│   ├── format.go     # GPBIN4 制品信封、边界安全编解码
│   ├── chain.go      # 版本链段结构与各版本指纹回放推导
│   ├── chunk.go      # 补丁后端（版本链 + 共享块池）编解码与自检
│   ├── chunkbuild.go # 块池去重、删除文件块索引与差异构建
│   ├── detect.go     # 应用端版本自动检测与整体校验
│   ├── build.go      # 构建、-prev 附加、版本号递增、接缝校验
│   ├── scan.go       # 目录扫描与变更清单
│   ├── carrier.go    # EXE 载体拼装与尾部定位
│   ├── apply.go      # 逐段校验/备份/应用与回滚
│   ├── delta.go      # CDC 分块、差异构建与应用
│   ├── select.go     # 生成前的变更文件勾选界面
│   ├── patch.go      # RunEmbedded 编排与输出
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

- 当前版本使用 `GPBIN4` 链式制品格式（支持多版本），线格式版本 `formatVersion=5`。
- 读到 `formatVersion=4` 的旧制品会被明确拒绝并提示用当前 CLI 重新生成（旧版会写入版本名称字段）。
- **不兼容** `GPBIN3` / `GPBIN2` 生成的旧补丁（已发布的旧 EXE 自带旧代码不受影响，但新 CLI 无法把旧补丁作为 `-prev` 读取）；`GAMEPATCH1` 同样不再支持。
- 仅支持目录模式：`-old` / `-new` 必须是目录。
- `restore.json` 与恢复工具的契约保持不变：`add` = 删除新增文件，`update`/`delete` = 从备份还原。
- Windows 上运行中的升级工具不能作为目标文件本身被替换。

## 技术栈

- Go 1.25（纯 Go，无 CGO）
- BLAKE3：`github.com/zeebo/blake3`
- 压缩：标准库 `compress/flate`
- 目标平台：Windows amd64（代码同样可在其他平台构建运行）

## 许可证

本项目采用 [MIT License](LICENSE)。
