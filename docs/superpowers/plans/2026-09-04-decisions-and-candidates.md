# Decisions and Candidates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the empty “关键决策” panel with a human-owned “决策与约定” workflow for creating, editing, superseding, and confirming evidence-bound AI candidates without allowing scans or Agents to invent accepted project semantics.

**Architecture:** Confirmed decisions live in review-presentation-v4 and flow through existing semantic patches plus the three-file human publication transaction, which verifies but does not rewrite the current Session index. AI extraction writes immutable dependency-bound candidate revisions to a private annotation store under CAS. Only an explicit confirm transition creates a HumanPresentation decision revision.

**Tech Stack:** Go 1.26, existing presentation patch/sync/publication/reviewjob infrastructure, private atomic JSON store, TypeScript 5.8, Obsidian 1.13, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Prerequisites: reopened Gate 0, Session index/query, and the Obsidian five-tab shell are complete.
- Formal decisions originate only from `human_created`, `migrated`, or `ai_candidate_confirmed` provenance.
- Extraction failure, cancellation, invalid output, or candidate CAS conflict never changes the scan generation or extraction watermark.
- Candidates cite `(provider, session_id, session_view_digest, revision_id)` dependencies and contain no unverifiable confidence score.
- `confirmed`, `not_decision`, and `stale` candidate revisions are terminal; ignored may restore to pending. Stale dependencies cannot be confirmed.
- Decision supersession is acyclic. No physical delete is exposed; archive/supersede creates a new revision.
- Write commands read at most 64 KiB versioned JSON from stdin, validate review SHA and expected revision, and accept no caller-supplied file path.
- Human publication verifies `session-index.json` generation/digest but keeps its bytes unchanged.
- Decision commands and UI filter `annotation_kind` to `decision_candidate|agreement_candidate`; they never list, confirm, ignore or stale a `milestone_conclusion_candidate` through the decision workflow.

## File Structure and Ownership

- `internal/reviewv4/decision.go`: decision validation, ordering, supersession graph.
- `internal/annotation/store.go`: private candidate/extraction-run CAS records.
- `internal/decision/service.go`: create/edit/transition orchestration.
- `internal/decision/extract.go`: deterministic dependency-set identity and proposal-only job lifecycle.
- `internal/presentation/`: decision fields and semantic patch rendering.
- `internal/publication/`: three-file human transaction plus read-only index guard.
- `internal/cli/decisions.go`: fixed write/read commands.
- `obsidian-plugin/src/view/render-decisions.ts`: confirmed and candidate sections.

---

### Task 1: Persist v4 decision fields and safe supersession relationships

**Files:**
- Create: `internal/reviewv4/decision.go`, `decision_test.go`
- Modify: `internal/presentation/project.go`, `patch.go`, related tests
- Modify: `internal/reviewv2/review_markdown.go` or Gate-0 compatibility adapter
- Modify: `obsidian-plugin/src/data/markdown.ts`, `tests/markdown.test.ts`

**Interfaces:**

```go
type Decision struct {
    ID, Kind, OccurredAt, Title, Rationale, Impact, Status, ReevaluateWhen, Provenance string
    Supersedes, MilestoneIDs []string
    SessionRefs []SessionKey
    Pinned bool
    Revision int
}
func ValidateDecisionSet([]Decision) error
func OrderCurrent([]Decision) []Decision // pinned first, then occurred_at desc, stable ID
```

- [ ] **Step 1: Write RED round-trip tests** for every field, empty migrated defaults, same native Session ID across providers, pinned ordering, and revision preservation.

```go
func TestDecisionRoundTripPreservesProviderQualifiedSessionRefs(t *testing.T) {
    decision := minimumDecision()
    decision.SessionRefs = []SessionKey{{Provider: "codex", SessionID: "same"}, {Provider: "claude", SessionID: "same"}}
    got := roundTripDecision(t, decision)
    if diff := cmp.Diff(decision.SessionRefs, got.SessionRefs); diff != "" { t.Fatal(diff) }
}
```
- [ ] **Step 2: Add RED graph tests** for self-cycle, multi-node cycle, missing superseded predecessor, `status=superseded` without a successor, and archived chains.
- [ ] **Step 3: Run RED:** `go test ./internal/reviewv4 ./internal/presentation ./internal/reviewv2 -run Decision -count=1 && (cd obsidian-plugin && npm test -- markdown.test.ts)`.
- [ ] **Step 4: Implement validation and semantic field patches** for kind, reevaluation, status, relations, Session refs, pinned, and revision. Preserve unknown user Markdown outside controlled marker fields.

