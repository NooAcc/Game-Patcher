# 计划：移除 Label 字段 + 支持带双引号的路径

## 1. 总体目标与范围
- 目标一：从补丁制品中**完全移除 `Label`（版本名称）字段**——不同版本游戏目录名差异很大，该字段只写不读，无判定/显示价值。
- 目标二：命令行与交互模式**接受带双引号的路径**（Windows 资源管理器"复制地址"会带 `"..."`）。
- 范围：`patcher` 制品模型/编解码/检测、CLI 参数与交互输入、测试、README 与 ADR-0001。
- 治理判定：删除死字段 = **delete-first**（用户已明确要求移除）。

## 2. 当前阶段与进度
- 阶段：**已完成**。进度 100%（实现、验证、文档同步全部完成；待提交/推送）。

## 3. 详细执行步骤
- [x] 删除 `VersionRef.Label`、`SourceMismatch.Label`
- [x] `chain.go`：`checkSteps`/`deriveVersionRefs`/`encodeSteps`/`decodeSteps` 去掉 label 段
- [x] `ChunkPayload` 去掉 `Labels`，`NewChunkPayload(steps, pool)`
- [x] `build.go` 删除 labels 收集与 `versionLabel()`
- [x] 线格式 `formatVersion` 4 → 5；`legacyFormatVersion=4` 在 payloadKind 分派前明确报错提示重新生成
- [x] 删除随之不可达的 `removedPayloadChain` 专用分支（v4 闸门已覆盖全部旧制品）
- [x] 适配全部单元/集成测试；新增 `TestDecodeRejectsLegacyFormatVersion`、`TestVersionDisplayUsesOrdinalOnly`
- [x] 更新 README 制品图、Op 列表、兼容性；修订 ADR-0001（第二次修订，含陈旧表述清理）
- [x] CLI：新增 `cleanPath`（剥成对双引号）+ `splitPathList`（忽略引号内逗号），应用到 9 处输入点
- [x] CLI 测试：`TestCleanPath`、`TestSplitPathListIgnoresCommasInsideQuotes`、`TestStringListAcceptsQuotedCommaPaths`、`TestPromptPathAcceptsQuotedPath`、`TestResolvePrevPatches` 补引号用例
- [x] gofmt 内容检查（LF 临时文件比对）→ 修复 4 个文件缺失尾换行
- [x] `go vet ./...` + `go build ./...`
- [x] `go test ./cmd/cli/` + patcher 全量测试（48 通过 / 0 失败）
- [x] 真实 CLI 端到端手测（带双引号、含逗号目录名）：生成 fix1(v1)/fix2(v2) + 应用 A→C / B→C + 交互模式
- [ ] 提交并推送

## 4. 已发现的问题与风险
- **本地环境限制（非代码问题）**：本机安全策略阻止执行 `go-build` 临时目录下新生成的 `patcher` 测试二进制
  （`fork/exec ... test.test.exe: Access is denied`），`cmd/cli` 不受影响。
  绕行：`go test -c -o build/testbin/x.test.exe ./patcher/` 后手工执行。CI 干净 runner 不受影响。
- **破坏性变更**：`formatVersion=4` 制品（仍含版本名称）将被新 CLI 明确拒绝并要求重新生成；已在 README/ADR 记录。
- **发布副作用**：该仓库 push 到 main 会自动发布 Release，推送将产生新版本，需提醒用户。
- 行尾约定 CRLF（`core.autocrlf=true`）；gofmt 会规范化行尾，故用 LF 临时文件比对后再写回 CRLF。

## 5. 已做出的决策与优化记录
- 版本识别仍靠内容指纹（存在性/大小/BLAKE3）回放推导，**不占存储**；删除 Label 不改变检测正确性。
- 线格式因删字段升至 5，并对 v4 给出专门迁移提示（而非笼统"不支持"）。
- `cleanPath` 保守：仅剥成对双引号（Windows 路径中双引号本身非法），不配对的引号保留，避免误伤。
- `splitPathList` 按引号感知拆分，支持"含逗号的目录名"列表输入。
- 判定 `payloadKind=1` 的专用诊断分支已不可达（旧制品均为 v4，先被 v4 闸门拦下），故随死代码一并删除，并同步修正 ADR 的陈旧表述。

## 6. 下一步行动
1. 提交（`fix!: drop version labels and accept quoted paths`）并推送
2. 确认 CI 绿（`gh run list --limit 1`）

## 7. 文件统计与进度追踪
- 变更文件：17（Go 源码 8、测试 7、文档 2）+ plan.md
- 代码净变更：约 +216 / -152
- 完成度：100%

## 8. 变更日志
- 2026-10-01 创建本计划（Label 移除 + 双引号路径）。
- 2026-10-01 实现完成：模型/编解码/构建/检测去 label；线格式升至 5；CLI 引入 cleanPath/splitPathList；测试与文档同步。
- 2026-10-01 gofmt 内容检查通过（修复 4 个文件缺失尾换行的格式问题）。
- 2026-10-01 验证：go vet/build/CLI 测试/patcher 48 项测试全通过；真实端到端（含逗号目录名 + 双引号、A→C 与 B→C、交互模式）通过。
- 2026-10-01 清理 ADR 两处陈旧表述（备选方案里的"版本标签"、`payloadKind=1` 专用分支说明），使其与实际实现一致。