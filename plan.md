# Game-Patcher Adapter 架构升级实施计划

## 任务定义
- **任务性质**：可进行破坏性变更的架构升级任务。
- **核心目标**：新增 `adapter/` 适配器层，为不同游戏引擎提供专用增量优化；首个实现为 Godot PCK 适配器。
- **直接收益**：对 `temp` 中的 Godot 4.7.2 测试游戏，复用旧 PCK 中未变化文件，预计把当前约 139.64 MiB 的补丁降到约 13-15 MiB。
- **兼容策略**：非 Godot 游戏、PCK 不可读或适配器失败时，自动回退现有 CDC 字节级差分，不破坏原有升级能力。

## 1. 总体目标与范围
- 新增 `adapter/` 顶层包，定义引擎适配器接口、注册表和通用构建上下文。
- 新增 `adapter/godot/`，实现 Godot PCK V2/V3/V4 目录解析、标准 AES-256-CFB 目录解密、文件级 MD5 比对和增量操作规划。
- 修改 `patcher` 构建流程：在通用 CDC 之前先询问已注册适配器；适配器成功则使用适配器生成的 `OpCopy` / literal 操作。
- 保留现有 GPBIN4 容器和 `OpCopy` / `OpPoolRef` 操作，应用端无需理解 Godot 或密钥。
- 增加 CLI 配置入口：Godot AES key、适配器开关或等价配置。
- 增加单元测试和真实样本端到端验证。
- 本次允许破坏兼容性，但优先避免无必要的 wire-format 变更。

## 2. 当前阶段与进度
- 阶段：架构升级实施中。
- [x] 确认 Godot 旧 PCK 为标准 V4 加密目录
- [x] 使用给定 key 成功解密旧目录
- [x] 解析新 PCK V2 明文目录
- [x] 量化文件级差异：1870 个未变化、112 个变化、1 个新增
- [x] 确认理论压缩 payload 约 8.1 MB
- [x] 建立 `adapter` 接口与注册表（adapter 包测试通过）
- [x] 建立 Godot 适配器解析层（V2/V4 单测通过）
- [x] 接入 `patcher` 构建流程（适配器优先，失败回退 CDC）
- [x] 增加 CLI `-godot-key` 配置和交互输入
- [x] 完成单测与真实样本验证
- [x] 彻底移除 `auto` 适配器模式（代码、文档与最终回归均已完成）

## 3. 关键背景事实
- 旧 exe：`GDPC V4`、flags=`0x3`、file_base=`0x70`、目录加密、1982 个条目。
- 新 exe：`GDPC V2`、flags=`0x2`、目录明文、1983 个条目、全部文件未加密。
- 旧目录加密格式：
  - 目录偏移处先读 `file_count u32`（明文）；
  - 随后 `md5[16] + length u64 + iv[16] + AES-256-CFB128 密文`；
  - 解密后校验 MD5。
- 文件级比较结果：
  - 未变化：1870 个，133,641,088 B；
  - 变化：112 个，新大小 6,090,783 B；
  - 新增：1 个，8,278,565 B；
  - 删除：0 个。
- 新总明文：148,010,436 B；复用未变化文件后只需处理 14,369,348 B；DEFLATE 后约 8,019,014 B。
- 旧 PCK 文件目录中的 `md5` 是**原始明文 MD5**，因此比较文件是否变化不需要解密全部文件内容。
- 未变化文件可复用旧 exe 中的加密 blob；只需在新 PCK 目录中把该条目标记为 encrypted，并将 offset 指向旧 blob。

## 4. 架构设计
### 4.1 目录结构
```
adapter/
  adapter.go        # 接口、上下文、结果类型、注册表
  all/all.go        # 导入全部内置适配器，供 CLI side-effect 注册
  godot/
    adapter.go      # Godot 适配器实现
    pck.go          # PCK 头、目录、AES 解密和布局规划
    pck_test.go
    adapter_test.go
```