```go
func ValidateDecisionSet(values []Decision) error {
    byID := indexDecisions(values)
    if err := validateSupersessionTargets(byID); err != nil { return err }
    if cycle := findSupersessionCycle(byID); len(cycle) != 0 { return fmt.Errorf("decision supersession cycle: %s", strings.Join(cycle, " -> ")) }
    return validateSupersededHasSuccessor(byID)
}
```
- [ ] **Step 5: Run all gates and commit when authorized** with message `feat: persist human-owned decisions and agreements`.

---

### Task 2: Extend private AgentAnnotation CAS storage for decisions

**Files:**
- Modify: `internal/annotation/store.go`, `store_test.go`
- Modify: `internal/annotation/paths.go`, `paths_test.go`
- Modify: `internal/atomicfile/` only if a missing reusable lock primitive is proven

**Interfaces:**

```go
type Store interface {
    Load(projectID string) (annotation.ProjectState, error)
    CompareAndSwap(projectID string, expectedRevision int, next annotation.ProjectState) error
}
type ProjectState struct {
    SchemaVersion, Revision int
    Annotations []AnnotationRevision
    Runs []ExtractionRun
    LastSuccessfulDependencies map[AnnotationKind][]string
}
```

- [ ] **Step 1: Write RED tests** for private permissions, project ID path confinement, atomic replace, concurrent revision conflict, duplicate annotation revision, kind-specific filtering, terminal-state mutation rejection, stale marking, and crash recovery. Include an existing milestone conclusion candidate and prove decision operations leave it byte-identical.

```go
func TestStoreCompareAndSwapRejectsStaleRevision(t *testing.T) {
    store := openTestStore(t)
    current := minimumProjectState(2)
    saveState(t, store, current)
    err := store.CompareAndSwap("project-p", 1, current)
    if codeOf(err) != "candidate_revision_conflict" { t.Fatalf("err=%v", err) }
}
```
- [ ] **Step 2: Run RED:** `go test ./internal/annotation -run Store -count=1`.
- [ ] **Step 3: Implement one locked private project record** below the platform data root. Use safe IDs, no symlink traversal, canonical JSON, fsync/atomic replace, and typed `candidate_revision_conflict`.

```go
func (s *FileStore) CompareAndSwap(projectID string, expected int, next ProjectState) error {
    return s.withProjectLock(projectID, func(path string) error {
        current, err := s.loadLocked(path)
        if err != nil { return err }
        if current.Revision != expected { return revisionConflict(current) }
        next.Revision = expected + 1
        return atomicfile.WritePrivate(path, canonicalJSON(next))
    })
}
```
- [ ] **Step 4: Run all Go gates and commit when authorized** with message `feat: persist decision candidates under CAS`.

---

### Task 3: Create and revise formal decisions through guarded human publication

**Files:**
- Create: `internal/decision/service.go`, `service_test.go`
- Modify: `internal/publication/service.go`, `service_test.go`, `recovery_test.go`
- Modify: `internal/presentation/render.go`, `render_test.go`
- Modify: `internal/contextupdate/service.go`

**Interfaces:**

```go
type CreateRequest struct { ProjectID, ExpectedReviewSHA256 string; Input DecisionInput }
type TransitionRequest struct { ProjectID, CandidateID, Action, ExpectedReviewSHA256 string; ExpectedRevision int; Input *DecisionInput }
func (s *Service) Create(context.Context, CreateRequest) (DecisionResult, error)
func (s *Service) TransitionCandidate(context.Context, TransitionRequest) (CandidateResult, error)
```

- [ ] **Step 1: Write RED tests** for human create, candidate confirm with edits, ignore/restore/not-decision, stale candidate rejection, review-preimage conflict, candidate-revision conflict, and invalid supersession.

