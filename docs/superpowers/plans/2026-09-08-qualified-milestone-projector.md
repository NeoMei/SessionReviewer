# Qualified Milestone Projector Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after retained materializer/CAS/scan and historical source-reference contracts are reviewed.

**Goal:** Produce deterministic five-part milestone closures from authenticated typed facts and visible answers, with no raw-request homepage or invented semantic completion.

**Architecture:** A pure projector consumes exact SessionView/revision/chain triples already authenticated by the caller. It groups qualifying evidence by stable turn identity, emits bounded closure text and exact snapshot-qualified references, and returns generated milestones separately from accepted human content. A subsequent reviewed rebase/publication task applies this output; this task cannot bypass Markdown's existing identity-only update guard.

**Tech Stack:** Existing Go memory/conversationchain/reviewv4/presentation and redaction. No new dependency.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§2,4,4.1,5.1,9,13.13/14,17.3; replaces only obsolete Task3 projector interfaces in `2026-09-04-conversation-chain-evolution-closure.md`.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes.
- Plain user requests, generic command completion, stdout prose, patch completion alone and unconfirmed assistant statements do not qualify as milestones.
- Typed supported verification passing, commit creation, release/deployment/version changes and typed rollback qualify. Implementation-stage completion, major failure or direction change require an explicit supported typed qualifier or accepted human confirmation; never infer significance from a failed command or the word “完成”.
- HumanPresentation wins. This pure task returns generated content only and never edits decisions, workflow state, human text or accepted history.
- Visible conclusion is the last nonempty retained assistant excerpt, maximum4096 UTF-8bytes. Partial answer or source coverage stays explicit; missing sections stay empty with typed reasons.
- Each reference names true provider/Session/turn and exact SessionView digest. No time/text-similarity join across Sessions.
- Arrays remain bounded; exceeding65536 milestones fails without returning a truncated success. No raw transcript/tool output or hidden-role data in results.

### Task 1: Qualify evidence and project bounded closure baselines

**Files:** Create `internal/presentation/milestone.go`, `milestone_test.go`, `milestone_evidence.go`, `milestone_evidence_test.go`. Use existing `memory`, `conversationchain`, `reviewv4`, redaction and canonical helpers. Do not change legacy v3 projector behavior, scan/publication, wire contracts or UI in this task.

**Interfaces:**

```go
type MilestoneSessionInput struct {
    View memory.SessionView
    Chain conversationchain.Document
    Revisions []memory.ObservationRevision
}
type MilestoneInput struct {
    ProjectID, GenerationID, ProjectViewDigest string
    Sessions []MilestoneSessionInput
}
type MilestoneProjection struct {
    Timeline []reviewv4.Timeline
    ChainDependencies []reviewv4.ChainDependency
    QualifyingFacts, UnassignedQualifyingFacts uint64
}
func ProjectMilestones(MilestoneInput) (MilestoneProjection, error)
```

Input selection clarification2026-09-09: one invocation receives at most one selected current snapshot per `(provider, session_id)`, as supplied by the later scan loader's current roots. Reject duplicate Session identities even if their view digests differ; do not choose a newest snapshot or emit two versions of one stable milestone. Historical accepted sources are retained by the separate Markdown rebase, not projected again as parallel current inputs. The stable-ID evolution test calls the projector separately for the old and extended snapshots.

- [ ] RED canonical value tests: user-only produces no milestone; successful generic command and successful patch alone produce none; supported verification passed creates one; duplicate command-finished+verification result for one operation does not create two; commit/release/version facts create neutral machine-marked events. Unknown/failure outcomes do not become passed verification. A plain failure is not automatically “重大失败”.
- [ ] RED closure tests: trigger question, last nonempty answer, execution facts, verification facts and missing impact/follow-up appear in the exact existing five-part contract. Missing answer gives `ConclusionMissing`/`no_visible_answer`; no execution gives `no_execution_evidence`; no verification gives `not_verified`. Missing impact uses `not_captured`, never generated next-step prose.
- [ ] RED identity tests: mixed providers with same native ID, foreign project/view, bad chain digest, inactive/substituted revision, duplicate Session snapshot, out-of-order timestamps and shuffled input. The complete revision set must exactly reconcile View.ActiveRevisionIDs and canonical revision digests. Each chain fact matches the same view's active revision and exact source coordinate; reject malformed caller input instead of silently dropping it.

