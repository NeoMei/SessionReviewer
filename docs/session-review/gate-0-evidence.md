# Obsidian 项目上下文 v4：Gate 0 验收证据

## 结论

**SUPERSEDED BY CONTRACT EXTENSION / GATE 0 REOPENED**

以下记录仍证明原八组合同在当前 macOS 工作树通过，但 2026-09-04 后续确认的 `conversation-chain-v1`、`problem-map-candidate-v1`、正式问题图和演进闭环扩展尚未包含在该矩阵中。Gate 0 因此重新打开；必须完成 `2026-09-04-obsidian-context-gate-0-contracts.md` Task 7 并重跑完整门禁。由于审计提交尚未推送且没有 Pull Request，`.github/workflows/ci.yml` 中 `windows-x64` / `windows-latest` 原生执行证据也仍不存在。

## 审计对象

- 分支：`codex/obsidian-context-v4`
- 实现提交：`3e8337e98a21b7cdc7a2fed0152008f4f79db87d`
- 合并基准：`ea5b1ba950cf03ecfa26353873f10c9aeeb1ffa4` (`origin/main`, tag `0.3.5`)
- 开始审计时 `git status --short`：无输出，工作树干净
- 本地环境：`Darwin arm64`，`go1.26.5 darwin/arm64`，Node `v24.18.0`，npm `11.16.0`

## 完整本地门禁

| 命令 | 结果 | 证据 |
|---|---|---|
| `go test -p 1 -timeout 5m -count=1 ./...` | PASS | `go list ./...` 共 55 个 package；最慢的可见 package 为 `internal/scan` 130.429s，其次为 `internal/reviewjob` 72.177s，均低于每个测试二进制的 5 分钟超时；`test/zerotoken` 53.044s 收尾 |
| `go vet ./...` | PASS | exit 0，无输出 |
| `go mod tidy -diff` | PASS | exit 0，无 diff |
| `git diff --check` | PASS | exit 0，无输出 |
| `cd obsidian-plugin && npm run check` | PASS | lint 通过；17/17 test files、111/111 tests；TypeScript typecheck 和 production bundle 通过 |

本地完整 Go 门禁使用串行 `-p 1`，与 Gate 0 ledger 中已记录的 macOS 文件系统 I/O 争用裁决一致。CI 仍保持原生并行命令。

## 八组 fixture 与稳定拒绝码

架构级 schema fixture 门禁：

```text
go test ./internal/memory -run '^TestV4ContractFixtures$' -count=1 -v
```

结果：PASS；1 个父测试和 8/8 个命名子测试通过，每组 valid fixture 被接受，invalid fixture 被拒绝。

Go 生产解析器稳定码矩阵门禁：

```text
go test ./internal/reviewv4 ./internal/sessionindex ./internal/inspect ./internal/annotation ./internal/pricing -run 'Test(FrozenInvalidReviewAndLedgerFixturesAreRejected|ParseRejectsFrozenInvalidFixture|ParsersRejectFrozenInvalidFixtures|ParseAndRenderPricingFixtureParity|PricingSupplementFixtureParityAndNullMeansUnknown)$' -count=1 -v
```

结果：PASS；8/8 invalid fixtures 通过生产解析入口返回预期的机器可比较错误码。

TypeScript 精确稳定码矩阵：

```text
cd obsidian-plugin
npx vitest run tests/contracts-v4.test.ts -t 'rejects the frozen .* invalid fixture with its Go-compatible code'
```

结果：PASS；8/8 矩阵测试通过，39 个非矩阵测试按过滤器跳过。同一文件的完整命令 `npx vitest run tests/contracts-v4.test.ts` 也通过 47/47。

| 合同 | valid fixture | invalid fixture | Go / TypeScript 预期拒绝码 |
|---|---|---|---|
| review-presentation-v4 | `review-presentation-v4.valid.json` | `review-presentation-v4.invalid.json` | `wire_shape_invalid` |
| machine-ledger-v4 | `machine-ledger-v4.valid.json` | `machine-ledger-v4.invalid.json` | `wire_contract_invalid` |
| session-index-v1 | `session-index-v1.valid.json` | `session-index-v1.invalid.json` | `wire_contract_invalid` |
| session-summary-v1 | `session-summary-v1.valid.json` | `session-summary-v1.invalid.json` | `wire_shape_invalid` |
| session-event-page-v1 | `session-event-page-v1.valid.json` | `session-event-page-v1.invalid.json` | `wire_contract_invalid` |
| agent-annotation-v1 | `agent-annotation-v1.valid.json` | `agent-annotation-v1.invalid.json` | `wire_shape_invalid` |
| pricing-snapshot-v1 | `pricing-snapshot-v1.valid.json` | `pricing-snapshot-v1.invalid.json` | `wire_contract_invalid` |
| pricing-supplement-v1 | `pricing-supplement-v1.valid.json` | `pricing-supplement-v1.invalid.json` | `wire_contract_invalid` |

拒绝码属于封闭的五类合同：`wire_input_overflow`、`wire_invalid_utf8`、`wire_json_invalid`、`wire_shape_invalid`、`wire_contract_invalid`。Go 和 TypeScript 都保留 cause 链，调用方无需比较可变的人类错误文案。

## Fixture 字节一致性

独立遍历 `testdata/contracts/v4/*.json`，对同名 `obsidian-plugin/tests/fixtures/v4/*.json` 执行 `cmp`。结果为 16/16 字节完全一致。此门禁同时覆盖上表 8 个 valid 和 8 个 invalid fixture。

## 禁止占位符检查

按计划原样执行：

```bash
rg -n $'\x54\x42\x44|\x54\x4f\x44\x4f|\x46\x49\x58\x4d\x45|\x69\x6d\x70\x6c\x65\x6d\x65\x6e\x74\x20\x6c\x61\x74\x65\x72|\x66\x69\x6c\x6c\x20\x69\x6e\x20\x64\x65\x74\x61\x69\x6c\x73|\x68\x61\x6e\x64\x6c\x65\x20\x65\x64\x67\x65\x20\x63\x61\x73\x65\x73|\x73\x69\x6d\x69\x6c\x61\x72\x20\x74\x6f' schemas internal/reviewv4 internal/sessionindex internal/inspect internal/annotation internal/pricing obsidian-plugin/src/contracts/review-v4.ts
```

结果：无输出，`rg` exit 1，表示要求的路径内无命中。

## Windows 证据状态

`.github/workflows/ci.yml` 的 `test` job 包含：

```text
name: windows-x64
os: windows-latest
```

该原生 job 会执行 Obsidian 门禁、`go test ./...`、Windows 替换压测、race/vet、PowerShell 可重现发行包与插件打包检查。当前实现提交未推送且无 PR，因此状态为 **PENDING**。

补充交叉编译：使用 `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c` 分别编译 `memory`、`reviewv4`、`sessionindex`、`inspect`、`annotation`、`pricing`、`migrationv4`、`publication`、`syncproject`和 `cli`，10/10 通过。该结果只证明 Windows amd64 可编译，不是 Windows 原生执行证据。

## 明确不属于 Gate 0 的后续工作

- Session index 累积发布与受限查询；
- Obsidian “全部 Sessions”浏览器、虚拟列表与真实 Vault 验收；
- 人工决策/约定、AI 候选提炼、CAS 确认与发布；
- ModelPriceWatch 缓存、匹配、不可变价格快照、补价和用量卡片。

上述四组仍需按各自实施计划完成，不因本地 Gate 0 验收而视为已实现。
