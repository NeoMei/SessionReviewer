# Generated Milestone Readability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after the active answer task is independently reviewed; never overlap implementation workers.

**Goal:** Make newly generated milestone titles and machine evidence paragraphs readable in the approved evolution view without changing source facts or human edits.

**Architecture:** Localize bounded typed fact descriptions at the existing pure projector, using authenticated revisions already supplied to it. Preserve generator identity, accepted source refs and coverage; the existing generated-baseline rebase publishes changed machine text while retaining human overrides. Never parse concatenated excerpts back into facts.

**Tech Stack:** Existing Go presentation/reviewv4/contextupdate packages; current strict TypeScript and browser fixtures. No dependencies, network, Agent or new public contract.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§2,4,4.1,9 and `2026-09-05-v4-human-markdown-codec-design.md` §§6,7. Approved five-tab layout is unchanged. This task implements already-required readable projection, not additional evidence producers.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes. No model summarizes a reply unless the user explicitly requests an AI candidate.
- Existing human edits win over generated fields. Never rewrite source identity, timestamps, hashes, decision state or workflow state.
- Fixed detail order: 触发问题、Agent 结论、执行与变更、结果与验证、对项目的影响与后续.
- `execution_verified` is a machine evidence state. It never implies the human workflow state `resolved`.
- Keep exact source references and machine evidence available through authenticated drilldown. Public primary paragraphs must not expose raw field assignments or raw tool stdout.
- No migration, provider activation, dependencies, native daily-Vault install, network, merge, push or release in these task commits.

### Task 1: Render readable typed machine facts and verify human-preserving scan updates

**Files:** Create `internal/presentation/milestone_readability.go` and `milestone_readability_test.go`. Modify `internal/presentation/milestone.go`, `milestone_evidence.go`, and existing `milestone_evidence_test.go` assertions that deliberately assert old generated English text. Add rebase regression in `internal/reviewv4/markdown_milestone_update_test.go` and actual scan assertions in `internal/contextupdate/milestones_test.go`. Do not change source adapters, private retained-chain policy/schema, UI source, publication/no-op rules or baseline-hash implementations.

**Interfaces:** Add private `milestoneTitle(category string) string` and `readableMilestoneEvidence(kind, state string, revision memory.ObservationRevision) string`. Pass the already-built `revisionsByID` into execution rendering, just as verification already receives it. Lookup the exact action/result revision by ID; do not split `action.Excerpt` on `;` or `=`. If a referenced revision is absent, return a neutral unavailable-evidence description, never fabricate fields or accept a mismatched revision. Existing `ProjectMilestones` input validation remains mandatory.

- [ ] Write owned RED tests using `milestoneProjectInput`, `milestoneFixtureSpec` and `milestoneFactSpec`. Assert existing qualification/ref/answer positive controls before readable-text assertions. Literal production verification `{component:"package",status:"test",exit_code:"0",passed:"true",failed:"false"}` must produce title `已记录验证通过`, preserve exact answer, and not present `Machine-observed`, `command_started`, `command_signature=` or `exit_code=` in title/summary/execution/verification text.

```go
input := milestoneProjectInput(t, milestoneFixtureSpec{
    messages: milestoneMessages("执行测试", "原始回答保留"),
    facts: []milestoneFactSpec{{kind: "verification", operation: "verification", outcome: "passed", line: 2, timestamp: milestoneTime2,
        fields: map[string]string{"component": "package", "status": "test", "exit_code": "0", "passed": "true", "failed": "false"}}},
})
got, err := ProjectMilestones(input)
if err != nil || len(got.Timeline) != 1 { t.Fatalf("qualification: %v", err) }
if got.Timeline[0].ClosedLoop.Conclusion.Text != "原始回答保留" { t.Fatal("answer changed") }
if got.Timeline[0].Title != "已记录验证通过" { t.Fatalf("title=%q", got.Timeline[0].Title) }
```

