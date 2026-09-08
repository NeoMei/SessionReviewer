# Summary Integrity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Follow the current sequential restoration queue.

**Goal:** Prevent contradictory terminal facts from closing failures and reject summary responses that lose source or coverage evidence.

**Architecture:** Reuse the existing closed typed-outcome classifier for recovery qualification. Tighten the existing summary Go/TypeScript/schema boundaries to the reducer's established invariants without changing the public shape or publication state.

**Tech Stack:** Go inspect, JSON Schema, TypeScript/Vitest; no new dependency.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.3,17.2,18.4 and `2026-09-07-summary-typed-fact-text.md`.

## Global Constraints

- Summary remains read-only; no source log/model/network reads, Vault writes or generation advancement.
- Every block has at most32 items, each text at most512 UTF-8bytes, and exact source revision IDs.
- A raw success/passed label is not verified success when typed exit evidence conflicts. Unknown or unsupported operations never recover a failure.
- Preserve bounded public errors, exact identity, final physical/project/generation recheck, canonical order and existing redaction.
- No migration, unrelated refactor, real-Vault install or release inside this task.

### Task 1: Reconcile recovery semantics and summary contract invariants

**Files:** Modify `internal/inspect/summary.go`, `summary_test.go`, `validate.go`, `validate_test.go`; reuse `summary_text.go` classification without duplicating its policy. Modify `schemas/session-summary-v1.schema.json`, `obsidian-plugin/src/data/contracts-v4.ts`, `obsidian-plugin/tests/contracts-v4.test.ts` and scoped summary/CLI fixtures only when a fixture violates the tightened invariant. Add no new fields.

**Interfaces:** Preserve `LoadSessionSummary`, `ValidateSummary` and `parseSessionSummaryV1`. Recovery invokes `summaryTypedOutcome(fact, summaryTextMapping(fact))`. Every displayed summary item needs1..64 unique source_revision_ids; empty blocks remain valid. For both normal and error blocks, require `coverage.seen=total`, `coverage.indexed=shown`, `coverage.unprojected=omitted`, and collapsed/undecodable/truncated all0. This is block projection coverage, not the independent top-level Session source coverage.

- [ ] RED recovery regression using `summaryTestInput` and `summaryRecoveryRecordIdentity`: an exact matching failure followed by passed with exit_code1 has a structurally valid recovery_link but must remain unresolved. Its verification text stays conflicting. Cover failed with exit_code0, unknown outcome/operation and command_started; a normal supported failed→passed with consistent exit codes still closes only its own exact failure. Keep identity/order/record-digest tamper checks.

```go
if got.UnresolvedQuestions.Total != 1 {
    t.Fatal("contradictory success closed a real failure")
}
// Recovery requires this for the successor, not summarySucceeded(fact.Outcome).
successful := summaryTypedOutcome(success, summaryTextMapping(success)) == summaryOutcomeSuccess
```

- [ ] RED Go and TypeScript contract tests reject an entry with no source revisions in all five blocks, block total/shown/omitted40/32/8 with zero coverage, and shifted coverage buckets that preserve arithmetic sum but contradict projection counts. Valid capped blocks, omitted phase records, zero blocks and independent nonzero top-level source gaps remain accepted. Prove the CLI/runner keeps these failures bounded rather than showing an empty-success summary.
- [ ] Run RED `go test ./internal/inspect -run 'Summary.*(Recovery|Integrity|Coverage|Source)' -count=1` and `npm --prefix obsidian-plugin test -- contracts-v4.test.ts`; record the actual assertions failing before implementation.
- [ ] Implement the shared typed recovery predicate and matching block validation. Failure predecessor must be unambiguously failed; conflicting predecessor/successor cannot create a validated recovery link. Do not remove conflicting evidence from errors/unresolved merely to hide the contradiction. Increment the deterministic summary rule version to `summary-typed-fact-text-v3` because recovery projection changed; update only affected expectations.
- [ ] Require source array minItems1/uniqueItems in the JSON schema; use Go/TS cross-field checks for coverage relationships that plain schema cannot express. Factor only the shared small block count predicate rather than copying validation bodies. All current valid canonical fixture bytes remain unchanged unless the summary rule fixture is an actual producer output.
- [ ] GREEN focused tests, `go test ./internal/inspect ./internal/cli -count=1`, full plugin check, scoped vet and diff check; one full Go regression on the frozen implementation. Commit exact files and obtain independent spec/quality review. This correction does not certify unimplemented milestone/source/provider workflows.

## Preflight

| Shared surface | Reconciliation |
|---|---|
| Summary text / recovery | Same closed typed classifier decides whether success is unambiguous |
| Reducer / Go / TS | Block projection coverage matches total/shown/omitted; top-level source coverage stays separate |
| Schema / parser | Require nonempty unique sources structurally, check arithmetic relationships at runtime |
| Current tests / new semantics | Preserve valid recovery, exact sources and existing partial source coverage; no weakening to satisfy fixtures |

Ruling: Apply the existing typed-outcome interpretation to recovery, not only display — independent audit found one fact can say result conflict while closing a failure — cost if wrong is conservative unresolved evidence, never falsely verified recovery.

Ruling: Tighten summary parser invariants to the existing reducer output without changing its shape — provenance and accurate omission are required by the accepted spec — cost if wrong is rejecting malformed legacy responses; no accepted stored data is rewritten.
