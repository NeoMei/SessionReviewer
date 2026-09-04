# Obsidian 项目上下文 v4：Gate 0 验收证据

## 结论

**LOCAL COMPLETE / WINDOWS CI PENDING**

本地 Gate 0 已在最终实现提交 `d81e68a9fe4bc656ce5c29cb7369a4924a2d3a7b` 上重新通过。最终修正同步封闭了 Session 时间未知态、v3 人类决策状态无损迁移和价格替代图完整性三项 Important 问题。该提交未推送，也没有原生 Windows 运行，因此 Windows CI 证据仍为 PENDING。

## 审计对象

- 分支：`codex/obsidian-context-v4`
- 任务基准：`6421a9aad6d7a65bbaef2fa71e4d7e7be3431db6`
- 初始实现提交：`e3ff49beb6cb28d4aacb73a5ba4f45c43289b112`
- Fix Round 1 实现提交：`b30876db1d61026eb52f5d6d6533c052fd7a93b7`
- Final fix 实现提交：`d81e68a9fe4bc656ce5c29cb7369a4924a2d3a7b`
- 环境：`Darwin arm64`，`go1.26.5 darwin/arm64`，Node `v24.18.0`，npm `11.16.0`
- Fix Round 1 提交统计：18 files changed，279 insertions，39 deletions
- Final fix 提交统计：20 files changed，628 insertions，72 deletions
- 未纳入任务提交：既有未跟踪目录 `.superpowers/brainstorm/`

## TDD 边界

规定 RED 命令：

```text
go test ./internal/conversationchain ./internal/problemmap ./internal/reviewv4 -count=1 && (cd obsidian-plugin && npx vitest run tests/contracts-v4.test.ts)
```

结果：FAIL（预期）。Go 编译器报告 `conversationchain.Parse/Render/Validate/Document/SourceRef`、`problemmap.ParseCandidates/ValidateCandidates/RenderCandidates/CandidateStore`、`reviewv4.ProblemNode` 和新增 v4 presentation 字段未定义；TypeScript 分支因 `&&` 未运行。

相同命令在实现后 PASS：`internal/conversationchain` 0.428s、`internal/problemmap` 0.555s、`internal/reviewv4` 0.310s；Vitest 1/1 file、57/57 tests。随后增加 canonical digest tamper mirror，最终聚焦合同文件为 58/58 tests。

Fix Round 1 先新增回归测试并获得预期 RED：CLI 编译报告 `ParseProblemContractWithInput` 未定义；Go 分别证明零 digest、超过 JavaScript safe integer 上限以及 schema 的非空图 revision-zero 会被错误接受；TypeScript 为 2 failed / 57 passed。修正后聚焦 Go 五个 package 全部 PASS，`contracts-v4.test.ts` 为 59/59 PASS。完整 RED/GREEN 原始输出保存在 Task 7 report 的 `Fix Round 1` 节。

Final fix 先分别获得聚焦 RED：Go Session index 拒绝 `started_at:null`；v3 `已采用` 状态报告无精确 v4 mapping，v4 reader 拒绝未知的 `legacy_status_text`；整本账本验证错误接受缺失前驱、自指、环、身份不符和多有效叶子。TypeScript 对应为 3 failed / 59 skipped。修正后聚焦 Go `sessionindex/reviewv4/migrationv4/syncproject/memory` 全部 PASS，`contracts-v4.test.ts` 为 62/62 PASS。完整 RED/GREEN 原始输出保存在 final-fix report。

## 完整本地门禁

按串行顺序执行：

| 命令 | 结果 | 证据 |
|---|---|---|
| `gofmt -w` 所有变更的 Go 文件 | PASS | `gofmt` 后聚焦 Go 包和完整串行门禁均通过 |
| `go test -p 1 -timeout 5m -count=1 ./...` | PASS | 在 Final fix 精确提交树上全 package 重跑；较慢 package 包括 `internal/scan` 221.908s、`internal/reviewjob` 119.880s、`test/zerotoken` 103.223s、`internal/apply` 101.111s，均低于每个测试二进制 5 分钟超时 |
| `go vet ./...` | PASS | exit 0，无输出 |
| `go mod tidy -diff` | PASS | exit 0，无 diff |
| `cd obsidian-plugin && npm run check` | PASS | lint；17/17 test files、126/126 tests；TypeScript typecheck；production bundle |
| `git diff --check` | PASS | exit 0，无输出 |

