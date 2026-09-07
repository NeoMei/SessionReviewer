# Generated Milestone Publication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after the qualified milestone projector and historical binding tasks are reviewed.

**Goal:** Populate the real v4 evolution view during a scan while preserving accepted and pending human Markdown exactly where humans own it.

**Architecture:** A narrow generated-milestone delta is applied to a draft authenticated against its old ledger. The renderer recomputes the permitted merge and rejects any caller-supplied presentation differing from that result. Publication also authenticates public chain references against current/retained private roots before the existing four-file transaction advances.

**Tech Stack:** Existing reviewv4 Markdown registry, presentation render, memory CAS, contextupdate and publication locks. No dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§4.1,9,10,17.3/4; `2026-09-05-v4-human-markdown-codec.md`; qualified projector and historical binding plans.

## Global Constraints

- HumanPresentation > deterministic projection. A scan cannot create a formal decision, rearrange problems, resolve a workflow, invent a follow-up or replace a human conclusion.
- Authenticate pending Markdown through ParseMarkdownDraft against the old accepted ledger before accepting any generated delta. No unconditional `validatedDraft=true` escape hatch or removal of the existing equality guard.
- Scan publication remains two Markdown files, ledger and index atomically. Candidate/human edits remain three files with an exact index preimage guard.
- New generated content must have authenticated private evidence; a source-turn triple alone is not private-store proof.
- Source disappearance retains accepted history and human fields; no automatic deletion, migration or forgetting.
- Ordinary scans start zero Agent processes. No network, daily-Vault mutation or release in these task commits.

### Task 1: Rebase a narrow milestone delta over an authenticated human draft

**Files:** Create `internal/reviewv4/markdown_milestone_update.go`, `markdown_milestone_update_test.go`; modify `markdown_render.go`, `markdown_baseline.go`, `markdown_lookup.go` only for shared helpers and focused regressions. Modify `internal/presentation/render_v4.go` and tests. Do not modify arbitrary presentation fields or the legacy renderer.

**Interfaces:**

```go
type ScanMilestoneUpdate struct {
    ProjectID, GenerationID, ProjectViewDigest string
    Timeline []Timeline
    ChainDependencies []ChainDependency
}
func RebaseMarkdownMilestones(oldLedger MachineLedger, pending MarkdownPair, update ScanMilestoneUpdate) (Presentation, error)
func RenderMarkdownMilestoneUpdate(next Presentation, oldLedger MachineLedger, pending MarkdownPair, update ScanMilestoneUpdate) (MarkdownPair, error)
```

Add optional `MilestoneUpdate *reviewv4.ScanMilestoneUpdate` to V4RenderInput. Existing no-delta callers continue using RenderMarkdownUpdate unchanged. The new renderer calls RebaseMarkdownMilestones itself, compares the complete expected presentation with `next`, then uses the already proven merge. The trusted input shape contains no decision/problem/current-state payload.

- [ ] RED real Markdown fixtures: initial generated milestone append; later append plus a pending edited project goal; accepted and pending milestone title/summary/conclusion/impact edits; unknown blocks/comments/CRLF preserved; unchanged repeated render byte-identical; exactly one accepted revision increment for combined scan and pending edits. Tampered generated source block, old ledger hash, wrong project, changed human field or malformed baseline/patch fails.
- [ ] RED baseline tests: only generator-owned fields can update. Ownership requires matching valid generated baseline metadata and the machine event identity, not merely an ID prefix. An existing human/migrated node with no proven generated ownership stays untouched; collision with a new machine ID is rejected. Store generated defaults for title/summary/conclusion/impact using existing scalar hash rules. Rebase active human values over changed defaults, preserving explicit confirmed conclusion kind and references; update baseline bindings consistently and validate all patches/orphans.
- [ ] Preserve every old timeline entity; update only proven machine-owned ones, append new ones and carry exact historical dependencies needed by old references. Before adding ambiguous new snapshots, qualify legacy refs only from their unique authenticated old dependency. Do not mutate decision graph, problem structure/state, risks/open loops or current-state text. All non-milestone metadata can change only via existing exact scan-identity carry.

