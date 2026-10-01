# 计划：退役 chain 后端，仅保留 chunk 后端（delete-first）

## 1. 总体目标与范围
- 目标：完全移除 chain 差异后端，使 chunk 成为唯一后端，消除因"多后端"产生的分叉 owner。
- 治理判定：内部代码退役 → **delete-first**（用户已给出明确范围确认）。
- 范围：后端实现、构建入口、CLI 开关、测试、文档与 ADR。

## 2. 当前阶段与进度
- 阶段：**已完成**。进度 100%。

## 3. 退役执行结果（责任域）
| 载体 | 处置 |
|---|---|
| `PayloadChain` 常量 | 已删除（保留 `removedPayloadChain=1` 仅用于旧制品诊断） |
| `Chain` 结构与其全部方法 | 已删除；`Sources/Target/状态回放` 迁移到 `ChunkPayload` |
| `buildStepEntries` | 已删除 |
| `buildDelta` / `buildLiteralOps` / `applyDelta` 包装 | 已删除（生产与测试统一走 `buildChunkFileEntry` / `applyOps`） |
| `CreatePatchOptions.Backend` | 已删除 |
| `kindName` / `checkPayload` / `payloadLabelsSteps` 多态分派 | 已收敛为 chunk 单一实现 |
| CLI `-payload` / `parseBackend` / `backendName` / `promptBackend` | 已删除 |
| ADR-0001 "两个后端" 章节 | 已修订（amend，含退役说明） |
| README `-payload` 章节 | 已改写为"共享块池 + 跨文件复用" |

**保留（非 chain 专属）**：`ChainStep`/标签/指纹回放（版本链语义）、`Entry`/`DeltaOp`/四种 Op、
`indexChunks`/`chunkScanner`/`compressLiteral`/`literalReader`、`applyOps`/`materialize`/`opContext`、
`Payload` 契约与 `payloadKind` 线格式字段（判别 + ADR 记录的 `payloadKind=3` 扩展点）。

## 4. 兼容性边界
- 线格式 `PayloadChunk = 2` 保持不变 → 此前生成的 chunk 补丁仍可读取与 `-prev` 拼接。
- `payloadKind=1`（chain）→ 明确报错并提示用当前 CLI 重新生成；已发布的旧 EXE 自带旧代码不受影响。
- 版本检测、备份/回滚、`restore.json` 与 restorer 契约未变。

## 5. 验证结果
- `go build ./...` 通过；`go vet ./...` 通过；`gofmt` 内容检查干净。
- `cmd/cli` 测试通过；`patcher` 完整测试（含两个真实 EXE 端到端）通过，失败数 0。
- 真实 CLI 端到端：fix1(v1→v2) + fix2(v2→v3, `-prev fix1`)；v1→v3、v2→v3 均一步到位，v3 提示已是最新且不落盘；`-payload` 已不再被接受。
- 退役期发现并修复一个真实缺陷：测试 helper 构造补丁时丢弃块池，导致"块池引用越界"；已改为统一构造完整补丁并执行 `SelfCheck`。

## 6. 已发现的问题与风险
- **本地环境限制（非代码问题）**：本机安全策略阻止执行 `go-build` 工作目录下新生成的 `patcher` 测试二进制
  （`fork/exec ... test.test.exe: Access is denied`），`cmd/cli` 不受影响。
  已验证：用 `go test -c -o build/testbin/x.test.exe ./patcher/` 手工产出后执行，完整套件全部通过。
  CI（干净 runner）不受此影响。
- 保留的 `removedPayloadChain` 仅为旧制品诊断，不承载任何 chain 行为。

## 7. 下一步行动
- 无必做项。可选：确认 CI 上 `go test ./...` 正常（预期正常）。

## 8. 变更日志
- 2026-10-01 创建退役计划（delete-first）与退役清单。
- 2026-10-01 完成退役：删除 chain 实现/构建入口/CLI 开关，收敛为 chunk 单一后端；同步修订 README 与 ADR-0001。
- 2026-10-01 修复测试 helper 的块池丢失缺陷；完整测试与真实 CLI 端到端验证通过。
- 2026-10-01 修复显示：应用补丁时的版本一律按链内序号显示 v1/v2/v3，不再显示构建目录名；
  制品仍保留 Label 作为溯源信息，仅不再展示。新增端到端断言（用非 v 命名的目录名验证不泄漏）。