### 4.2 Adapter 接口
- `Context`：旧/新文件绝对路径、相对路径、大小、哈希、构建选项。
- `MatchResult`：是否匹配、置信度、原因、逻辑指纹。
- `FileDelta`：旧/新大小与哈希、操作列表、可选逻辑指纹。
- `Op`：`Copy`（从旧文件偏移复制）、`Literal`（新数据）、`CopyFrom`（跨文件复制）。
- `Adapter`：
  - `Name() string`
  - `Match(Context) MatchResult`
  - `Build(BuildContext) (*FileDelta, bool, error)`
- `Build` 返回 `ok=false` 表示适配器放弃，调用方回退 CDC；返回 error 表示真正构建失败。

### 4.3 注册与接入
- `adapter` 包持有全局注册表和顺序匹配逻辑。
- `adapter/all` 只负责导入内置适配器，CLI 通过 `_ "game-patcher/adapter/all"` 注册。
- `patcher` 在 `buildChunkFileEntry` 前调用 `adapter.Build`。
- 适配器成功后，patcher 将 `adapter.FileDelta` 转换为内部 `Entry` / `DeltaOp`，并复用共享块池。
- 适配器失败或未匹配时，保持现有 chunk/CDC 路径。

### 4.4 Godot 适配器行为
- 识别 exe 或 `.pck` 中的 `GDPC`。
- 解析 V2/V3/V4 头：
  - V2：目录直接在头部后；
  - V3/V4：读取 `dir_offset`；
  - `enc_directory` 时，在目录偏移处先读明文 `file_count`，再解密 `md5+length+iv+ciphertext`。
- 解析目录条目：`pathLen + path + ofs u64 + size u64 + md5[16] + flags u32`。
- 新旧条目按 path 建表，比较 `size + MD5`：
  - 相同：生成 `OpCopy`，复制旧文件的加密/原始 blob；
  - 变化或新增：生成 `Literal`，内容取新 PCK 中的文件字节；
  - 删除：无需输出。
- 生成新 PCK：
  - 保留新版 PCK header 的 version/engine/flags；
  - 重写 file_base 和目录 offset；
  - 未变化条目 `flags |= PACK_FILE_ENCRYPTED`，指向旧加密 blob；
  - 变化条目保持新版 flags，指向新 literal。
- 生成整个 exe 的 op 序列：
  - PE/PCK 前缀复用现有 CDC op；
  - 在 pck 起始处截断；
  - 追加新 PCK header、目录、文件数据和尾部 padding。

## 5. 破坏性变更与风险
- **版本链指纹风险**：适配器生成的 exe 可能与官方新版 exe 逐字节不同，导致后续链式补丁把玩家识别的目标版本与官方新版不一致。
  - 第一版处理：以适配器生成后的实际 exe hash 作为目标指纹；多版本链必须使用“应用补丁后的结果”作为下一次 `-old`，不能直接使用官方新版目录。
  - 后续可升级：为 `FileState` 增加逻辑指纹（PCK 内文件 path+MD5 的规范化哈希），让官方新版与生成版都被识别为同一版本。
- **混合加密 PCK**：复用旧 encrypted blob、变化文件使用新 plaintext；Godot 运行时支持 per-file 加密标志，但必须真机验证。
- **自定义保护**：第三方保护/自定义 PCK 不保证可解析，失败必须回退 CDC。
- **外部 `.pck`**：本阶段优先支持 exe 内嵌 PCK；外部 PCK 需要后续扩展 patch 多文件目标。
- **密钥安全**：key 只用于构建时解密旧目录；应用端补丁不嵌入 key。
- **签名/完整性校验**：如果游戏校验 exe 或 PCK 签名，重建 PCK 可能导致启动失败。

## 6. 详细实施步骤（TDD）
1. [ ] 建立 `adapter` 接口、注册表和测试骨架。
   - 先写失败测试：注册/匹配/回退语义。
2. [ ] 实现 PCK V2/V4 头解析和目录解析。
   - 先写失败测试：V2 明文目录、V4 加密目录（含给定 key）。
3. [ ] 实现 Godot 文件级比较和 op 规划。
   - 先写失败测试：未变化复用、变化 literal、新增 literal、删除忽略。
4. [ ] 实现新 PCK header/目录重写。
   - 先写失败测试：offset、file_base、flags、path 长度和总大小。