```go
func TestConfirmCandidateRequiresCurrentReviewPreimage(t *testing.T) {
    service := decisionServiceWithReviewSHA("new-sha")
    _, err := service.TransitionCandidate(context.Background(), TransitionRequest{ProjectID: "project-p", CandidateID: "candidate-1", Action: "confirm", ExpectedRevision: 3, ExpectedReviewSHA256: "old-sha", Input: ptr(validDecisionInput())})
    if codeOf(err) != "review_preimage_conflict" { t.Fatalf("err=%v", err) }
}
```
- [ ] **Step 2: Add transaction RED tests.** Confirming a candidate updates two Markdown files and ledger v4 atomically, verifies the bound index generation/hash, leaves index bytes unchanged, and rolls back all human files on failure.
- [ ] **Step 3: Run RED:** `go test ./internal/decision ./internal/publication ./internal/presentation -run 'Decision|Candidate|HumanPublication' -count=1`.
- [ ] **Step 4: Implement service ordering:** load accepted v4 + index guard; validate CAS/preimage; create semantic patch/new revision; render; publish three files; only then finalize candidate state. If final annotation CAS fails after publication, reconcile idempotently by accepted decision ID rather than publishing twice.

```go
accepted, index := s.loadAcceptedWithIndexGuard(request.ProjectID)
if accepted.ReviewSHA256 != request.ExpectedReviewSHA256 { return CandidateResult{}, reviewConflict(accepted) }
decision := decisionFromCandidate(candidate, request.Input)
published, err := s.publishHumanRevision(ctx, accepted, index, decision)
if err != nil { return CandidateResult{}, err }
return s.finalizeConfirmedCandidate(candidate, published.DecisionID)
```
- [ ] **Step 5: Run all Go gates and commit when authorized** with message `feat: confirm decisions through guarded publication`.

---

### Task 4: Run idempotent dependency-bound candidate extraction

**Files:**
- Create: `internal/decision/extract.go`, `extract_test.go`
- Modify: `internal/reviewjob/types.go`, `service.go`, corresponding tests
- Create: `schemas/decision-candidate-proposal-v1.schema.json`

**Interfaces:**

```go
func ExtractionIdentity(projectID, extractorVersion, promptSchemaVersion string, sortedNewSessionViewDigests []string) string
func (s *Service) StartExtraction(context.Context, ExtractRequest) (JobSummary, error)
func (s *Service) ExtractionStatus(context.Context, string) (JobSummary, error)
func (s *Service) CancelExtraction(context.Context, string, int) (JobSummary, error)
```

- [ ] **Step 1: Write RED identity tests** proving digest-order independence, changed dependency set creates a new run, and repeated clicks return the same running/completed job.

```go
func TestExtractionIdentityIgnoresDependencyInputOrder(t *testing.T) {
    left := ExtractionIdentity("project-p", "extractor-v1", "prompt-v1", []string{"sha256:b", "sha256:a"})
    right := ExtractionIdentity("project-p", "extractor-v1", "prompt-v1", []string{"sha256:a", "sha256:b"})
    if left != right { t.Fatalf("left=%s right=%s", left, right) }
}
```
- [ ] **Step 2: Write RED proposal tests.** Reject missing Session/revision evidence, stale SessionView digest, unbounded text, unknown fields, confidence numbers, attempted edits to phase/next/risk, and duplicate semantic candidates within a run.
- [ ] **Step 3: Add watermark tests.** Only a successfully persisted candidate set advances `last_successful_extraction_dependencies`; empty valid success may advance; cancellation, Agent failure, malformed output, or store failure does not.
- [ ] **Step 4: Run RED:** `go test ./internal/decision ./internal/reviewjob -run Extraction -count=1`.
- [ ] **Step 5: Reuse the configured verified proposal-only Agent handle** and existing bounded job lifecycle. The prompt receives deterministic Session summaries/evidence refs, not raw transcripts or Vault write tools.

```go
identity := ExtractionIdentity(request.ProjectID, extractorVersion, promptSchemaVersion, newDigests)
if existing, ok := state.RunByIdentity(identity); ok { return existing.Summary(), nil }
job := reviewjob.NewBoundedProposalJob(identity, buildCandidatePacket(summaries))
return s.jobs.Start(ctx, job)
```
- [ ] **Step 6: Run all Go gates and commit when authorized** with message `feat: extract evidence-bound decision candidates`.

---

### Task 5: Expose decision CLI contracts

**Files:**
- Create: `internal/cli/decisions.go`, `decisions_test.go`
- Modify: `internal/cli/run.go`, `run_test.go`, `contracts.go`

