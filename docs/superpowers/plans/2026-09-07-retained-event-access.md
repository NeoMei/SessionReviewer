# Retained Session Event Access Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development.

**Goal:** Keep authenticated indexed facts readable after raw source logs become unavailable, without exposing source-backed conversation content.

**Architecture:** Remove the UI-only raw-source prerequisite from event loading. Keep complete identity/digest/cursor validation and the existing read-only CLI authentication boundary; source-backed Q/A remains unavailable. Add a real private-store regression that proves retained events need no source logs or writes.

**Tech Stack:** Existing TypeScript/Vitest and Go inspect tests; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§7.2–7.3,13.2,18.4. Sequential follow-up after summary UI and typed-fact text reviews.

## Global Constraints

- Retained private facts and raw source availability are different capabilities; do not invent missing source-backed content.
- Preserve exact project/provider/Session/generation/SessionView and cursor bindings. The full validated index remains visible without CLI.
- No source log reads, network, models, arbitrary paths, storage writes, schema/dependency/version changes, migration, production Vault installation, merge or release.
- Do not weaken private authentication, response parser validation, late-response rejection or human data preservation.

### Task 1: Restore retained event reading independently from source-backed Q/A

**Files:** Modify `obsidian-plugin/src/view/render-scan-records.ts`, `obsidian-plugin/tests/render-scan-records.test.ts`, `internal/inspect/service_test.go`. No production Go change is expected; report a concrete backend gap if the regression disproves the existing boundary.

**Interfaces:** Existing `ScanRecordsOptions.loadSessionEvents`, `loadConversation`, `loadSessionSummary` and `LoadSessionEventPage(ctx, EventPageRequest)` remain unchanged.

- [ ] **Write UI RED.** Render an unavailable-source Session with a valid view digest and retained indexed events. Assert event loader runs with all current identity fields, returned excerpts render, summary runs independently, and conversation loader does not run. Then paginate events, retry a rejected page and switch to an available Session; stale responses cannot overwrite the selected Session. Missing digest, zero retained events and missing CLI still prevent event calls and give explicit recovery/empty states.

```ts
expect(loadSessionEvents).toHaveBeenCalledWith(expect.objectContaining({
  provider: retained.provider, sessionId: retained.session_id,
  expectedGenerationId: index.generation_id,
  expectedSessionViewDigest: retained.session_view_digest
}));
expect(loadConversation).not.toHaveBeenCalled();
expect(root.textContent).toContain('retained indexed fact');
```

- [ ] **Run UI RED.** `npm --prefix obsidian-plugin test -- render-scan-records.test.ts` must fail because the retained event loader is not called. Update only tests whose old expectation incorrectly couples private facts to raw-source availability; preserve Q/A non-call assertions.
- [ ] **Add backend regression.** Use `buildEventFixtureCustomizedAt` with `view.TerminalState = memory.Missing` and `view.SourceAvailability = memory.SourceUnavailable`. Read with the authenticated request and assert retained IDs/excerpts, unchanged coverage and valid cursor paging. Snapshot the fixture tree before/after and assert no writes. The fixture must have no raw source logs. Also assert wrong generation still returns `CodeGenerationMismatch`.
- [ ] **Implement the minimal eligibility correction.** Keep these independent predicates, and preserve existing epoch/cache logic:

```ts
const eligible = (session: SessionIndexEntryV1 | undefined): session is SessionIndexEntryV1 =>
  Boolean(session && session.session_view_digest && session.indexed_event_count > 0);
// Conversation eligibility continues to require source_availability === 'available'.
```

  Make unavailable-source copy distinguish raw Q/A from retained events: `原始来源不可用；下方仅显示已保留的索引事实。` Do not imply events have loaded when authentication failed or CLI is missing. Existing loading/error/retry/zero-event states remain authoritative.
- [ ] **Verify GREEN.** Run focused plugin tests, `go test ./internal/inspect -run 'Retained|Unavailable' -count=1`, `npm --prefix obsidian-plugin run check`, `go vet ./internal/inspect`, and `git diff --check`. No real Vault or production data mutation. Report which unavailable-source and no-write paths ran; native product acceptance remains open.
- [ ] **Commit the three task files** with `fix(obsidian): retain event access without raw source logs`; report RED/GREEN, bindings, provenance boundaries, and unchanged source-backed Q/A behavior.
