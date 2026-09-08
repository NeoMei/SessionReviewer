# Conversation Chain CAS Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after the pure retained materializer is reviewed; this replaces the obsolete mutable ChainStore task.

**Goal:** Authenticate and retain immutable private conversation chains as part of the existing scan generation graph.

**Architecture:** Extend memorystore's existing immutable object family, not a parallel mutable per-Session database. A manifest carries exact current-chain dependencies and separately retained historical dependencies; every chain authenticates its own SessionView and active typed evidence. The existing retention snapshot traverses these roots once per unique object.

**Tech Stack:** Existing Go memory, memorystore, conversationchain, pathguard and JSON Schema tests. No new dependency.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§9,17.3,17.4,18.4; `2026-09-04-conversation-chain-evolution-closure.md` Task2.

## Global Constraints

- Chains are private, immutable and bounded; they never enter Project/Vault sync or persist raw transcript/tool output.
- Chain identity is `(project_id, provider, session_id, session_view_digest)`, with canonical document digest and dependency digest authenticated separately.
- Source disappearance does not delete accepted excerpts or their evidence. Current and historical dependencies never impersonate each other.
- Preserve existing published/prepared pointer atomicity, private permissions, read-only access, path authentication, cancellation checkpoints and retention graph checks.
- Maximum65,536 chain dependencies per manifest across current and retained sets; existing64MiB immutable-object cap remains. Capacity failure must not truncate or advance publication.
- Zero Agent processes; no source execution, network, real-Vault writes, migration or release in this task.

### Task 1: Integrate immutable chains into manifest authentication and retention

**Files:** Create `internal/memorystore/conversation.go`, `conversation_test.go`, `internal/memory/conversation_dependency.go`, `conversation_dependency_test.go`; modify `internal/memory/types.go`, `internal/memorystore/store.go`, `retention.go`, their focused tests and `schemas/generation-manifest-v1.schema.json`. Update `internal/scan/service_test.go` only for generation-identity tests. No change to public presentation schema, source adapter or scan runtime yet.

Evidence-authentication scope: create `internal/conversationchain/evidence.go` and `evidence_test.go` for a focused pure `ValidateRetainedEvidence(document Document, view memory.SessionView, revisions []memory.ObservationRevision) error` helper. Reuse the existing retained fact policy/formatting and factor its small common join from retained.go if necessary; do not duplicate policy tables in memorystore or refactor unrelated message segmentation.

Reviewed capability inventory scope: add only the new `memorystore -> conversationchain` edge for the four existing targets to `testdata/zero-token/gate-a-production-import-edges.txt`. The exact static run reported added1/removed0; controller inspected canonical Render/Parse and shared evidence validation as its purpose. No dangerous-capability/process-site changes or broad inventory regeneration are permitted. Independent review and final frozen full Go still apply.

**Interfaces:**

```go
// Add to memory, independent of conversationchain to avoid an import cycle.
type ConversationChainDependency struct {
    Provider string `json:"provider"`
    SessionID string `json:"session_id"`
    SessionViewDigest string `json:"session_view_digest"`
    Digest string `json:"digest"`
}
// Optional GenerationManifest fields preserve old canonical bytes when absent.
// ConversationChains []ConversationChainDependency `json:"conversation_chains,omitempty"`
// RetainedConversationChains []ConversationChainDependency `json:"retained_conversation_chains,omitempty"`
// New immutable object kind: ObjectConversationChain = "conversation-chains"
func (s *Store) PutConversationChain(conversationchain.Document) (string, error)
```

The existing `LoadObjectContext(ctx,ObjectConversationChain,digest)` returns canonical bytes; no new mutable `Get(project,provider,session)` API. Extend the private `generationGraphObjects` interface and both stored/snapshot implementations with `conversationChain(context.Context,string)(conversationchain.Document,error)`. Existing generic generation hashing must include both new fields; only SessionIndexDigest remains excluded for the existing self-reference reason.