```go
next, err := RebaseMarkdownMilestones(oldLedger, editedPair, generated)
if err != nil || next.CurrentState.Goal != "human goal" { t.Fatal("human draft lost") }
if _, err := RenderMarkdownMilestoneUpdate(tamperGoal(next), oldLedger, editedPair, generated); err == nil {
    t.Fatal("arbitrary mapped presentation bypassed merge proof")
}
```

- [ ] Run RED `go test ./internal/reviewv4 ./internal/presentation -run 'MarkdownMilestone|GeneratedMilestone' -count=1`; implement with existing field registry and canonical cloning. Reuse appendNewMarkdownEntities after proof, not a second Markdown parser. Bounds/overflow return failure without partially rendered output.
- [ ] GREEN focused suites, full Go, full plugin canonical check, vet/diff clean; commit exact files and independent review before scan integration.

### Task 2: Wire authenticated private evidence into the real scan publication

**Files:** Create `internal/contextupdate/milestones.go`, `milestones_test.go`, `internal/publication/chain_binding.go`, `chain_binding_test.go`; modify `contextupdate/service.go`, `v4.go`, relevant tests, `publication/service.go`, and `syncproject/markdown.go` only if its binding helper must share the same proof. Add focused `test/zerotoken` end-to-end assertions without weakening capability gates.

**Interfaces:** Reuse `presentation.ProjectMilestones(MilestoneInput)` and the new ScanMilestoneUpdate. A private loader in contextupdate reads already prepared current chain roots with their exact views/revisions once; it verifies the manifest's graph and passes no raw source text to the projector. Publication checks every public ChainDependency and source-turn ref against a current/retained manifest root and the exact chain DependencyDigest/turn set.

- [ ] RED real temporary project+Vault scan: user+answer+verification produces a qualified five-part milestone in both Markdown and ledger; user-only produces no milestone. Second identical scan is byte-stable; append creates/update evidence without losing prior entries. Source loss and restoration preserve history with exact old refs. Two providers with equal native IDs do not merge.
- [ ] RED human edit→scan test through the actual contextupdate/publication path, not pure helper only: edit goal and milestone conclusion in the Vault, append a new supported source fact, scan, and verify both project/Vault texts and patch/baseline metadata. Inject concurrent Project/Vault changes at each preimage boundary and require no overwritten human bytes or mixed generation.
- [ ] RED publication-authentication tests: invented chain digest, valid but unreferenced private object, wrong historical view, foreign project/provider and missing turn fail before journal/pointer/public writes. Re-signing public ledger hashes alone must not make a fabricated private reference acceptable.
- [ ] Integrate both initial and existing v4 branches. Existing branch authenticates human draft under publication owner, constructs projector output from prepared immutable objects, rebases with the narrow update and passes all four exact preimages. Initial branch installs the generated baselines before first render. Keep current-state semantic fields human-owned; no default invented project goal/stage/next step.
- [ ] Instrument ordinary scan and renderer paths for zero Agent starts; source-prefix reads and permitted git probe stay independently audited. Test cancellation before rendering and during publication, capacity overflow and corrupt graph recovery. No pricing/candidate side effect belongs in this task.
- [ ] GREEN affected contextupdate/publication/syncproject/reviewv4/zerotoken suites, full Go, full plugin check, vet/diff. Independent review and real candidate-Vault closure/source UI acceptance remain separate gates.

## Preflight

| Shared interface | Check |
|---|---|
| Task1 delta / draft | Input shape cannot carry unrelated semantics; complete expected-presentation comparison remains mandatory |
| Task1 baselines / old human patches | Valid old binding first, preserve value then consistently rebind generated default |
| Task1/Task2 render input | Projector supplies same narrow update renderer recomputes; no caller bypass boolean |
| Task2 public refs / private manifest | Authenticated current/historical root and exact turn membership required |
| Task2 journal / four-file sync | Existing publication ownership, preimages and recovery retained |

Ruling: Keep existing human and historical entities even when a new machine projection omits them — absence is not authorization to delete — cost if wrong is retained obsolete history requiring explicit later management, not lost user work.
