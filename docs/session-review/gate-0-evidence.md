# Obsidian 项目上下文 v4：Gate 0 验收证据

## 结论

**LOCAL COMPLETE / WINDOWS CI PENDING**

本地 Gate 0 已在实现提交 `e3ff49beb6cb28d4aacb73a5ba4f45c43289b112` 上重新通过。合同矩阵现已覆盖 `conversation-chain-v1`、`problem-map-candidate-v1`、正式问题图、演进闭环和通用 Agent annotation。该提交未推送，也没有原生 Windows 运行，因此 Windows CI 证据仍为 PENDING。

## 审计对象

- 分支：`codex/obsidian-context-v4`
- 任务基准：`6421a9aad6d7a65bbaef2fa71e4d7e7be3431db6`
- 实现提交：`e3ff49beb6cb28d4aacb73a5ba4f45c43289b112`
- 环境：`Darwin arm64`，`go1.26.5 darwin/arm64`，Node `v24.18.0`，npm `11.16.0`
- 实现提交统计：43 files changed，2,977 insertions，80 deletions
- 未纳入任务提交：既有未跟踪目录 `.superpowers/brainstorm/`

## TDD 边界

规定 RED 命令：

```text
go test ./internal/conversationchain ./internal/problemmap ./internal/reviewv4 -count=1 && (cd obsidian-plugin && npx vitest run tests/contracts-v4.test.ts)
```

结果：FAIL（预期）。Go 编译器报告 `conversationchain.Parse/Render/Validate/Document/SourceRef`、`problemmap.ParseCandidates/ValidateCandidates/RenderCandidates/CandidateStore`、`reviewv4.ProblemNode` 和新增 v4 presentation 字段未定义；TypeScript 分支因 `&&` 未运行。

相同命令在实现后 PASS：`internal/conversationchain` 0.428s、`internal/problemmap` 0.555s、`internal/reviewv4` 0.310s；Vitest 1/1 file、57/57 tests。随后增加 canonical digest tamper mirror，最终聚焦合同文件为 58/58 tests。

## 完整本地门禁

按串行顺序执行：

| 命令 | 结果 | 证据 |
|---|---|---|
| `gofmt -w internal/conversationchain internal/problemmap internal/reviewv4 internal/cli` | PASS | 无输出 |
| `go test -p 1 -timeout 5m -count=1 ./...` | PASS | 57 个 package；较慢 package 包括 `internal/reviewjob` 89.595s、`internal/scan` 79.903s、`test/zerotoken` 62.772s，均低于每个测试二进制 5 分钟超时 |
| `go vet ./...` | PASS | exit 0，无输出 |
| `go mod tidy -diff` | PASS | exit 0，无 diff |
| `cd obsidian-plugin && npm run check` | PASS | lint；17/17 test files、122/122 tests；TypeScript typecheck；production bundle |
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

独立遍历 `testdata/contracts/v4/*.json` 并对同名插件 fixture 执行 `cmp -s`，结果为 **20/20 Go/plugin fixture files byte-identical**。

## 扩展合同与迁移边界

- 会话身份始终为 `(provider, session_id)`；conversation chain 只允许 user/assistant 可见 excerpt，4,096 UTF-8 bytes 上限，并绑定认证 source refs。
- 两个新增自摘要合同只省略各自的 `digest` 字段计算 canonical digest；valid fixtures 使用非零 digest，tamper tests 同时覆盖 Go/TypeScript。
- 正式问题图只存在于 `review-presentation-v4`；验证 parent/relation 存在、无环、每组 sibling order 唯一稳定、related/alternate 最多两个。
- 缺失结论必须为空文本并携带 typed reason；milestone annotation 使用通用 `confirmed_entity_id`、source-turn dependency，禁止 decision-only fields。
- v4/partial 兼容 fixture 仅增加空问题图、空 chain dependencies 和 neutral closed-loop 默认；对应 review hash 与 ledger self hash 已机械重算。v3 语义未改。
- 64 KiB per-source ceiling 仅冻结为 CLI 常量与显式 truncation coverage 合同；未实现 SourceAdapter 读取行为。

## Windows 证据状态

`.github/workflows/ci.yml` 中的 `windows-x64` / `windows-latest` 原生 job 尚未针对实现提交运行。没有 push、PR 或原生 Windows 结果，因此 Gate 0 状态不得升级为跨平台完成。

## 明确不属于 Gate 0 的后续工作

- conversation-chain segmentation 或 SourceAdapter 读取；
- problem placement service/store；
- Agent 执行；
- Obsidian UI 和真实 Vault 验收。
