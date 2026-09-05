# Problem Map and Placement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one human-authoritative project problem tree across all Sessions and providers, with zero-token placement recommendations and explicit confirmation before structural changes.

**Architecture:** Formal problem nodes live in `review-presentation-v4` and reference private conversation turn units by `(provider, session_id, turn_unit_id)`. A deterministic rules engine creates private `problem-map-candidate-v1` recommendations from explicit structural and evidence signals; an Agent can be requested only for unresolved ambiguity and its digest-bound output remains a candidate. Obsidian renders a five-tab shell with a stable left tree, current path, Q/A evidence detail and a bottom placement drawer.

**Tech Stack:** Go 1.26, existing HumanPresentation patch/publication flow, canonical JSON and CAS, TypeScript 5.8, Obsidian 1.13, Vitest/jsdom.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Prerequisites: reopened Gate 0 Task 7 and `2026-09-04-conversation-chain-evolution-closure.md` are complete.
- The project has one formal problem graph across Codex, Claude Code and OpenCode; filters affect visibility only, never graph authority.
- Every node has at most one primary parent and two related nodes. Formal graphs are acyclic and persist no canvas coordinates.
- Formal structure changes require user action plus review SHA, graph revision and candidate revision CAS. No free drag writes hierarchy.
- Deterministic placement runs first and costs zero tokens. If it cannot place reliably, the question remains pending; it never launches an Agent automatically.
- `answer_state=execution_verified` does not set `workflow_state=resolved`. Resolution requires explicit user confirmation or accepted project acceptance evidence.
- Candidate merge preserves all source turn references. Human text and ordering edits survive rescans and sync.
- The left rail uses real question sentences only; top-level content categories remain exclusively in the top tab bar.

## File Structure and Ownership

- `internal/problemmap/rules.go`: deterministic candidate signals and confidence level.
- `internal/problemmap/store.go`: private candidate revisions and dependency invalidation.
- `internal/problemmap/graph.go`: formal graph invariants, move/merge/reorder operations and previews.
- `internal/presentation/problems.go`: HumanPresentation patch baselines and Markdown projection.
- `internal/cli/problems.go`: fixed read/write command handlers and CAS.
- `obsidian-plugin/src/view/render-problems.ts`: three-pane problem context view and pending drawer.
- `obsidian-plugin/src/state/problem-state.ts`: selected path, filters and pending candidate state.
- `obsidian-plugin/src/view/problem-action-modal.ts`: move/merge/reorder confirmation with affected paths.

---

### Task 1: Generate zero-token placement candidates from explicit evidence

**Files:**
- Create: `internal/problemmap/rules.go`, `rules_test.go`
- Modify: `internal/conversationchain/types.go`
- Modify: `internal/projectview/reduce.go`, `reduce_test.go`

**Interfaces:**

```go
type PlacementInput struct {
    ProjectID string
    Question string
    SourceTurns []reviewv4.SourceTurnRef
    Graph []reviewv4.ProblemNode
    Evidence []Fact
    RuleVersion string
}
func RecommendPlacement(PlacementInput) problemmap.Candidate
```

- [ ] **Step 1: Write failing rule tests.** Cover explicit numbered headings, quoted parent question, shared file/symbol/commit/error signature, immediate follow-up, conflicting signals, no signal and provider-neutral duplicate Session IDs.

```go
func TestNoReliableSignalKeepsQuestionPendingWithoutAgent(t *testing.T) {
    got := RecommendPlacement(input("How should this work?", graphWithUnrelatedNodes()))
    if got.RecommendedRelation != problemmap.KeepPending || got.RecommendedTargetID != nil { t.Fatalf("invented placement: %+v", got) }
    if got.AnalysisMode != problemmap.AnalysisDeterministic || got.AgentRunID != nil { t.Fatalf("started agent: %+v", got) }
}
```