- [ ] **Step 1: Write RED exact-argv/stdin tests** for create, extract, extract status/cancel, candidate list, and candidate transition. Cover 64 KiB+1 stdin, malformed JSON, absent expected SHA/revision, unknown action/status, extra file flags, and typed conflicts.

```go
func TestDecisionCreateRejectsOversizedStdin(t *testing.T) {
    input := bytes.NewReader(bytes.Repeat([]byte("x"), (64<<10)+1))
    code := runDecisions([]string{"create", "--project-id", "project-p", "--expected-review-sha256", validSHA, "--json"}, input, io.Discard, io.Discard)
    if code != 2 { t.Fatalf("code=%d", code) }
}
```
- [ ] **Step 2: Run RED:** `go test ./internal/cli -run Decisions -count=1`.
- [ ] **Step 3: Add `decisions` root dispatch** through Gate-0 grammar and `decision.Service`; return versioned JSON only, never human prose mixed into stdout.

```go
case "decisions":
    return runDecisions(args[1:], os.Stdin, stdout, stderr, decisionDependencies())
```
- [ ] **Step 4: Run all Go gates and commit when authorized** with message `feat: expose decision lifecycle CLI`.

---

### Task 6: Render confirmed decisions, empty guidance, and candidates in Obsidian

**Files:**
- Modify: `obsidian-plugin/src/contracts/review-v4.ts`
- Modify: `obsidian-plugin/src/cli/runner.ts`, `tests/cli.test.ts`
- Modify: `obsidian-plugin/src/view/render-decisions.ts`
- Modify: `obsidian-plugin/src/styles.css`
- Create: `obsidian-plugin/tests/decisions-v4-view.test.ts`

- [ ] **Step 1: Write RED view tests** for no-decision explanatory copy, exactly two actions, active-only default, archived/superseded filters, top-three homepage order, provenance, reevaluation text, Session/milestone links, and supersession chain.

```ts
it("explains an empty decision set and exposes two explicit actions", () => {
  const panel = renderDecisions(modelWithoutDecisions(), noopUpdate, handlers());
  expect(panel.textContent).toContain("扫描已经保存项目事实，但不会替你判断项目意图");
  expect(panel.querySelectorAll("[data-decision-empty-action]")).toHaveLength(2);
});
```
- [ ] **Step 2: Add RED candidate interactions** for start/status/cancel, pending cards with evidence, edit-confirm, ignore, not-decision, restore, stale disabled confirmation, CAS refresh, and zero confidence display.
- [ ] **Step 3: Run RED:** `cd obsidian-plugin && npm test -- decisions-v4-view.test.ts cli.test.ts`.
- [ ] **Step 4: Implement full CLI methods and UI states** using fixed argv and bounded stdin. On a write success reload the four-file repository; on typed conflict show current summary and preserve unsaved form text. Keep the five-tab order and problem view state unchanged.

```ts
async function confirmCandidate(candidate: CandidateRevision, input: DecisionInput): Promise<void> {
  await cli.transitionCandidate(model.review.projectId, candidate.id, candidate.revision, "confirm", model.source.reviewSha256, input);
  await reloadProject();
}
```
- [ ] **Step 5: Run `npm run check` and commit when authorized** with message `feat: add decisions and agreements workflow`.

---

### Task 7: Real acceptance and semantic-boundary audit

**Files:**
- Create: `docs/session-review/decisions-acceptance.md`

- [ ] **Step 1: In a disposable real Vault with no decisions,** confirm the explanation and both actions render instead of a blank panel.
- [ ] **Step 2: Create one decision and one agreement manually,** edit all fields, pin one, supersede one, archive one, reload Obsidian, and verify Markdown remains human-readable/editable.
- [ ] **Step 3: Extract candidates from newly indexed Sessions,** inspect cited facts, edit-confirm one, ignore/restore one, mark one not-decision, then rescan and verify stale dependency behavior.
- [ ] **Step 4: Inject Agent failure, cancellation, malformed proposal, review edit conflict, and annotation CAS conflict.** Verify no scan generation or watermark incorrectly advances and no mixed publication appears.
- [ ] **Step 5: Audit accepted decisions:** each must have one allowed provenance and an explicit human action in the evidence trail; deterministic scan output alone must never create one.
- [ ] **Step 6: Record installed bundle hashes, generation/revisions, commands, screenshots, and results in the acceptance document; commit when authorized.**
