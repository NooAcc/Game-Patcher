# 计划：压缩编解码器优化（编码器换 klauspost + 解码器复用）

## 1. 总体目标与范围
- 目标：解决 `compressLiteral` 使用 `flate.BestCompression` 导致的构建端瓶颈，并优化解码端。
- 范围：`patcher/delta.go`（编码器、解压器复用）、`patcher/apply.go`（复用器传递）、
  新增依赖 `github.com/klauspost/compress`、新增 `patcher/codec_test.go`。
- 用户授权：**不需要对旧版本代码/数据做兼容，也不要冗余**。
- 非目标：不改补丁线格式（本次**无需**升版本号）；不引入 CGO；不改 CDC 分块。

## 2. 当前阶段与进度
- 阶段：**已完成**。进度 100%（实现、测试、端到端验证、提交推送、CI 全部完成）。

## 3. 详细执行步骤
- [x] 选型实测：编码器 klauspost L6（构建 11~30x），解码器**不换** klauspost（+51 KB/份仅换 1.27x）
- [x] 解码器改用 `flate.Resetter` 复用（零体积成本，实测 1.2x），并验证读尽后可安全 Reset
- [x] 引入 `github.com/klauspost/compress v1.20.1`；级别抽为具名常量 `literalCompressLevel = 6`
- [x] 实现 `literalDecoder`（复用解压器），经 `opContext.literal` 传递；nil 时退化为一次性路径
- [x] `ApplyRelease` 创建并 `defer close` 复用器；确认其串行执行，无需加锁
- [x] 新增 `patcher/codec_test.go`：往返、分支选择、复用 vs 一次性等价（500 块）、
      `applyOps` 混合复用、**解码出错后不污染后续块**
- [x] gofmt / go vet / go build / cmd/cli / patcher 全量（**58 项，0 失败**）
- [x] 真实端到端：30 MiB 新可压缩内容，构建 1.471s → 0.209s；产物可正常应用且文件校验通过
- [x] 量化体积代价：升级器 +18 KB、恢复器 +13 KB（每份补丁 +31 KB）、CLI +74.5 KB
- [x] 提交并推送（commit d9155f8）；CI run 36821163327 成功，发布 Release v20261001-134430

## 4. 已发现的问题与风险
- **`flate.Resetter` 语义依赖**：文档未明确"读尽后可 Reset"。已实测通过，并写了
  `TestLiteralDecoderReuseMatchesOneShot`（500 块）与 `TestLiteralDecoderReuseAfterError` 固化。
- **复用器是可变状态**：依赖 `ApplyRelease` 串行这一前提，已在代码注释中明确标注；
  若将来并行应用，必须每 goroutine 一份。
- **载荷变大**：文本类载荷约 +12%。整包影响取决于载荷占比（实测 +5.1% @ 30 MiB 新内容）。
  级别已抽为常量，可回调到 8 换取更小载荷。
- 依赖新增：`klauspost/compress`（纯 Go，无 CGO），已 `go mod tidy`。

## 5. 已做出的决策与优化记录
- **编码器**：`klauspost/compress/flate` L6，标准 DEFLATE → **零线格式变更、零兼容性分支**。
- **解码器**：保留 stdlib `compress/flate`（体积最小）+ `Resetter` 复用；
  拒绝 klauspost 解码器（+51 KB/份仅换 1.27x，不划算）。
- **不升 formatVersion**：线格式未变，旧补丁仍可被新 CLI 读取；未加任何兼容/冗余分支。
- 名称与语义保持：`compressLiteral` / `copyLiteral` 签名仅按需调整，`CompRaw`/`CompFlate` 不变。

## 6. 下一步行动
- 无必做项。优化已合入 main，CI 成功并自动发布 Release。
- 可选：若认为载荷涨幅偏大，把 `literalCompressLevel` 调回 8（实测 1.8x 提速、载荷 +4.6%）。

## 7. 文件统计与进度追踪
- 改动：`patcher/delta.go`、`patcher/apply.go`、`go.mod`、`go.sum`、新增 `patcher/codec_test.go`、`plan.md`
- 测试：58 项全通过（新增 6 项编解码测试）

### 实测结果（Ryzen 5 9600X）
| 指标 | 优化前 | 优化后 | 提升 |
|---|---|---|---|
| 构建端压缩吞吐（30 MiB 文本） | 25.8 MB/s | **285.7 MB/s** | **11.1x** |
| 端到端构建（30 MiB 新内容） | 1.471 s | **0.209 s** | **7.0x** |
| 整包体积（同场景，未 strip 基线） | 13.59 MB | 14.28 MB | +5.1% |
| 解码吞吐（复用 vs 每 op 新建） | 基准 | **1.2x** | — |
| 每份补丁固定开销 | — | **+31 KB** | 可忽略 |

- 完成度：100%

## 8. 变更日志
- 2026-10-01 建立计划；完成编码器/解码器选型实测（klauspost L6、Resetter 复用、拒绝 kp 解码器）。
- 2026-10-01 验证 `flate.Resetter` 读尽后可安全 Reset；确认 `ApplyRelease` 串行可共享解压器。
- 2026-10-01 落地实现：编码器换 klauspost L6、新增 literalDecoder 复用、ApplyRelease 串行共享。
- 2026-10-01 新增 6 项编解码测试；全量 58 项通过；gofmt/vet/build 干净。
- 2026-10-01 端到端实测：30 MiB 新内容构建 1.471s → 0.209s（7.0x），产物应用并校验通过；
  每份补丁固定开销 +31 KB。
- 2026-10-01 提交并推送（d9155f8）；CI run 36821163327 成功，自动发布 Release v20261001-134430。任务完成。