独立 ordinary-flow 复核 `go test ./test/zerotoken -count=1 -run 'TestGate(A|B)' -v` PASS：Gate A 154/154 terminal、151 indexed、zero model tokens；Gate B 端到端发布与幂等测试通过。新增 deterministic candidate fixture 明确要求 `agent_run_id=null`，本任务没有启动或实现 Agent 执行。

## 十组合同比对

`go test ./internal/memory -run TestV4ContractFixtures -count=1 -v` 通过 10/10 命名子测试。Go 与 TypeScript 均使用封闭的五类拒绝码：`wire_input_overflow`、`wire_invalid_utf8`、`wire_json_invalid`、`wire_shape_invalid`、`wire_contract_invalid`。

| 合同 | invalid fixture 预期码 |
|---|---|
| review-presentation-v4 | `wire_shape_invalid` |
| machine-ledger-v4 | `wire_contract_invalid` |
| session-index-v1 | `wire_contract_invalid` |
| session-summary-v1 | `wire_shape_invalid` |
| session-event-page-v1 | `wire_contract_invalid` |
| agent-annotation-v1 | `wire_shape_invalid` |
| pricing-snapshot-v1 | `wire_contract_invalid` |
| pricing-supplement-v1 | `wire_contract_invalid` |
| conversation-chain-v1 | `wire_contract_invalid` |
| problem-map-candidate-v1 | `wire_contract_invalid` |

独立遍历 `testdata/contracts/v4/*.json` 并对同名插件 fixture 执行 `cmp -s`，结果为 **21/21 Go/plugin fixture files byte-identical**。

## 扩展合同与迁移边界

- `session-index-v1.started_at/ended_at` 现为显式可空；Go/schema/TypeScript 只把非 `null` 值计入 known coverage，并使用 null-last 规范排序。迁移投影把缺失时间写为 `null`，不补造时间。
- v4 Decision 新增封闭 `legacy_unmapped` 兼容状态和必填可空字段 `legacy_status_text`；兼容状态要求该字段保存精确原文且 `provenance=migrated`，原生 v4 状态要求它为 `null`。v3 只对精确 `active/archived` 做同名映射，其他文本不做语义猜测。
- `machine-ledger-v4` 的 Go/TypeScript 整本账本验证现在要求价格前驱存在、非自指、无环、无分叉、边两端用量身份一致，且每条用量最多一个被选中的有效叶子。历史快照仍不可变，未知成本仍为 `null`。

- 会话身份始终为 `(provider, session_id)`；conversation chain 只允许 user/assistant 可见 excerpt，4,096 UTF-8 bytes 上限，并绑定认证 source refs。
- 两个新增自摘要合同只省略各自的 `digest` 字段计算 canonical digest；valid fixtures 使用非零 digest，tamper tests 同时覆盖 Go/TypeScript。
- 正式问题图只存在于 `review-presentation-v4`；验证 parent/relation 存在、无环、每组 sibling order 唯一稳定、related/alternate 最多两个。
- 缺失结论必须为空文本并携带 typed reason；milestone annotation 使用通用 `confirmed_entity_id`、source-turn dependency，禁止 decision-only fields。
- v4/partial 兼容 fixture 仅增加空问题图、空 chain dependencies 和 neutral closed-loop 默认；对应 review hash 与 ledger self hash 已机械重算。v3 语义未改。
- 64 KiB per-source ceiling 仅冻结为 CLI 常量与显式 truncation coverage 合同；未实现 SourceAdapter 读取行为。
- `problem reorder` 的完整直接子节点顺序由最大 64 KiB、封闭且带 `schema_version=1` 的 stdin JSON 载荷携带；命令行仍不接受任意文件或路径输入。
- 空正式问题图允许 revision 0 并可作为第一次 apply 的 CAS 前像；非空图在 Go、TypeScript 和 JSON Schema 中都要求正 revision。
- 两个新增持久化合同在 parse 边界无条件验证 canonical digest，全零 digest 不再作为绕过值；新增/扩展整数线统一上限为 `9007199254740991`。

## Windows 证据状态

`.github/workflows/ci.yml` 中的 `windows-x64` / `windows-latest` 原生 job 尚未针对实现提交运行。没有 push、PR 或原生 Windows 结果，因此 Gate 0 状态不得升级为跨平台完成。

## 明确不属于 Gate 0 的后续工作

- conversation-chain segmentation 或 SourceAdapter 读取；
- problem placement service/store；
- Agent 执行；
- Obsidian UI 和真实 Vault 验收。