5. [ ] 将 adapter.Build 接入 `patcher` 构建流程。
   - 先写失败测试：匹配到适配器时不再生成 148 MB literal；未匹配时回退 CDC。
6. [ ] 增加 CLI key 配置和内置适配器注册。\n   - adapter 包接口/注册表已完成，测试通过。
   - 先写失败测试：key 解析、注册后真实调用。
7. [ ] 真实样本端到端验证：
   - 生成 patch；
   - 应用 patch；
   - 解析生成的 PCK；
   - 校验逻辑文件 MD5 与官方新版一致；
   - 测量补丁大小。
8. [x] 更新 README 和架构说明，记录破坏性变更与多版本链限制。

## 7. 已做出的决策与优化记录
- 2026-10-12：确认旧目录加密格式为 `file_count` 明文 + `md5+length+iv+ciphertext`，修正最初偏移 4 字节的误判。
- 2026-10-12：采用“复用旧加密 blob、变化文件存新明文”的方案，应用端不需要 key。
- 2026-10-12：采用顶层 `adapter/` 包隔离引擎逻辑，避免 `patcher` 与具体引擎互相依赖。
- 2026-10-12：允许破坏性变更；优先保留现有 GPBIN4 wire format，减少 apply 端改动。
- 2026-10-12：第一版接受“生成版 hash 作为目标指纹”限制，多版本链改用应用后的结果作为下一段 old。

## 8. 实施结果（真实样本）
- 适配器注册后，`temp/old -> temp/new` 生成的补丁从 **139.64 MiB** 降到 **13.15 MiB**。
- 应用补丁成功，目标 exe 大小 231,302,848 B；官方新版 231,249,328 B，差 53,520 B（布局 overhead）。
- 应用后 PCK 解析结果：
  - 1983/1983 个逻辑文件与官方新版一致；
  - 1870 个未变化文件复用旧加密 blob；
  - 113 个变化/新增文件使用新版明文；
  - 逻辑明文总量 148,010,436 B，与官方新版一致。
- `go test ./...` 全部通过。

## 9. 下一步行动
- 创建 `adapter` 包接口和注册表，先写失败测试。
- 然后实现 Godot PCK 解析器，并用给定 key 对 `temp/old` 做真实解析验证。
- 最后接入 `patcher` 并进行完整端到端补丁生成、应用和体积验证。

## 10. 文件统计与进度追踪
- 新增目录：`adapter/`。
- 首个适配器：`adapter/godot/`。
- 预计新增文件：5-8 个 Go 源文件/测试文件。
- 预计修改：`patcher/build.go`、`patcher/chunkbuild.go`、`cmd/cli/main.go`、`README.md`。
- 真实样本验证：`temp/old` 与 `temp/new`。
- 当前完成度：架构设计 100%，代码实现 100%，真实端到端 100%，文档 100%。

## 11. 变更日志
- 2026-10-12：将任务重定义为可破坏性架构升级任务。
- 2026-10-12：写入 adapter 目录架构、Godot 适配器设计、TDD 步骤、版本链风险和回退策略。

- 2026-10-12：完成 adapter 接口、注册表、匹配排序和回退语义；go test ./adapter 通过。


- 2026-10-12：完成 adapter 接口/注册表、Godot PCK 解析与增量构建、patcher 接入和 CLI key。
- 2026-10-12：真实样本补丁 139.64 MiB -> 13.15 MiB；应用后 1983/1983 逻辑文件校验通过；`go test ./...` 通过。

- 2026-10-12：README 增加 adapter/Godot PCK 说明、`-godot-key` 用法、回退策略和实测体积。
- 2026-10-12：`go vet ./...` 与 `go test ./...` 均通过。
- 2026-10-12：最终回归通过：补丁 13.15 MiB，应用成功，1983/1983 逻辑文件无差异；1870 个复用旧加密 blob，113 个变化/新增为明文。


## 12. TUI 适配器选择（已实现）
### 12.1 目标
- 交互模式下，用户填完“旧文件夹、新文件夹、附加旧补丁”后，必须显式选择本次构建使用的适配器。
- 菜单选项只包括：各已注册具体适配器、禁用适配器（强制通用 CDC）。
- 非交互/脚本模式使用 `-adapter` 显式指定，默认值为 `none`。

