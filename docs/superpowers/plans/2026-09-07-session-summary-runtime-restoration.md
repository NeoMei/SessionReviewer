# Session Summary Runtime Restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. This restores a bounded missing runtime from the approved Session index plan; it does not close product acceptance.

**Goal:** Make `inspect session-summary` return an authenticated deterministic summary instead of “not implemented”.

**Architecture:** Reuse the published-generation authentication boundary already used by event and conversation inspection. Project only authenticated SessionView facts and dependencies into the existing `SessionSummary` contract. Do not open raw sources, run Agent processes or write projections.

**Tech Stack:** Existing Go, strict JSON, private store, CLI test harness; no dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.3,17.2,18.1; restores Task 4 and the summary part of Task 6 of `2026-09-04-session-index-publication-query.md`.

## Global Constraints

- 每个区块最多 32 项，每项正文最多 512 UTF-8 字节，按 `occurred_at asc, sequence asc, revision_id asc` 稳定排序。
- 超出部分保存总数和未展示数。摘要只能使用确定性规则和受限脱敏 excerpt；不得生成原因、意图或未被事实支持的“下一步”。
- `session-summary-v1` 不写入 Vault，由 CLI 从当前 SessionView 和其依赖生成。
- Existing authentication, path confinement, revision consistency and final generation recheck are mandatory. No public-hash-only acceptance.
- Source disappearance does not remove retained summaries. No Agent, network, raw source-log read, writable store or generation change.
- No cross-platform expansion, migrations, real-vault changes, release, dependency additions or unrelated refactoring.

### Task 1: Implement and dispatch authenticated deterministic Session summaries

**Files:**
- Create `internal/inspect/summary.go`, `summary_test.go`.
- Modify `internal/inspect/service.go` only to share the existing full authentication envelope and expose the already-validated index entry / revision list / relevant ProjectView facts to the summary reducer.
- Modify `internal/cli/inspect.go`, `inspect_test.go` for runtime dispatch and help.
- Reuse `internal/inspect/types.go`, `validate.go`, strict renderer and existing event fixture helpers; do not create a second summary schema.

**Interfaces:**

```go
type SummaryRequest struct {
    DataRoot, ProjectID, Provider, SessionID, ExpectedGenerationID string
}
func LoadSessionSummary(context.Context, SummaryRequest) (SessionSummary, error)
```

Consume existing immutable `memory.SessionView`, its authenticated revisions and the selected `sessionindex.Entry` coverage. Relevant `phase_boundary` ProjectView records may be included only when all their source revision IDs belong to the selected Session; never silently drop cross-Session dependencies to make them fit. Do not invent a phase from a random user question.

- [ ] **Write RED integration tests first.** Use `buildEventFixture` / `buildEventFixtureCustomizedAt` to test actual configured/published storage: correct identity/digest, wrong generation/provider/Session/project, divergent summary vs immutable revision, tampered dependency, final generation advance, cancellation, source marked unavailable. Snapshot files before/after and prove no writes. Add real CLI invocation coverage that currently returns `inspect subcommand is not implemented`.

```go
func TestLoadSessionSummaryUsesPublishedIdentity(t *testing.T) {
    fixture := buildEventFixture(t, "project-summary", "generation-summary", "session-1")
    got, err := LoadSessionSummary(context.Background(), SummaryRequest{
        DataRoot: fixture.dataRoot, ProjectID: fixture.projectID,
        Provider: "codex", SessionID: "session-1", ExpectedGenerationID: fixture.generationID,
    })
    if err != nil { t.Fatal(err) }
    if got.ProjectID != "project-summary" || got.SessionID != "session-1" || got.Coverage != (Coverage{Seen:5, Indexed:3, Undecodable:2}) {
        t.Fatalf("summary=%+v", got)
    }
    if err := ValidateSummary(got); err != nil { t.Fatal(err) }
}
```

- [ ] **Write RED deterministic reducer tests.** Forty verification facts produce `total=40, shown=32, omitted=8`; reversed inputs still sort canonically; multibyte text remains valid UTF-8 and <=512 bytes; secrets/absolute paths are redacted. Explicit failed verification appears as a failure, never a passing result. Recovery links close only their exact dependency-matched unresolved failures; a bare user request has unknown resolution and is not asserted unresolved. Show request facts only if an explicit supported unresolved signal exists. Every returned item has nonempty source revision IDs from authenticated inputs.
- [ ] **Run RED:** `go test ./internal/inspect ./internal/cli -run 'SessionSummary|InspectSummary' -count=1`. Record missing-symbol or not-implemented failure before writing production code.
- [ ] **Implement a minimal shared read envelope and reducer.** Preserve the existing `inspectPublishedSession` event/conversation behavior. Reuse `safeEventExcerpt`, `entryLess`, `RenderSummary`, and exact existing JSON shape. Map typed command/file/artifact/commit/release facts to key operations, typed verification/test/build/lint to verification results, typed errors/failed outcomes to errors. Use stable typed error codes, not arbitrary source prose. Derive missing/unresolved facts only from supported failed outcomes not referenced by a validated recovery link. Empty sections remain empty, not filler text. Block coverage represents projection shown/unprojected counts; overall coverage comes from the accepted index, not len(items).

```go
// Each emitted entry retains the immutable source identity.
entry := Entry{OccurredAt: fact.OccurredAt, Sequence: uint64(fact.Sequence),
    RevisionID: fact.RevisionID, Text: safeEventExcerpt(fact.Excerpt),
    SourceRevisionIDs: []string{fact.RevisionID}}
// The public loader must return only after the common final generation recheck.
```

- [ ] **Wire runtime dispatch and bounded output.** Existing `ParseInspectContract` already accepts summary identity. `runInspect` selects `LoadSessionSummary` and `RenderSummary`; preserves 10-second execution timeout, response byte cap and typed errors. Help lists the real new command. No new parser grammar.
- [ ] **Run focused GREEN** with the RED command, then `go test ./internal/inspect ./internal/cli -count=1` and `go vet ./internal/inspect ./internal/cli`. Existing event/conversation tests must pass unchanged. Run `git diff --check` and format only changed Go files.
- [ ] **Commit exact task files** with `feat: restore authenticated session summary queries`. Report RED/GREEN, no-write proof, implemented behavior and unresolved product requirements to this plan's task report. Do not mark S07 complete until plugin summary/search/filter flows also pass.
