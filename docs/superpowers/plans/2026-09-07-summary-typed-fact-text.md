# Readable Typed Session Summary Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development.

**Goal:** Prevent authenticated but empty bookkeeping rows from masquerading as useful key operations, and preserve typed outcomes when excerpts are absent or misleading.

**Architecture:** Keep the existing summary authentication and wire schema. Tighten deterministic classification and compose bounded neutral text from a closed set of typed facts before optional sanitized excerpts; no source-log parsing and no semantic inference.

**Tech Stack:** Existing Go inspect reducer/tests only.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.3,17.2; actual-data follow-up to summary runtime `ae742f4`.

## Global Constraints

- Session 摘要不写入 Vault，由 CLI 从当前 SessionView 和其依赖生成；不生成原因、意图或未被事实支持的下一步。
- Every block at most32 entries, every text at most512 UTF-8 bytes; retain exact sources, order, identity, coverage and final publication recheck.
- No model/network/raw-source reads/writes, no auth bypass, no project/response schema change, no release or Vault installation.
- A claimed excerpt never overrides typed outcome. Unknown outcome does not become success. Observed artifact metadata is not an implementation milestone.

### Task 1: Filter bookkeeping and render readable typed outcomes

**Files:** Modify `internal/inspect/summary.go`, `internal/inspect/summary_test.go`; add `internal/inspect/summary_text.go`, `summary_text_test.go`. Existing CLI/integrity tests unchanged.

**Evidence:** Real retained Session query on2026-09-07 returned5 key operations with empty text. Inspecting its authenticated SessionView showed all5 were `kind=artifact, operation=session_started`; these are not useful key operations. Existing test fixtures use human-written excerpts and missed this production pattern.

**Interfaces:**

```go
func summaryFactText(fact memory.ObservationRevision) string
func isSummaryOperation(fact memory.ObservationRevision) bool
```

- [ ] **RED classification test.** Using existing real-store fixture customization, include `artifact/session_started`, `artifact/cwd_changed`, a real command and artifact creation. Summary key operations excludes the two bookkeeping operations (overall accepted coverage unchanged), retains real operations and their source IDs. A fixture containing only metadata returns empty key operations, not blank entries.
- [ ] **RED text tests.** An empty-excerpt `command_started` produces `命令已启动 · 结果未记录`; `command_finished` outcome success with exit_code0 produces `命令已完成 · 成功 · 退出码 0`; failure with exit_code1 remains failed even if excerpt says `all tests passed`. Verification failed/passed/unknown has an explicit corresponding neutral prefix. Unsupported arbitrary outcome/operation never gets success, and arbitrary Fields strings are not echoed as labels. A real typed file/artifact/commit/release operation without excerpt still gets a nonempty neutral kind label. Hidden paths/secrets/high entropy field values never enter output; any appended excerpt passes existing redaction and512-byte cap. Preserve valid UTF-8 and result prefix under truncation.

```go
got := summaryFactText(memory.ObservationRevision{
  Key: memory.ObservationKey{Kind:"command"}, Operation:"command_finished",
  Outcome:"failure", Fields:map[string]string{"exit_code":"1"}, Excerpt:"all tests passed",
})
if !strings.HasPrefix(got,"命令已完成 · 失败 · 退出码 1") { t.Fatalf("text=%q",got) }
```

- [ ] **Run RED:** `go test ./internal/inspect -run 'Summary.*(Text|Bookkeeping|Typed)' -count=1`. Record missing helper or incorrect blank/bookkeeping behavior, not unrelated compilation errors.
- [ ] **Implement closed typed text mapping.** Recognize command start/finish, file change, verification/test/build/lint, artifact/commit/release/error. Explicitly exclude `session_started` and `cwd_changed` bookkeeping from key operations regardless of broad artifact kind; do not remove accepted observations or falsify index coverage. Use exact known outcomes for success/failure; normalize only safe closed tokens. Include only strictly parsed integer exit code in labels; reject conflicting success/outcome and exit code rather than promoting. Unknown/contradictory values say result unknown/conflicting, never verified. Prefix typed result before optional redacted excerpt, skip duplicate empty suffix. Do not echo arbitrary object/path/component/command text. Existing known command signature may be used only if closed-value mapped, not raw. Reuse `safeEventExcerpt` after concatenation.
- [ ] **Apply text to all summary entry consumers.** `summaryEntry` uses summaryFactText; error and unresolved entries inherit same typed result. Update summary Rules.RuleVersion to a named next deterministic version because projected content/classification changed; do not change source adapter or project generation. Keep phase record handling and source references unchanged.
- [ ] **GREEN:** focused tests, `go test ./internal/inspect ./internal/cli -count=1`, `go vet ./internal/inspect ./internal/cli`, `git diff --check`. Re-run readonly CLI on actual retained Session using its still-current published generation and report counters only: bookkeeping-only key operations must no longer produce5 blank items; no false success/verification. Snapshot no-write test remains passing.
- [ ] **Commit exact four task files** with `fix: render meaningful typed session summary facts`; report RED/GREEN, exact text/privacy behavior and remaining current-host decoder gap. This does not capture previously unindexed exec-wrapped commands and does not close S02/S03/S07.