### 12.2 实现方案
- `adapter.Options.Values` 使用选择键 `adapter.selected`。
  - 空值或 `none`：禁用全部适配器；其他值：只允许同名适配器。
- `adapter.Registry.Match` 根据选择键严格过滤，不再遍历所有适配器做自动选择。
- `Adapter` 新增可选 `Described` 接口用于 TUI 显示名称。
- CLI 参数为 `-adapter <none|名称>`。
- 交互模式在旧/新目录和附加补丁输入后，调用 TUI 菜单选择适配器。
- 只有显式选择 `godot` 时才询问 `-godot-key`；禁用适配器时跳过 key 提示。

### 12.3 测试
- 注册表单测：指定 adapter 与 `none`/空值的过滤语义。
- CLI 单测：默认与空值映射为 `none`，显式 Godot 映射为 `godot`。
- 真实样本回归：TUI 选择 Godot 后仍生成约 13.15 MiB 补丁。

### 12.4 进度
- [x] adapter 选择过滤（指定名称/none，注册表单测通过）
- [x] TUI 菜单（交互模式在旧/新目录和附加补丁后显式选择）
- [x] CLI `-adapter <none|name>` 参数，默认 `none`
- [x] 真实样本回归：显式 godot 为 13.15 MiB，none 回退 139.64 MiB

- 2026-10-12：新增 TUI 适配器选择菜单和 `-adapter` 参数；真实样本显式 godot 13.15 MiB、none 139.64 MiB。

## 13. Auto 适配器选择机制（历史记录，已移除）
- 早期 `auto` 并不是可靠的“游戏级引擎识别”，而是逐文件询问所有已注册 adapter 的 `Match`。
- 这会导致同一构建中的不同文件理论上可能走到不同 adapter，也没有统一的引擎指纹、冲突提示或游戏级锁定机制。
- 由于无法准确判断引擎，已决定彻底移除 `auto` 模式；本节仅保留历史背景，不再代表当前行为。
- 当前行为统一为：未显式选择时使用 `none`，显式选择时才运行对应 adapter；adapter 拒绝处理时回退通用 CDC。

## 14. 移除 auto 模式（已完成）
### 14.1 决策
- 彻底移除 `auto` 适配器模式，因为当前无法可靠完成游戏级引擎识别。
- 新语义：
  - 交互模式必须在 TUI 中显式选择具体 adapter 或“禁用适配器”；
  - 非交互模式 `-adapter` 默认值为 `none`；
  - 未显式选择 adapter 时，一律走通用 CDC；
  - `auto` 是非法值，`adapter.selected` 空值等价于 `none`。

### 14.2 执行清单
- [x] 删除 `adapter.SelectedAuto` 常量及相关分支；
- [x] Registry 对空值/`none` 返回无匹配，指定值只匹配同名 adapter；
- [x] TUI 菜单移除“自动选择”，保留具体 adapter 和“禁用适配器”；
- [x] CLI `-adapter` 默认值改为 `none`，并拒绝 `auto`；
- [x] Godot key 只在显式选择 `godot` 时询问；
- [x] 更新单测、README 和计划文档；
- [x] 最终执行 `gofmt`、`go test ./...`、`go vet ./...` 与真实样本回归。

### 14.3 验收标准
- `go test ./...`、`go vet ./...` 通过；
- 菜单和帮助文本中不再出现 `auto`/“自动选择”；
- 不传 `-adapter`：生成约 139.64 MiB 的通用 CDC 补丁；
- `-adapter godot -godot-key <key>`：生成约 13.15 MiB 补丁；
- `-adapter auto`：明确报“未知适配器”错误。

### 14.4 变更日志
- 2026-10-12：决定并完成 `auto` 模式移除；生产代码、CLI、TUI、单测和用户文档已同步。
- 2026-10-12：最终回归完成：`go test ./...`、`go vet ./...` 通过；`-adapter auto` 以退出码 1 报未知适配器；默认 `none` 生成 139.64 MiB CDC 补丁；显式 `-adapter godot` 生成 13.15 MiB 补丁。

