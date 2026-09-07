# Retained Conversation Materializer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. This is the current-interface replacement for the obsolete observations-only portion of the accepted conversation-chain plan.

**Goal:** Build the accepted private bounded `conversation-chain-v1` document from authenticated visible messages and typed execution facts without copying a transcript into observations.

**Architecture:** Reuse the established visible user segmentation and stable turn identities. Join active typed revisions by authenticated source identity and source record ordinal, never by timestamps or text similarity. Return a pure canonical document and explicit materialization diagnostics for subsequent scan/store integration.

**Tech Stack:** Existing Go memory/conversationchain/redact contracts; no dependency, network or model.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§4.1,5.1,17.3,18.4, including the2026-09-07 source-prefix recovery supplement; `2026-09-04-conversation-chain-evolution-closure.md` Task1.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes.
- Only visible user/assistant messages and typed bounded tool evidence participate; hidden reasoning, system/developer instructions and raw tool output never enter the chain.
- Visible excerpts at most4096 UTF-8bytes. Source bodies are in-memory only; no `Text`, transcript or raw source body field in retained `Document`.
- Turn units use `(provider, session_id, turn_unit_id)`; actual visible user starts `[user ordinal,next actual visible user ordinal)`. Ambient-only user wrappers do not split turns.
- `execution_verified` never implies `workflow_state=resolved`; this pure task does not create milestones, problems or decisions.
- Existing canonical wire schema and `MaterializeVisible` IDs are preserved. Rule/redaction/input changes affect dependency digest; do not churn already exposed turn IDs merely to change a rule version.
- All sources must bind to the supplied SessionView identity. Source record ordering, not wall-clock ordering, controls turn assignment.
- Worktree-only implementation. No scan of daily sources, store/publication edits, Vault installation or release in this task.

### Task 1: Build canonical retained turns and attach typed evidence

**Files:** Create `internal/conversationchain/retained.go`, `retained_test.go`; modify `materialize.go` and focused tests only where shared segmentation/completeness needs correction. Existing `types.go/codec.go/validate.go` wire fields remain unchanged. If runtime validation needs a stronger timestamp/source-ordinal check, add focused tests and ensure existing canonical fixtures remain accepted.

**Interfaces:**

```go
type MaterializeInput struct {
    View memory.SessionView
    Messages []SourceMessage
    Revisions []memory.ObservationRevision
    SourceCoverage VisibleCoverage
    RuleVersion string
    RedactionVersion string
}
type MaterializeReport struct {
    UnassignedFacts uint64
    UnsupportedFacts uint64
    SourceIncomplete bool
}
func Materialize(MaterializeInput) (Document, MaterializeReport, error)
```

- [ ] RED real-value tests: one user + final answer + tools/results + next user; multiple assistant messages; commentary-only; empty final answer; final answer followed by interrupted commentary; unanswered final user; pure ambient wrapper between fragments; oversized multibyte text. Retained bytes contain only bounded excerpts, no `text` property or full tool output. Stable identical inputs produce byte-identical Render output and unchanged IDs relative to `MaterializeVisible`.
- [ ] RED ordering/identity tests: same native ID across providers, wrong source identity/hash/ordinal shape, duplicate message source coordinate/revision, duplicate fact revision, shuffled observations, timestamp collision/out-of-order clocks, multiple facts on one source record, facts before first user. A malformed timestamp does not get replaced with scan time. Invalid caller input fails closed; known source coverage gaps produce explicit report and mark potentially incomplete final answer partial.

```go
if len(doc.TurnUnits) != 2 || len(doc.TurnUnits[0].Actions) != 1 ||
    len(doc.TurnUnits[0].Results) != 1 || doc.TurnUnits[1].AnswerState != AnswerNone {
    t.Fatalf("incorrect causal segmentation: %+v", doc)
}
if bytes.Contains(body, []byte(`"text":`)) {
    t.Fatal("full visible-body property persisted")
}
```

- [ ] RED supported fact semantics: `command_started` becomes action; `command_finished` becomes result with unknown/passed/failed from typed authoritative outcome; `verification` becomes result; successful/failed patch file fact becomes action/result appropriate to its operation; commit/release/deployment/version/branch typed operations retain neutral bounded evidence. Startup/cwd/request bookkeeping is not execution. Command stdout strings or user assertions cannot create verification state. Preserve exact revision ID/source ref and do not join across Sessions.
- [ ] Run focused RED `go test ./internal/conversationchain -run 'Retained|Materialize' -count=1`.
- [ ] Implement single source-order join with bounded arrays and deterministic secondary sort `(Key.Sequence, RevisionID)`. Reuse `VisibleUserText` and stable ID derivation. Validate supplied active revisions match the SessionView's active IDs/summaries; do not accept caller-provided inactive revisions. Translate `Ref.Location.JSONL.Line` to chain record ordinal, keeping original hash. Only supported typed fields/excerpts become evidence; apply existing redaction and absolute-path sanitization before truncation. Report unassigned/unsupported facts rather than silently assigning to the first turn.
- [ ] Compute `DependencyDigest` from SessionView digest/source-record digest, ordered visible record identities/hashes, sorted active revision IDs, rule version and redaction version. Coverage reconciles source/captured/turn/unanswered/truncated counts. Canonicalize through existing Render+Parse so the returned document has its true digest, not a placeholder.
- [ ] GREEN `go test ./internal/conversationchain ./internal/inspect ./internal/source/codex -count=1`, scoped vet, `git diff --check`; one full `go test ./...` before exact-file commit. Report the first/last/middle turn and privacy test evidence. Independent task review precedes persistence integration.

## Integration rulings and next gates

Ruling: Reuse the existing visible turn IDs; put rule changes into dependency digest — source links already expose those IDs — cost if wrong is a future explicit identity upgrade, not silently broken human refs.

Ruling: Implement the pure provider-neutral builder first with Codex input, then register the other real adapters before release — this keeps one independently testable seam without reducing S11 scope — cost if wrong is provider-specific integration rework.

Ruling: Missing completion/severity signals do not authorize an implementation-stage or major-failure milestone — only typed supported facts or human confirmation qualify — cost if wrong is an honest omission instead of fabricated semantic completion.

Subsequent binding work remains required: immutable `ObjectConversationChain` + typed current/retained manifest dependencies and retention graph; scan integration immediately after SessionView materialization; bounded query fallback for source-unavailable retained excerpts; v4 generated-field rebase preserving pending human Markdown; qualified milestone projection and source-linked UI. Current `RenderMarkdownUpdate` only permits identity carry and must NOT be weakened with an unconditional bypass. Its next task must prove the generated delta and retained human values separately.