- [ ] RED memory/schema tests: optional fields omitted preserve historical fixture bytes; a current chain must match a current or cumulative retained SessionView dependency exactly. Current set allows one entry per provider/session. Retained set allows distinct historical view digests for the same provider/session but never repeats the same `(provider,session,view)` across either set. Reject invalid provider/session/digests,65,537 entries and conflicting duplicate digest bindings. Historical chains must belong to an identity still in the cumulative Session set. Use safe-ID validation for these neutral fields rather than hardcoding Codex.
- [ ] RED store tests build real temporary stores with Render/Parse canonical chain documents: repeat Put returns same digest/bytes; changed digest/body, foreign project/provider/session/view, symlink/weak permissions, truncated/duplicate JSON and unknown fields are rejected. `OpenReadOnly` can load but cannot create the object or namespace.
- [ ] RED generation authentication tests: missing chain, missing chain SessionView, wrong dependency identity and chain action/result revision absent from that view fail preparation and reload. Historical view authentication checks the immutable view's own chunks and active revision IDs without requiring it to masquerade as the current lineage head. An arbitrary non-active revision cannot be made trustworthy by placing it in a chain. Visible-message refs validate source identity/hash/ordinal shape and bind the view's source identity; they are not falsely expected to appear in machine observations.
- [ ] RED semantic proof tests: an existing active failed verification ID cannot authenticate a retained `passed` result; changing its kind, source coordinate/hash, excerpt or action/result role must fail even after recomputing chain digest. Typed evidence must reside in the source-ordinal interval of the corresponding user turn. Reject duplicate evidence and contradictory order. `ValidateRetainedEvidence` validates the supplied active view/revision set and compares each retained fact to the same deterministic supported projection used by Materialize; membership alone is insufficient. Visible message source refs remain source coordinates, not fabricated Observation IDs. Test both current and historical views through Prepare and reload.

```go
if got, err := store.PutConversationChain(chain); err != nil || got != chain.Digest {
    t.Fatalf("chain CAS: digest=%s err=%v", got, err)
}
// A retained chain with the same Session but a different historical view is
// valid only when that exact historical view and its selected facts authenticate.
```

- [ ] Run focused RED `go test ./internal/memory ./internal/memorystore -run 'ConversationChain|ChainDependency' -count=1`.
- [ ] Implement Put with existing `conversationchain.Render/Parse` plus `putImmutable`, extending object-path, decode, canonical-digest and validity switches. Factor only chain-specific checks into conversation.go; do not refactor the existing entire store. Add both dependency sets to graph reconciliation with deterministic iteration and context checks. Cache already loaded views/revisions within the existing graph walk where practical, avoiding one full generation reload per chain.
- [ ] RED retention tests: published/prepared/external pins keep current and historical chain objects, their SessionViews and observation chunks reachable; unreachable old chains appear in the report and can be cleaned only by the existing explicit cleanup path. Missing/corrupt chain roots abort cleanup. Snapshot loads shared objects once; cancellation during chain enumeration/reconciliation leaves files and pointers unchanged. Extend `retentionObjectSnapshot`, collection enumeration, manifest references and view-chunk reachability so none of the new transitive roots are omitted.
- [ ] Prove chain dependency change alters scan generation identity while absent extension keeps previous golden bytes. No generation ID self-reference is introduced by a chain; it binds SessionView rather than generation.
- [ ] GREEN focused tests, `go test ./internal/memory ./internal/memorystore ./internal/scan -count=1`, scoped vet, one full `go test ./...`, `git diff --check`. Commit exact files. Independent review must approve manifest authentication and retention together before scan wiring.

## Reconciliation and next gates

Ruling: Retained historical chain views have their own dependency set instead of being inserted into `RetainedSessionViews` — the latter intentionally disallows duplicate current Session identities and defines cumulative index membership — cost if wrong is additional private-schema rework, not silently overwritten history.

Ruling: Persist only bounded immutable chains through the existing CAS; do not introduce a second mutable per-Session store — avoids divergent recovery and permission rules — cost if wrong is adapting consumers to digest reads.

Ruling: Authenticate the retained fact's semantics and source interval, not just existence of its revision ID — otherwise a passed label could be attached to an active failed observation and survive CAS validation — cost if wrong is a small shared projection-validation helper; future changes to retained evidence rules must preserve supported historical rule semantics rather than reinterpret accepted bytes.

Subsequent required integration remains explicit: scan builds chains from authenticated visible prefixes after SessionView materialization, publication carries chain roots, read-only queries use retained excerpts when source is missing, and v4 closure projection preserves human edits. Public `ChainDependency` currently permits one snapshot per provider/session; any current/retained source-ref projection change needs a separately reviewed Go/TypeScript contract reconciliation, not an implicit validator bypass in this storage task.