- [ ] **Step 2: Define deterministic precedence.** Explicit stable problem ID wins, then document heading/numbering, then quoted question, then exact shared evidence, then immediate follow-up. Conflicting top-rank signals produce `keep_pending`; lower ranks cannot break a conflict.

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/problemmap ./internal/projectview -run 'Placement|Rule|Pending' -count=1`

- [ ] **Step 4: Implement readable grounds and coarse confidence.** Emit `high` only for explicit ID or unambiguous hierarchy, `medium` for at least two independent exact evidence signals, and `low` otherwise. Return one primary target, at most two alternates and two related nodes using stable `(rank, first_proposed_at, id)` sorting.

- [ ] **Step 5: Run GREEN plus zero-Agent instrumentation.**

Run: `gofmt -w internal/problemmap internal/projectview && go test ./internal/problemmap ./internal/projectview ./test/zerotoken -count=1`

- [ ] **Step 6: Commit when authorized.**

```bash
git add internal/problemmap internal/conversationchain internal/projectview test/zerotoken
git commit -m "feat: recommend problem placement without tokens"
```

---

### Task 2: Persist candidates and cache optional Agent analysis by dependencies

**Files:**
- Create: `internal/problemmap/store.go`, `store_test.go`, `agent.go`, `agent_test.go`
- Modify: `internal/agent/codex/run.go`, `run_test.go`
- Create: `internal/cli/problems.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**

```go
type CandidateStore interface {
    List(context.Context, string, problemmap.CandidateStatus) ([]problemmap.Candidate, error)
    CompareAndSwap(context.Context, problemmap.Candidate, int) error
}
func AnalysisIdentity(projectID, normalizedQuestion, ruleVersion string, dependencies []string) string
func (s *Service) RequestAgentPlacement(context.Context, AgentPlacementRequest) (problemmap.Candidate, error)
```

- [ ] **Step 1: Write failing store tests.** Assert stable identity, revision CAS, pending/kept/stale transitions, dependency invalidation, concurrent writers and recovery after atomic-write interruption.

- [ ] **Step 2: Write failing Agent-cache tests.** The first explicit request invokes the proposal-only Agent once; the same normalized question and sorted dependency digests return the stored run; changed dependency starts one new run; failed/invalid output does not advance the successful dependency set.

```go
func TestRepeatedAgentPlacementReusesDependencyBoundRun(t *testing.T) {
    first := requestPlacement(t, service, requestWithDeps("d2", "d1"))
    second := requestPlacement(t, service, requestWithDeps("d1", "d2"))
    if first.CandidateID != second.CandidateID || fakeAgent.Starts() != 1 { t.Fatalf("cache miss: %d", fakeAgent.Starts()) }
}
```

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/problemmap ./internal/agent/codex ./internal/cli -run 'Candidate|Placement|Dependency' -count=1`

- [ ] **Step 4: Implement private canonical storage and stale detection.** Store no candidate in Project/Vault. Validate all referenced turn and problem revisions before list/transition. Use process-safe locking and atomic replacement consistent with existing review-job storage.

- [ ] **Step 5: Restrict the optional Agent.** Pass only bounded visible question/evidence summaries and the current candidate targets; require strict candidate JSON; reject hidden-role fields, unsupported target IDs, invented source refs and extra prose.

- [ ] **Step 6: Run full Go gates and commit when authorized.**

```bash
git add internal/problemmap internal/agent/codex internal/cli
git commit -m "feat: persist problem placement candidates"
```

---

### Task 3: Apply formal graph changes through HumanPresentation CAS

**Files:**
- Create: `internal/problemmap/graph.go`, `graph_test.go`
- Create: `internal/presentation/problems.go`, `problems_test.go`
- Modify: `internal/cli/problems.go`
- Create: `internal/cli/problems_test.go`
- Modify: `internal/publication/service.go`, `service_test.go`
- Modify: `internal/syncproject/service.go`, `service_test.go`

**Interfaces:**

```go
func ApplyCandidate(graph Graph, candidate Candidate, action ApplyAction, targetID *string) (Graph, error)
func PreviewMove(graph Graph, problemID string, newParentID *string) (MovePreview, error)
func Move(graph Graph, MoveRequest) (Graph, error)
func Reorder(graph Graph, parentID *string, orderedChildIDs []string) (Graph, error)
```

- [ ] **Step 1: Write failing graph tests.** Cover child, sibling, merge, keep pending, root move, subtree move, cycle rejection, related-node cleanup, missing/duplicate reorder IDs and stable sibling order.

```go
func TestMergePreservesEverySourceTurn(t *testing.T) {
    got := ApplyCandidate(graphWith("target", refs("codex/s1/t1")), candidate(refs("claude/s2/t2")), Merge, ptr("target"))
    assertRefs(t, got.Node("target").SourceTurnRefs, "codex/s1/t1", "claude/s2/t2")
}
```

- [ ] **Step 2: Write failing status tests.** Adding verified execution promotes only answer state; resolving requires `ConfirmResolved=true` or an accepted verification reference whose acceptance record explicitly names the problem ID.

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/problemmap ./internal/presentation ./internal/cli ./internal/publication ./internal/syncproject -run 'Problem|Graph|Candidate|Reorder' -count=1`