- [ ] Table-test all existing qualified categories: verification `已记录验证通过`, commit `已记录提交`, release `已记录发布`, deployment `已记录部署`, version `已记录版本变化`. These titles only describe accepted typed facts, not project completion. Do not add rollback qualification here. Test ordinary question-only input still produces no milestone and machine contradictions still cannot qualify as passed.
- [ ] Render event descriptions with a closed local kind/state map: command_started `命令执行已开始`, command_finished `命令执行已结束`, file_change `文件变更记录`, verification `验证记录`, commit/commit_created `提交记录`, release/release_created/release_published `发布记录`, deployment `部署记录`, version `版本记录`, branch/git_observation `Git 状态记录`; unknown kind `执行证据`. States passed/failed/conflict/unknown become `通过`/`失败`/`证据冲突`/`结果未确认`. Use the existing strict verification state classifier; no generic command exit0 may be described as verified project completion.
- [ ] Append only whitelisted typed facts with readable labels: `command_signature → 命令类型`, `component → 组件`, `exit_code → 退出码`, `path → 项目内文件`, `git_head → 提交标识`, `tag → 标签`, `version → 版本`, `target → 目标`, `release_id → 发布标识`, `branch → 分支`. Keep field allowlists per event category; omit internal tool IDs, hashes other than useful commit identity, and raw `status/passed/failed` key-value lists from primary paragraphs. Boolean/count passed markers already drive qualification; never display boolean true as one passed test. Text values remain data and are bounded/redacted by the existing helper. No raw stdout/excerpt fallback.
- [ ] Add adversarial field values containing `;`, `=`, Chinese/multiline text, absolute paths, secret canaries and long UTF-8 strings. Output remains valid UTF-8 within the established segment bound, no hidden/tool canary leaks, no naive excerpt parsing can synthesize extra fields. Unknown values stay neutral rather than guessed translations. Repeated projection and reversed input ordering keep identical accepted IDs/source refs/counters/answer and deterministic generated text.
- [ ] Run focused RED before implementing: `go test ./internal/presentation -run 'MilestoneReadability' -count=1`; capture intended readable-text assertion failures, not setup/fixture errors. Implement only the closed mappings and exact revision-driven rendering. Remove obsolete raw rendering helpers if now unused; do not maintain two projector paths.
- [ ] Rebase test: use existing `milestoneUpdateLedger`/`generatedMilestone`/`milestoneUpdate` fixtures. Start from accepted generated English title and summary; supply readable generation at same stable ID. Untouched generated title updates, manually edited title/summary/conclusion remain byte-identical with human provenance and original source refs. Custom Markdown and CRLF survive. Reapplying identical new text stays byte/revision stable. Missing source does not erase accepted human history.
- [ ] Extend actual `TestRunPublishesQualifiedMilestoneAndKeepsIdenticalScanByteStable` assertions to verify readable ordinary scan output, preserved human edits and repeated no-op bytes. For a text-only generator change, existing `unchangedV4ScanPublication` normalizes generation then compares the rebased presentation; keep that guard intact, do not bypass it on unchanged ProjectViewDigest.
- [ ] Run focused GREEN, full `go test ./internal/presentation ./internal/reviewv4 ./internal/contextupdate -count=1`, scoped `go vet` for those packages, `git diff --check`, and `go test ./test/zerotoken -run '^TestMarkdownV4OrdinaryScanPublishesMilestoneWithoutAgentStart$' -count=1`. Controller reruns actual CLI scan→human edit→rescan→source removal fixture and strict TS→Chrome1200/390. Inspect screenshot for primary readable titles/evidence, human text preserved, five sections and five tabs unchanged. Existing command-qualified producer gaps/native/provider/actions remain open.
- [ ] Commit exact source/tests/report; pass independent spec+quality review before continuing. If unchanged wire contracts fail, investigate the specific seam rather than weakening validators or changing legacy canonical bytes.

## Preflight

| Shared surface | Producer / consumer | Checked relationship |
|---|---|---|
| Task1 projector / rendering | typed revisions / accepted machine text | exact map lookup, no reparsing private excerpt strings |
| Task1 execution / verification | existing by-ID map / per-turn evidence | reuse single map; keep strict qualifier state for verification |
| Task1 generated text / rebase | stable IDs / human override baseline | existing baseline engine owns overwrite decisions; no direct Markdown replacement |
| Task1 no-op / normal scan | changed projected text / normalized semantic comparison | current implementation compares rebased presentation even at same ProjectViewDigest |
| Task1 / remaining milestone producers | existing qualified typed facts / readable labels | category fixture handling does not prove production commit/release/deployment producers |

Ruling: Improve generated text at the typed projector rather than reinterpret accepted paragraphs inside the UI — keeps native Markdown and the browser consistent and preserves one human-overwrite authority — cost if wrong is a bounded generated-text revision; no human text or machine identity may change.

Self-review: every category maps to the existing supported projector set; files and helpers above exist or are explicitly introduced. Unknown operation fallback is neutral, existing byte/redaction policy stays in force. UI actions, provider support, complete producer coverage and native release acceptance are outside this task and remain in the umbrella checklist.

Controller preflight2026-09-09: external `TestControllerMilestoneReadableMachineText` runs the actual authenticated pure projector, passes one-qualified-milestone, exact answer and exact source-view positive controls, then fails at `Machine-observed verification` instead of `已记录验证通过` (exit1,0.634s). Primary raw-field assertions after that failure remain unexecuted. This supplements the actual scan-exported Chrome screenshots; no readability implementation has started.