## 15. Bubble Tea TUI 样式重构（设计待确认）
### 15.1 总体目标与范围
- 使用 `github.com/charmbracelet/bubbletea` 彻底替换当前基于 `bufio`、手动 raw mode 和 ANSI 清屏的交互界面。
- 将旧目录、新目录、附加旧补丁、适配器选择、Godot key、输出路径、确认构建整合为一个全屏多步向导。
- 保持所有 CLI 参数、脚本模式、业务校验和补丁生成逻辑不变。
- 允许引入 Bubble Tea 生态组件；计划使用 `bubbles/textinput`、`bubbles/list` 和 `lipgloss` 实现输入、列表和视觉样式。

### 15.2 交互设计（待用户确认）
- 全屏界面：顶部标题与步骤指示，中间为当前步骤，底部为快捷键提示。
- 步骤 1/6：旧版本目录；步骤 2/6：新版本目录；步骤 3/6：附加旧补丁。
- 步骤 4/6：显式选择适配器（Godot 或禁用适配器），不提供 `auto`。
- 仅在选择 Godot 时显示步骤 5/6：Godot AES key；选择禁用适配器时自动跳过。
- 最后一步：输出路径与确认摘要，Enter 开始构建，Esc 返回，Ctrl+C 退出。
- 所有路径输入继续复用 `cleanPath`，目录存在性、适配器选择和 key 格式校验保持在提交前执行。
- TUI 只负责收集配置；离开 TUI 后沿用现有 `patcher.CreatePatchWithOptions` 和构建日志，避免破坏业务核心。

### 15.3 计划文件与测试
- 新增 TUI 模型文件，逐步替换 `cmd/cli/menu.go` 和 `main.go` 中的交互 prompt。
- 为纯模型逻辑增加单元测试：步骤推进/回退、校验错误、Godot 条件步骤、取消行为、最终配置生成。
- 保留非终端/CI 的降级路径，避免测试和自动化依赖真实终端。
- 验收：`go test ./...`、`go vet ./...` 通过；`-shell` 在真实终端可完成向导；脚本参数行为不变。

### 15.4 当前进度
- [x] 调研现有 TUI 结构和 Bubble Tea 接入点
- [x] 用户确认设计并允许破坏性变更
- [x] 引入 Bubble Tea / Bubbles / Lip Gloss 依赖
- [x] 实现 CLI 全屏多步配置向导（路径、补丁链、适配器、条件 Godot key、输出、确认）
- [x] 替换现有交互流程并删除 `cmd/cli/menu.go` 中手写 raw mode 菜单
- [x] 额外迁移 `patcher/select.go` 的文件选择界面，移除第二套手写 ANSI/raw mode 实现
- [x] 模型级单测、`go test ./...`、`go vet ./...` 通过
- [x] 真实终端验收：向导渲染与 Ctrl+C 取消正常；文件选择器确认后生成 13.15 MB 测试补丁；完整向导按键流程由 Bubble Tea Program 级测试覆盖

### 15.5 风险
- Bubble Tea 接管终端输入输出后，Windows 下需要验证 Ctrl+C、Esc、方向键和中文输入。
- 现有构建阶段直接输出日志，TUI 切换到普通终端时不能丢失回滚/错误信息。

### 15.6 变更日志
- 2026-10-12：创建 Bubble Tea TUI 重构设计初稿；已分类为现有交互流程的有界重构，等待设计确认后实施。

- 2026-10-12：完成 Bubble Tea 迁移。CLI 配置向导和变更文件选择器均改用 Bubble Tea；`cmd/cli/menu.go` 与 `patcher/select.go` 不再使用手写 raw mode/ANSI 重绘。`go test ./...`、`go vet ./...` 通过，真实终端验收待执行。
- 2026-10-12：真实终端验收完成。`-shell` 全屏向导正常渲染并支持取消；脚本模式进入 Bubble Tea 文件选择器，Enter 确认后生成 13.15 MB Godot 补丁。`go test ./...`、`go vet ./...`、`git diff --check` 均通过。
- 2026-10-12：Bubble Tea TUI 与 adapter 架构升级提交为 `82f7fe2`，并已推送到 `origin/main`。