- [ ] **Step 4: Implement pure graph operations, then wrap them in one locked CAS transaction.** Validate review SHA, graph revision, candidate revision, target revisions and publication preimages before rendering. On any error write no Markdown, ledger, candidate state or sync pointer.

- [ ] **Step 5: Project the formal tree into existing Markdown.** Add one bounded “问题脉络” block in `项目回顾.md`; keep full evidence private, preserve unknown blocks and use generated baselines/human patches for editable question, conclusion, criterion and state fields.

- [ ] **Step 6: Run full gates and commit when authorized.**

Run: `gofmt -w internal/problemmap internal/presentation internal/cli internal/publication internal/syncproject && go test ./... && go vet ./... && go mod tidy -diff`

```bash
git add internal/problemmap internal/presentation internal/cli internal/publication internal/syncproject
git commit -m "feat: apply human-confirmed problem graph changes"
```

---

### Task 4: Build the five-tab Obsidian problem context view

**Files:**
- Create: `obsidian-plugin/src/state/problem-state.ts`, `state/problem-state.test.ts`
- Create: `obsidian-plugin/src/view/render-problems.ts`, `render-problems.test.ts`
- Create: `obsidian-plugin/src/view/problem-action-modal.ts`
- Modify: `obsidian-plugin/src/view/render-shell.ts`, `project-view.ts`, `styles.css`
- Modify: `obsidian-plugin/src/state/store.ts`, `data/repository.ts`, `cli/runner.ts`
- Modify: `obsidian-plugin/tests/view.test.ts`, `accessibility.test.ts`, `styles.test.ts`, `cli.test.ts`

**Interfaces:**

```ts
export type ViewKind = "evolution" | "problems" | "decisions" | "sessions" | "usage";
export function renderProblems(model: BrowserModelV4, state: ViewState, actions: ProblemActions): HTMLElement;
export interface ProblemActions {
  selectProblem(id: string): void;
  transitionCandidate(request: CandidateTransitionRequest): Promise<void>;
  moveProblem(request: MoveProblemRequest): Promise<void>;
  reorderChildren(request: ReorderProblemRequest): Promise<void>;
  openTurn(ref: SourceTurnRef): Promise<void>;
}
```

- [ ] **Step 1: Write failing shell tests.** Require exact tab order `项目演进 / 问题脉络 / 决策与约定 / 全部 Sessions / 用量`, roving tab focus and persistence migration from the old four-tab state.

- [ ] **Step 2: Write failing tree/layout tests.** The left rail contains only question text and workflow state; selecting a node shows its ancestor path, direct children, up to two related nodes and right-side Q/A chain; collapsed branches keep descendant counts without flattening hierarchy.

