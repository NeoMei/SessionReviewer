# Scan Retained Conversations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after provider-neutral contracts, retained materializer and conversation CAS are independently approved.

**Goal:** Build authenticated retained conversation evidence during ordinary scans and use it for source-unavailable drilldown without copying transcripts into the Vault.

**Architecture:** A provider capability reads a precisely authenticated source prefix using its existing private root. Scan passes visible messages and the exact active typed revisions into the pure materializer, then persists the chain in the existing immutable generation graph. Inspect authenticates the published chain before attempting optional full-body reads; retained excerpts remain available when original source reads fail.

**Tech Stack:** Existing Go source manager, scan, conversationchain, memorystore and inspect; existing TypeScript conversation-page consumer. No dependencies or Agent starts.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§4.1,5.1,7,9,10,17.3/4,18.4. This replaces scan/query integration portions of the original conversation-chain plan, not its milestone/confirmation/UI requirements.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes.
- Only visible user/assistant messages and bounded typed facts; no hidden reasoning, system/developer instructions, raw tool output or persistent full transcript.
- Identity always includes provider and Session. Read exactly the accepted prefix, never append content newer than its authenticated boundary.
- Existing chain Document, stable turn IDs, private manifest dependencies and public paging bounds remain authoritative.
- Repeated identical scans must reuse canonical chain and generation bytes; a changed source, active fact, rule or redaction dependency changes its binding.
- Preserve current/retained chain authentication, source-catalog CAS, prepared/published pointer atomicity, cancellation and byte budgets.
- Full visible source bodies remain in-memory only. Retained messages contain at most4096 UTF-8bytes; on-demand source records at most64KiB and responses at most1MiB.
- No real source scan, daily-Vault install, migration or release in these tasks. Real candidate acceptance follows complete implementation.

### Task 1: Bind source-prefix capability and immutable chains into scan

**Files:** Create `internal/source/visible.go`, `internal/source/visible_test.go`, `internal/scan/conversation.go`, `internal/scan/conversation_test.go`; modify `internal/source/manager.go`, Codex visible reader, `internal/scan/service.go` and focused tests. Update `test/zerotoken` only to preserve its static capability inventory for this explicitly deterministic reader, not to remove the zero-Agent boundary.

**Interfaces:** Reuse reviewed `conversationchain.Materialize(MaterializeInput)` and `Store.PutConversationChain(Document)`. Add provider capability:

```go
type VisibleReader interface {
    ReadVisiblePrefix(context.Context, memory.SourceRecord) ([]conversationchain.SourceMessage, conversationchain.VisibleCoverage, error)
}
```

The method lives on actual adapters and Manager dispatches by `record.Provider`. Codex delegates to `ReadPublishedVisible(ctx, adapter.sessionsRoot, record)` after checking record project association against adapter bindings. Method naming does not imply the record is already published: authentication is its exact frozen prefix. Unregistered/mismatched provider and invalid source identity fail closed. An adapter without the capability returns a typed unsupported-capability error; it never fabricates an empty complete chain.

- [ ] RED real scan fixtures: user+answer+verification produces one chain dependency, canonical chain object and correct typed evidence; next user creates a second unit. Forbidden roles/tool output never appear in persisted chain bytes. Source changed after freeze must not produce a chain with the old source hash. Follow the existing isolated temporary source/catalog/store fixture pattern.
- [ ] RED source Manager dispatch tests verify provider spoof, unsupported capability, no fallback to another provider, exact root binding and no lease leak. Codex reads only accepted prefix even if bytes append after decode; all same-prefix namespace/hash checks remain active.
- [ ] RED scan lifecycle tests: initial, identical repeat, append, decoder supersession, source unavailable, restored source, historical chain still referenced, cancellation before persistence and before prepared advancement. Existing indexed Session identity and coverage stay cumulative; retained history remains reachable. Capacity failure leaves publication unchanged.

```go
if len(manifest.ConversationChains) != 1 { t.Fatal("scan omitted retained chain") }
body, err := store.LoadObject(memorystore.ObjectConversationChain, manifest.ConversationChains[0].Digest)
if err != nil || bytes.Contains(body, []byte(`"text":`)) { t.Fatal("invalid private retained representation") }
```

- [ ] Run RED `go test ./internal/source ./internal/source/codex ./internal/scan -run 'VisiblePrefix|ScanConversation|RetainedConversation' -count=1`.
- [ ] Implement chain planning immediately after each SessionView and active-lineage reconciliation, before manifest generation identity. Feed exactly active revisions from the view, including reused revisions when needed; reuse existing chunk loaders and verify revision digest rather than treating the newest spool as the whole history. Pass source coverage, deterministic rule/redaction versions and sanitized excerpts through the pure builder.
- [ ] Persist chains after their SessionViews/chunks and before `PrepareGeneration`/`AdvancePrepared`. Add new roots before generation hash calculation. Carry prior authenticated chain roots as history when their `(provider,session,view)` differs, never duplicate a current root in retained roots. Missing original source may retain existing authenticated chain; if none exists, preserve index and explicit capability/source-unavailable state rather than a forged complete chain. Invalid canonical materialization or store integrity errors abort preparation.
- [ ] Bound cumulative chain storage and use cancellation checks per Session. Do not keep every full transcript in a project-wide slice; release visible message bodies after each pure materialization. Compare repeated chain bytes and generation IDs in the unchanged fixture.
- [ ] GREEN focused tests, `go test ./internal/source/... ./internal/scan ./internal/memorystore ./test/zerotoken -count=1`, scoped vet and full `go test ./...`; commit exact files. Independent task review covers source authentication and generation lifecycle together.

