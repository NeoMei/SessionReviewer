# Visible Conversation Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Remove false Codex decode failures and make the selected Session's visible user/assistant exchange accessible in Obsidian without model calls.

**Architecture:** Preserve the existing immutable facts index. Add an authenticated, bounded conversation query over the selected accepted source prefix, using the frozen conversation-chain model for deterministic turn grouping. Display it separately from execution facts so an assistant claim never becomes machine verification. Do not implement unrelated problem-map or migration work.

**Tech Stack:** Existing Go source adapters, private store, CLI contracts, TypeScript and Vitest/Obsidian.

**Spec:** This bounded repair uses the visible-message/privacy and on-demand query requirements of the already approved design, sections 17.3 and inspect conversation-chain in `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`. This plan supersedes the old chain implementation plan only for recovery: query-derived chains need not widen the immutable facts schema or persist full conversations. Existing approved navigation stays unchanged.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes. No model summarizes a reply unless the user explicitly requests an AI candidate.
- Only visible `user` and `assistant` content participates. System/developer instructions, hidden reasoning, encrypted compaction and opaque content are excluded.
- A turn unit starts at one visible user message and ends immediately before the next visible user message in the same provider Session.
- Visible excerpts are at most 4,096 UTF-8 bytes; truncation must be explicit. Expanded per-source reads are at most 64 KiB, with the existing CLI total response cap and timeout.
- Authenticate project, published generation, Session dependencies and source prefix. Cursors bind project/provider/session/generation/view/turn/limit/redaction version and cannot cross those boundaries.
- Do not copy conversation bodies or raw tool outputs to Vault. Do not evaluate logged JavaScript or execute logged commands. Assistant words are not execution verification.
- Preserve user edits and existing v4/legacy views. Work only in this isolated branch; no merge, push or release. Native installation and selected Session rescan follow tests, with rollback backups.

### Task 1: Correct Codex accounting-envelope classification

**Files:** `internal/source/codex/decode.go`, its tests, `internal/accounting/accounting_test.go` if needed.
**Interfaces:** Existing DecodeReport and accounting Accumulator remain unchanged.

- [ ] Write a regression using the current valid `token_usage_record` envelope between a user message and `event_msg/token_count`; assert zero unsupported records, preserved user observation, unchanged token totals, and unchanged rejection of a genuinely unknown top-level type.
- [ ] Run `go test ./internal/source/codex ./internal/accounting -count=1` and record the expected RED failure.
- [ ] Recognize the duplicate usage envelope as known metadata without adding its usage again. The compatibility change is an explicit switch case, not a catch-all default. Preserve malformed JSON reporting.

```go
// token_count remains the authoritative accounting input; this envelope is
// supplementary host metadata and is not a fact observation.
case "world_state", "compacted", "inter_agent_communication_metadata", "token_usage_record":
    return nil
```

- [ ] Run the focused tests GREEN, self-review and commit only task files. Report exact RED/GREEN evidence.

### Task 2: Implement authenticated private visible-conversation query

**Files:** Create focused files under `internal/inspect` for conversation query, source loading and paging; `internal/conversationchain/materialize.go` and tests; modify `internal/cli/inspect.go` and tests. Source-adapter helper extensions are allowed where required for exact accepted-prefix reads. Add a separate page schema and fixtures if paging differs from the frozen whole-document schema; do not change session-event-page-v1 semantics.
**Interfaces:** Export `ConversationRequest` with DataRoot, ProjectID, Provider, SessionID, ExpectedGenerationID, TurnUnitID, Cursor, MessageCursor and Limit; export `LoadConversationPage(context.Context, ConversationRequest)` and a bounded JSON renderer. Default query pages turn indexes via new optional `--cursor`; selected turn query pages visible messages via existing `--message-cursor`. Reject index cursor on a selected turn and message cursor without a turn. Publish exact wire types for Task 3 in the report.

- [ ] Add RED integration tests using a real private-store fixture and source files: user/assistant/tool/user ordering; commentary and final_answer visibility; analysis/system/developer/encrypted exclusion; unanswered tail; UTF-8 truncation; first/middle/last paging; no raw tools; stale cross-turn/Session/limit/generation cursor; wrong source hash; missing/shrunk/replaced source; accepted prefix still readable after append; context cancellation; response caps. Reuse existing inspect generation authentication tests rather than weakening them.
- [ ] Implement deterministic turn grouping from visible source messages and bounded machine facts only. Stable IDs derive from source identity and record identity; source hash/revision changes invalidate dependencies. Return source-unavailable diagnostics instead of fabricated empty success. Distinguish partial-only commentary from completed final answer when source supplies phase.

```go
type ConversationRequest struct {
    DataRoot, ProjectID, Provider, SessionID, ExpectedGenerationID string
    TurnUnitID, Cursor, MessageCursor string
    Limit int
}
// All source access is scoped to authenticated references for the selected
// published Session. Never rediscover arbitrary unrelated Session logs.
```

- [ ] Reuse existing adapters' path/symlink/inode/hash defenses. The queried source boundary must be the published prefix, never new appended messages. If generic provider support cannot safely reuse an existing adapter, return a typed unsupported-provider error rather than silently claiming no answers; Codex segmented Session recovery is mandatory for this task.
- [ ] Wire existing `inspect conversation-chain` grammar. Return bounded, content-free errors. No source path/raw payload in public errors. Reading must not mutate private store or Vault. Queries use deterministic extraction/redaction only.
- [ ] Run focused RED/GREEN and then `go test ./...`, `go vet ./...`, `go mod tidy -diff` once. Commit task files and report the complete frontend wire contract and exact test evidence.

### Task 3: Connect visible Q/A to Obsidian and verify the real flow

**Files:** `obsidian-plugin/src/cli/runner.ts`, new conversation page types/parser, focused new `src/view/render-conversation.ts`, `src/view/render-scan-records.ts`, `src/view/project-view.ts`, relevant tests and styles; durable verification document.
**Interfaces:** Consume Task 2's reported wire contract through fixed CLI argv, never a shell string. Add `getConversation` to the runner and inject the loader in the scan view.

- [ ] Write RED tests for strict wire validation, fixed argv, user/Agent labels, loading/error/retry, stale response suppression after Session/project changes, first/middle/last page controls, explicit truncation and unanswered state. Existing facts and legacy view must still work when conversation query is unavailable.
- [ ] Add a clearly labeled conversation section to the selected Session detail, separate from execution facts. Default turn rows show the user question and answer availability; selecting a turn shows user text and Agent responses in chronological order, with final answer distinguishable from progress text where the wire supports it. Render text safely, never injected HTML. Page long replies/turns and keep controls reachable. Do not add a duplicate top-level navigation category.
- [ ] Run plugin tests/build once, review diff and commit. Report covering test output.
- [ ] Controller runs full final gates and independent whole-branch review. After clean review, back up installed CLI/plugin and selected public/private project state; install the candidate, rescan only logical Session `01a06a33-fe42-77c3-b850-c4eeaa4c13fa`, sync accepted projections and reload only SessionReviewer.
- [ ] Controller verifies actual Obsidian project switching plus first/middle/last Q/A pages, visible final Agent answers, honest coverage and CLI/Vault hash consistency. Record results and any remaining blockers in `docs/verification/2026-09-07-visible-conversation-recovery.md`. Do not publish.