```ts
it("does not duplicate top categories in the problem tree", () => {
  const panel = renderProblems(model(), state(), actions());
  const tree = panel.querySelector('[role="tree"]')!;
  expect(tree.textContent).not.toContain("决策与约定");
  expect(tree.textContent).not.toContain("模型价格");
});
```

- [ ] **Step 3: Write failing candidate interaction tests.** Show recommended relation/target, two alternates, related nodes, grounds and confidence. Cover child, sibling, merge, keep pending, stale candidate refresh and CAS conflict without optimistic tree mutation.

- [ ] **Step 4: Run RED.**

Run: `cd obsidian-plugin && npx vitest run tests/view.test.ts tests/accessibility.test.ts tests/styles.test.ts tests/cli.test.ts src/state/problem-state.test.ts src/view/render-problems.test.ts`

- [ ] **Step 5: Implement the stable three-pane layout.** Use semantic `tablist`, `tree`, `treeitem`, headings and buttons; compute indentation from parent relations rather than stored coordinates; show the bottom pending drawer only when a candidate is selected. At narrow width stack the evidence panel below without changing tree order.

- [ ] **Step 6: Implement confirmation modals.** Move previews show old path, new path and affected subtree. Merge previews list all source turns retained. Reorder submits the complete direct-child ID array. Announce success/error through one polite live region.

- [ ] **Step 7: Run plugin gates and commit when authorized.**

```bash
git add obsidian-plugin/src obsidian-plugin/tests
git commit -m "feat: add Obsidian problem context view"
```

---

### Task 5: Verify cross-Session/provider behavior and real Obsidian acceptance

**Files:**
- Create: `testdata/problem-map/mixed-provider-project/`
- Create: `test/integration/problem_map_test.go`
- Create: `docs/session-review/problem-map-acceptance.md`
- Modify: `test/zerotoken/gate_a_test.go`

**Interfaces:** Uses the frozen contracts and public commands from Tasks 1–4; creates no new production interface.

- [ ] **Step 1: Build a mixed-provider fixture.** Include Codex, Claude Code and OpenCode Sessions sharing native ID `same`; one explicit cross-Session continuation; one similarity-only question; one missing answer; one verified execution; one resolved problem; one stale candidate.

- [ ] **Step 2: Write the end-to-end test.** Scan twice and assert one stable formal graph, namespaced source refs, explicit continuation linked, similarity-only item pending, zero Agent starts, identical second-run bytes and unchanged human edits.

```go
func TestMixedProviderProblemMapIsStableAndZeroToken(t *testing.T) {
    first := runFixture(t, "mixed-provider-project")
    second := runFixture(t, "mixed-provider-project")
    if first.AgentStarts != 0 || second.AgentStarts != 0 { t.Fatal("ordinary scan started an agent") }
    if !bytes.Equal(first.Review, second.Review) { t.Fatal("problem presentation drifted") }
}
```

- [ ] **Step 3: Run repository gates.**

Run: `go test -p 1 -timeout 5m -count=1 ./... && go vet ./... && go mod tidy -diff && (cd obsidian-plugin && npm run check) && git diff --check`.

- [ ] **Step 4: Install the current bundle into a disposable real Vault.** Verify the left hierarchy never flattens, top tabs do not overlap it, the focus path and right chain match the selected question, ambiguous placement remains pending, every source opens the correct provider Session, and restart/reopen keeps the same structure.

- [ ] **Step 5: Exercise all structural actions with undo evidence.** Apply child, sibling and merge; keep one candidate pending; move a subtree; reorder siblings; provoke one stale CAS; sync Project/Vault both directions; confirm source refs and human edits survive.

- [ ] **Step 6: Record evidence and commit when authorized.** Include build hash, Obsidian version, fixture totals, before/after screenshots, zero-Agent counter, command outputs and any unverified platform boundary.

```bash
git add testdata/problem-map test/integration test/zerotoken docs/session-review/problem-map-acceptance.md
git commit -m "test: accept project problem map workflow"
```