### Task 2: Read retained excerpts and authenticated bodies through one paged experience

**Files:** Create `internal/inspect/conversation_retained.go`, `conversation_retained_test.go`; modify `internal/inspect/conversation.go`, `service.go` only for its authenticated callback context, conversation-page contracts and plugin parser/renderer tests if a bounded explicit response extension is required. Do not remove prior source-prefix fallback for pre-chain generations.

**Interfaces:** Preserve `LoadConversationPage(ctx, ConversationRequest)` and its fixed CLI argv. Supply store/manifest through the already authenticated inspection callback instead of opening the complete generation again for every turn. Current chain lookup is exact `(provider,session,view)`; any historical lookup must be a manifest-retained root and must not silently use another view's content as current.

- [ ] RED published-store tests: source available yields full visible bodies from authenticated prefix; removing the source still yields retained user/assistant excerpts and bounded action/result evidence. Missing chain in an old generation still uses the original authenticated source-prefix path. Corrupt/mismatched chain or selected revision fails closed, not fallback success. Body-unavailable state is distinguishable from answer-not-captured.
- [ ] Cover source removal followed by a successful new scan, not only removal before a read: `sessionview.Materialize` changes the SessionView digest when availability changes, even while carrying the old active facts. With no exact current chain, a retained answer is historical and must carry its actual evidence-view digest separately from the current index-view digest. Never overwrite its document binding or select the lexically largest hash/most recent timestamp. Automatic retained selection is allowed only for a unique manifest-retained chain whose authenticated view has the same source identity and exact active revisions, and whose source-record digest matches the current authenticated record with only `Availability` restored to `SourceAvailable`. This proves the original frozen boundary and all remaining source fields agree. If no unique match exists, return an explicit unavailable/ambiguous retained-evidence state; explicit snapshot selection belongs to the subsequent historical-bindings task. Test two earlier snapshots, source replacement, changed usage, differing active revisions, ambiguous candidates, and removed/restored source.
- [ ] RED page tests cover first/middle/last, large multibyte messages, response-budget reduced page length with next cursor, same native IDs across providers, switched generation/turn/limit, removed source and restored source. A cursor must bind whether its dependency is retained or source-backed so changes cannot reorder evidence silently. Returned action/result data must authenticate against the selected view.
- [ ] Close reproduced legacy/retained answer-state divergence: controller pure probe at1b39b35 reports `answered` for both a final answer followed by interrupted commentary and an empty final answer through `MaterializeVisible`, while retained materialization correctly reports `partial`. Add exact source-backed inspect regressions and shared classification tests for these cases, only-commentary, unanswered, normal final and oversized/malformed source gaps. Make pre-chain fallback and retained-query states agree conservatively; source coverage gaps must not be erased by the later assignment of page.Coverage. Reuse one small pure classification helper if needed in `internal/conversationchain/materialize.go`/`retained.go` and tests, preserving exposed turn IDs and full-source/retained-body distinctions. This is a required query integration bugfix, not optional polish.
- [ ] Run RED `go test ./internal/inspect ./internal/cli -run 'RetainedConversation|ConversationCursor|ConversationReadOnly' -count=1`.
- [ ] Refactor query into authenticated chain selection, optional body read and page rendering. Source-read failure may degrade to already authenticated excerpts only; invalid private graph cannot degrade into success. Preserve true original source references and record ordinals. Never set full-body availability merely because an excerpt exists. Account explicitly for uncaptured/oversized records; no invented role or missing-answer count.
- [ ] If current page shape cannot express retained-only bodies and typed evidence honestly, first add optional backward-compatible fields with strict Go/TypeScript/JSON-Schema fixtures, unchanged legacy golden bytes when absent, and fixed byte/item caps. Do not overload `text` with an excerpt while claiming it is a complete body. No arbitrary response field or caller-supplied file path is permitted.
- [ ] GREEN inspect/CLI tests, full Go suite, full plugin check, read-only filesystem/hash/cancellation tests. Commit exact files and obtain independent spec/quality review before milestone UI wiring.

## Preflight and integration boundaries

| Producer / consumer | Resolution |
|---|---|
| Existing source Adapter / new visible reader | Optional capability preserves synthetic adapters and registered provider boundary; unsupported is explicit, not empty success |
| SourceMessage / retained Document | Full text exists only during one Session build; storage remains bounded canonical excerpts |
| SessionView active IDs / new spool | Authenticate full selected active revisions; never omit reused facts merely because spool is incremental |
| Chain CAS / generation identity | Populate roots before hashing; persist dependencies before preparing pointer |
| Current chain / historical root | Preserve both under distinct manifest sets and exact identity; never relabel history as current |
| Retained excerpts / expanded body UI | Explicit availability/truncation semantics; any wire extension requires Go/TypeScript/schema fixtures |

Ruling: Source disappearance creates a new availability-bound view, not a new answer — retained-only fallback must separately identify the current index view and exact historical evidence view, with unique content-bound selection as above — cost if wrong is a bounded response extension or an explicit unavailable result, not fabricated current provenance. This reconciles the existing exact-match requirement with the accepted source-disappearance workflow without changing the in-progress CAS task.

This plan does not implement automatic milestones, generated Markdown rebase, confirmed problem structure, decisions or pricing. Those remain release blockers. Provider-specific Claude/OpenCode capability implementations follow their current-contract adapter tasks; a generic capability alone does not satisfy provider parity.
