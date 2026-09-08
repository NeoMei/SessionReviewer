# Event Topology Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after summary integrity; preserve independently reviewed frontend navigation.

**Goal:** Reject malformed nonempty event-page cursor topology consistently in Go, TypeScript and structural schema validation.

**Architecture:** Tighten the existing Go event-page validator to match the already required page semantics. Keep page shape, cursor authentication, pagination producer and old valid canonical bytes unchanged; add the shared structural nonempty-string restriction to the schema.

**Tech Stack:** Existing Go inspect, JSON Schema and frontend contract fixtures; no dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.3,16,18.4. This is a bounded correction from comprehensive code/contract review, not a new pagination design.

## Global Constraints

- Each page contains at most100 events; ranges remain zero-based half-open. An empty page has total0/range0–0 and all four cursors null.
- Nonempty pages have at least one item, nonempty first/last cursors, previous cursor iff range_start>0 and next cursor iff range_end<total.
- Cursor maximum4096 UTF-8bytes; response maximum1MiB; source identity and indexed coverage remain exact.
- No source/model/network reads, state mutation, schema migration, provider implementation, real-Vault install or release in this task.
- Existing valid legacy canonical fixtures remain unchanged. Do not weaken tests to admit malformed pages.

### Task 1: Align nonempty cursor validation without changing the producer

**Files:** Modify `internal/inspect/validate.go`, `validate_test.go`, `schemas/session-event-page-v1.schema.json`; add structural schema cases in existing `internal/memory/api_compat_test.go` using `readContractJSON` and `validateContractSchema`. Modify frontend `tests/contracts-v4.test.ts` only for parity assertions; do not change the reviewed renderer/runner.

**Interfaces:** Preserve `ValidateEventPage`, `ParseEventPage`, `RenderEventPage` and `parseSessionEventPageV1`. Existing nonempty cursor strings are opaque; never decode them for topology checks.

- [ ] Add RED table tests using an otherwise valid two-event response with one displayed item, matching coverage2/2, nonempty first/last/next cursors, previous nil. Verify that valid control first passes before mutating exactly one property per invalid case: missing next, missing first, missing last, empty first/last/previous/next, unexpected previous on range_start0, unexpected next at range_end=total, and positive total with no items. Cover valid first/middle/last/single-page/empty controls, all through Parse and Render boundaries as well as Validate.

```go
page := minimumEventPage()
page.Total, page.RangeEnd = 2, 1
page.Coverage = Coverage{Seen: 2, Indexed: 2}
page.Items = []EventItem{{Kind:"command", RevisionID:"revision-1", Sequence:1, OccurredAt:"2026-09-08T00:00:00Z", Excerpt:"synthetic"}}
cursor := "opaque-boundary"
page.FirstCursor, page.LastCursor, page.NextCursor = &cursor, &cursor, &cursor
if err := ValidateEventPage(page); err != nil { t.Fatal(err) }
page.NextCursor = nil
if _, err := RenderEventPage(page); err == nil { t.Fatal("accepted hidden next page") }
```

- [ ] Run RED `go test ./internal/inspect -run 'Event.*Topology' -count=1`. Controller independent overlay at `/tmp/session-reviewer-filter-qa.OYTCsf/event-topology-overlay.json` already fails three exact cases at ea093bc (hidden next, missing boundaries, empty first); retain it as an external acceptance check.
- [ ] Add a small validator predicate after existing range/empty checks; preserve current arithmetic/identity/item validation and public error wrappers. Check every non-null cursor for nonempty content, require first/last for total>0, and check previous/next nullness against range boundaries.

```go
if page.Total > 0 && (len(page.Items) == 0 || page.FirstCursor == nil || page.LastCursor == nil ||
    (page.PreviousCursor == nil) != (page.RangeStart == 0) ||
    (page.NextCursor == nil) != (page.RangeEnd == page.Total)) {
    return errors.New("event page cursor topology does not reconcile")
}
```

- [ ] Add `minLength:1` to the schema's string/null cursor definition. Null is still structurally valid; Go/TS enforce cross-field topology. Run valid and empty-string-invalid fixtures through the actual existing schema harness; do not use text-search assertions on schema source.
- [ ] Run GREEN focused topology tests, `go test ./internal/inspect ./internal/cli -count=1`, current frontend contract fixture suite, scoped vet and diff check. Re-run the controller overlay; all three cases must pass. Existing frozen valid page bytes must remain unchanged. Commit exact files and obtain independent spec/quality review. Full release-wide regression remains mandatory after all tasks.

## Preflight

| Surface | Check |
|---|---|
| Go producer / validator | Producer already emits cursors; stricter validator protects future malformed output without changing paging |
| Go / TypeScript | Same nonempty topology, safe bounds and opaque cursor semantics |
| Schema / runtime | Schema handles empty strings; runtime handles cross-field nullness |
| Summary task / this task | Both touch validate.go; execute sequentially after summary integrity to avoid overlap |

Ruling: Preserve Go/TypeScript page-contract parity as a separate small correction — exact external RED shows backend RenderEventPage accepts payloads the corrected plugin rejects — cost if wrong is fail-closed malformed responses, not loss or rewriting of valid data.