```go
got, err := ProjectMilestones(fixtureWithUserAnswerAndVerification(t))
if err != nil || len(got.Timeline) != 1 { t.Fatal("verified turn missing") }
loop := got.Timeline[0].ClosedLoop
if loop.Conclusion.Kind != reviewv4.ConclusionVisibleAnswerExcerpt || loop.ImpactAndFollowUp.State != "missing" {
    t.Fatal("answer missing or project impact invented")
}
```

- [ ] RED completeness/privacy tests: multibyte4096-byte excerpts, partial answer, oversized source gap, first/middle/last turn among thousands, capacity65537, prompt/hidden-role canaries and tool-output canaries. Excerpts are re-redacted and path-sanitized before bounded rendering. The projection returns explicit unassigned qualifying-fact count for supported evidence before the first captured user; it must not attach it to an unrelated question.
- [ ] Run RED `go test ./internal/presentation -run 'Milestone|ClosureEvidence' -count=1`.
- [ ] Implement a source-coordinate join using the exact retained chain and selected revisions. One default machine milestone per qualifying turn; neutral kind identifies its strongest supported evidence category. Derive stable ID from project/provider/Session/turn and a versioned machine category, not generation, current timestamp or free text. Typed source revisions determine event time; sort by canonical instant then stable ID. Reordering input yields identical output.
- [ ] Stable-identity clarification: the ID's versioned machine category is the fixed generator family `qualified-turn-v1`, not the currently strongest evidence kind. Adding a commit/release fact to an already qualifying verification turn may update its displayed kind but must keep the milestone ID. Test the initial verification snapshot and its extended commit/release snapshot independently, requiring one same-ID output in both; the later rebase must not append a duplicate merely because evidence became stronger.
- [ ] Build source-qualified `ChainDependencies` from exact input documents without merging different digests. Build trigger and answer from retained messages, execution/verification from closed typed labels and bounded redacted excerpts. Keep coverage explicit for partial/truncated input; set zero missing text, not placeholder prose. Return a validated result or error, never partial success at capacity.
- [ ] Include only dependency snapshots referenced by emitted milestones in output; input still validates independently. Do not alter accepted human or migrated nodes, infer formal decisions or create problem nodes. Cross-Session higher-level grouping remains possible through explicit confirmed links; the pure projector must not invent one.
- [ ] GREEN focused tests, `go test ./internal/presentation ./internal/conversationchain ./internal/reviewv4 -count=1`, scoped vet/diff, one full Go suite. Commit exact files and obtain independent task review before scan/presentation rebase wiring.

## Preflight

| Producer / consumer | Check |
|---|---|
| Typed facts / milestone qualification | Closed evidence categories, not arbitrary stdout or semantic claims |
| Chain source order / active revisions | Exact view/hash/source coordinates, no timestamp similarity |
| Stable turn / new generation | Event identity survives rescan; source snapshot remains explicit |
| Pure output / future Markdown rebase | Generated content alone is not authority to overwrite a human draft |
| Counts / UI summary | No silent list cap; unassigned evidence stays diagnosed |

Ruling: Treat stage-completion/significance prose as unqualified unless an accepted human or supported typed fact establishes it — the spec separates machine facts from human intent — cost if wrong is an omitted machine milestone that can be explicitly confirmed, not fabricated completion.

Ruling2026-09-09: distinguish fixed generator-family identity from changing strongest-evidence kind — otherwise “one milestone per turn” and “rescan-stable identity” conflict when a later scan adds stronger evidence — cost if wrong is a local generated-ID recipe adjustment before any implementation/publication, not duplicate accepted history.

Ruling2026-09-09 input selection: reject multiple selected snapshots of one Session in a pure projection call — the scan loader supplies current roots, while human/history preservation belongs to rebase; accepting both would collide on the required rescan-stable ID — cost if wrong is a bounded caller/validation correction, not choosing a historical answer implicitly.

This task does not close S02/S03 alone. Remaining mandatory steps are authenticated generated-delta rebase preserving pending human Markdown, publication graph proof, closure source controls, actual scanned candidate Vault and end-to-end acceptance.
