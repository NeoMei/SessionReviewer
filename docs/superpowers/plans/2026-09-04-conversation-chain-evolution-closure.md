# Conversation Chain and Evolution Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a zero-token, provider-neutral Q/A execution chain and use it to replace placeholder Project Evolution details with traceable milestone closure summaries.

**Architecture:** Materialize a private `conversation-chain-v1` beside each accepted SessionView using visible user/assistant messages and bounded tool evidence only. Project projection promotes only qualified milestones and stores human-editable closure fields plus source turn references in `review-presentation-v4`; Obsidian reads concise closures by default and queries the private chain only when the user expands evidence.

**Tech Stack:** Go 1.26, existing Observation/SessionView/ProjectView and publication layers, canonical JSON, TypeScript 5.8, Obsidian 1.13, Vitest/jsdom.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Prerequisites: the reopened Gate 0 Task 7 is complete for `conversation-chain-v1`, expanded `review-presentation-v4`, CLI grammar, Go/TypeScript parity and native Windows CI; the Codex, Claude Code and OpenCode SourceAdapters from `2026-08-30-multi-agent-session-review.md` are present and passing.
- Ordinary scans and deterministic projection start zero Agent processes. No model summarizes a reply unless the user explicitly requests an AI candidate.
- Only visible `user` and `assistant` content participates. System/developer instructions, hidden reasoning, encrypted compaction and opaque content are excluded.
- A turn unit starts at one visible user message and ends immediately before the next visible user message in the same provider Session.
- The private chain retains bounded visible Q/A excerpts and authenticated source refs; Vault Markdown contains only bounded closure text and source references. 回答正文 is read on demand through `SourceAdapter.Read`, never persisted as a transcript or raw tool output.
- `execution_verified` is a machine evidence state. It never implies the human workflow state `resolved`.
- Cross-Session chains require explicit stable evidence. Semantic similarity alone produces a candidate in the problem-map plan and never mutates a milestone.
- Existing human edits win over generated fields. Migration is explicit, digest-bound, previewable and idempotent.

## File Structure and Ownership

- `internal/conversationchain/materialize.go`: deterministic turn segmentation and action/result attachment.
- `internal/conversationchain/store.go`: private generation-bound chain persistence and retention.
- `internal/inspect/conversation.go`: bounded read-only chain query and cursor binding.
- `internal/presentation/milestone.go`: qualification rules and generated closure baselines.
- `internal/migrationv4/`: classification preview for old automatic `user_request` events.
- `obsidian-plugin/src/cli/runner.ts`: fixed-argv conversation-chain query.
- `obsidian-plugin/src/view/render-evolution.ts`: milestone rail and closure detail.
- `obsidian-plugin/src/view/render-closure.ts`: closure sections, coverage, source links and expandable answer.

---

### Task 1: Materialize deterministic conversation turn units

**Files:**
- Create: `internal/conversationchain/materialize.go`, `materialize_test.go`
- Modify: `internal/source/codex/decode.go`, `decode_test.go`
- Modify: `internal/source/claude/adapter.go` and its tests
- Modify: `internal/source/opencode/export.go`, `adapter.go` and their tests
- Modify: `internal/memory/types.go`, `types_test.go`
- Modify: `internal/sessionview/materialize.go`, `materialize_test.go`
- Modify: `internal/scan/service.go`, `service_test.go`

**Interfaces:**

```go
type MaterializeInput struct {
    ProjectID string
    Provider string
    SessionID string
    SessionViewDigest string
    Observations []memory.ObservationRevision
    RuleVersion string
}
func Materialize(MaterializeInput) (conversationchain.Document, error)
```

- [ ] **Step 1: Write failing boundary tests.** Cover one user/one assistant, multiple assistant messages, tools between answer fragments, a second user message, a user message without an answer, an interrupted final assistant message, a stable-prefix-only Session, malformed timestamps and duplicate revision IDs. Assert the deterministic conclusion excerpt is the last non-empty complete visible assistant message and incomplete tails remain visibly partial.

```go
func TestMaterializeEndsTurnBeforeNextVisibleUserMessage(t *testing.T) {
    got := materialize(t, user("u1", "first"), assistant("a1", "answer"), toolResult("r1", "PASS"), user("u2", "second"))
    if len(got.TurnUnits) != 2 { t.Fatalf("turns=%d", len(got.TurnUnits)) }
    if got.TurnUnits[0].UserMessage.RevisionID != "u1" || len(got.TurnUnits[0].AssistantMessages) != 1 { t.Fatalf("bad first turn: %+v", got.TurnUnits[0]) }
    if got.TurnUnits[1].AnswerState != conversationchain.AnswerNone { t.Fatalf("invented answer: %+v", got.TurnUnits[1]) }
}
```

- [ ] **Step 2: Write failing decoder privacy and size tests.** Emit bounded `visible_user_message` and `visible_assistant_message` observations with authenticated source refs and at most 4,096 UTF-8 bytes of excerpt. Feed system/developer/analysis records and an oversized visible message; assert forbidden roles never emit an observation and truncation metadata is exact. Keep full text out of SessionView summaries.

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/conversationchain ./internal/sessionview -run 'Materialize|Turn|Visible|Chunk' -count=1`

Expected: FAIL because `Materialize` and the visibility adapter do not exist.

- [ ] **Step 4: Extend all three source decoders and implement a single-pass state machine.** Codex response messages, Claude assistant content blocks and finalized OpenCode assistant text emit the same bounded `visible_assistant_message` observation; each provider's visible user text emits `visible_user_message`. Open a unit only on a user observation, append assistant observations and bounded action/result references until the next user observation, close the final unit at end-of-source, and derive stable IDs from provider/session/user revision/rule version. The scan service materializes the chain from the same authenticated observation set as SessionView, then stores only the chain digest in the prepared generation manifest.

```go
func turnUnitID(provider, sessionID, userRevision, ruleVersion string) string {
    return "turn-" + digestID(provider+"\x00"+sessionID+"\x00"+userRevision+"\x00"+ruleVersion)
}
```

- [ ] **Step 5: Run GREEN and the zero-token boundary.**

Run: `gofmt -w internal/conversationchain internal/source internal/memory internal/sessionview internal/scan && go test ./internal/conversationchain ./internal/source/... ./internal/memory ./internal/sessionview ./internal/scan ./test/zerotoken -count=1`

- [ ] **Step 6: Commit when authorized.**

```bash
git add internal/conversationchain internal/source internal/memory internal/sessionview internal/scan test/zerotoken
git commit -m "feat: materialize deterministic conversation chains"
```

---

### Task 2: Persist and query private chains without copying transcripts to Vault

**Files:**
- Create: `internal/conversationchain/store.go`, `store_test.go`
- Create: `internal/inspect/conversation.go`, `conversation_test.go`
- Modify: `internal/source/adapter.go`, `internal/source/codex/adapter.go`
- Modify: `internal/memorystore/store.go`, `store_test.go`, `retention.go`, `retention_test.go`
- Modify: `internal/cli/run.go`, `run_test.go`

**Interfaces:**

```go
type ChainStore interface {
    Put(context.Context, conversationchain.Document) error
    Get(context.Context, string, string, string) (conversationchain.Document, error)
}
func (s *Service) ConversationChain(ctx context.Context, request ConversationChainRequest) (conversationchain.Page, error)
```

- [ ] **Step 1: Write failing store tests.** Assert atomic replacement, exact generation/session binding, dependency digest mismatch rejection, source-unavailable reads from the last accepted chain, and retention of the revision referenced by the active presentation.

- [ ] **Step 2: Write failing paging and authenticated-read tests.** Query the unit index, a selected turn, the first/middle/last visible message, wrong provider/session/generation, stale message cursor, oversized limit, changed source hash, a source record over 64 KiB and a missing chain.

```go
func TestConversationCursorCannotCrossTurnUnits(t *testing.T) {
    cursor := pageCursor(t, request("turn-a"))
    _, err := service.ConversationChain(context.Background(), withCursor(request("turn-b"), cursor))
    assertCode(t, err, cli.ContractCodeStaleCursor)
}
```

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/conversationchain ./internal/inspect ./internal/memorystore ./internal/cli -run 'Conversation|Chain|Retention' -count=1`

- [ ] **Step 4: Implement generation-bound storage and HMAC cursor binding.** Store bounded excerpts and source refs under the existing private project/session namespace. Cursors bind project, provider, session, generation, turn unit, sanitization version, message ordinal and limit. For an expanded turn, call `SourceAdapter.Read` with the stored ref and a 64 KiB maximum, verify the source hash, decode only the referenced visible user/assistant record, redact again and never persist the response body.

- [ ] **Step 5: Wire the fixed CLI command.** Route `inspect conversation-chain` to the service, enforce the Gate-0 response byte cap and timeout, and return stable `generation_mismatch`, `stale_cursor`, `response_too_large` and `source_unavailable` codes.

- [ ] **Step 6: Run full Go gates and commit when authorized.**

Run: `gofmt -w internal/conversationchain internal/inspect internal/memorystore internal/cli && go test ./... && go vet ./... && go mod tidy -diff`

```bash
git add internal/conversationchain internal/inspect internal/source internal/memorystore internal/cli
git commit -m "feat: query private conversation chains"
```

---

### Task 3: Project only qualified milestones and build closure baselines

**Files:**
- Create: `internal/presentation/milestone.go`, `milestone_test.go`
- Modify: `internal/presentation/project.go`, `project_test.go`, `render.go`, `render_test.go`
- Modify: `internal/reviewv4/types.go`, `validate.go`

**Interfaces:**

```go
func QualifyMilestones(project memory.ProjectView, chains []conversationchain.Document, accepted reviewv4.Presentation) []reviewv4.Timeline
func GeneratedClosure(event MilestoneEvidence) reviewv4.ClosedLoop
```

- [ ] **Step 1: Write failing qualification tests.** A plain user request is not a milestone. Explicit confirmation, completed implementation, supported verification, release, rollback, major failure and direction adjustment qualify. Repeated tool noise and assistant prose without evidence do not.

- [ ] **Step 2: Write failing closure tests.** Assert exact section order, bounded visible answer excerpt, source turn refs for every segment, `missing` rather than filler text, and human patch precedence over the generated baseline.

```go
func TestGeneratedClosureDoesNotInventMissingVerification(t *testing.T) {
    got := GeneratedClosure(answerOnlyEvidence())
    if got.Verification.State != "missing" || got.Verification.Text != "" { t.Fatalf("invented verification: %+v", got.Verification) }
    if got.Conclusion.Kind != "visible_answer_excerpt" { t.Fatalf("wrong conclusion kind: %s", got.Conclusion.Kind) }
}
```

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/presentation ./internal/reviewv4 -run 'Milestone|Closure|UserRequest' -count=1`

- [ ] **Step 4: Replace the 20-request projector.** Remove automatic `user_request` promotion in `projectHistoryEvents`; select only typed evidence or accepted human events. Generate closure fields without changing machine timestamps, command exit codes or source identities.

- [ ] **Step 5: Render the closure into the existing two Markdown documents.** Keep full chain text private; write concise fields and stable source references inside the existing event block so no third visible document is created.

- [ ] **Step 6: Run focused and full gates, then commit when authorized.**

Run: `gofmt -w internal/presentation internal/reviewv4 && go test ./internal/presentation ./internal/reviewv4 -count=1 && go test ./... && go vet ./... && go mod tidy -diff`

```bash
git add internal/presentation internal/reviewv4
git commit -m "feat: project milestone closure summaries"
```

---

### Task 4: Add optional dependency-cached Agent conclusion candidates

**Files:**
- Modify: `internal/annotation/types.go`
- Create: `internal/annotation/store.go`, `store_test.go`, `paths.go`, `paths_test.go`
- Create: `internal/cli/evolution.go`, `evolution_test.go`
- Modify: `internal/agent/agent.go`, `agent_test.go`
- Modify: `internal/reviewjob/agent_handle.go`, `service_test.go`
- Modify: `obsidian-plugin/src/cli/runner.ts`, `obsidian-plugin/tests/cli.test.ts`

**Interfaces:**

```go
func (s *Service) RequestConclusionCandidate(context.Context, ConclusionCandidateRequest) (annotation.Annotation, error)
func (s *Service) TransitionConclusionCandidate(context.Context, ConclusionTransitionRequest) (reviewv4.Presentation, error)
```

- [ ] **Step 1: Write failing generic annotation-store tests.** Cover private path confinement, atomic replace, revision CAS, `annotation_kind`, terminal state mutation rejection and `confirmed_entity_id` validation.

- [ ] **Step 2: Write failing dependency-cache tests.** Repeating `evolution summarize` with the same milestone, sorted source turn digests, extractor version and prompt schema returns the same run with one Agent start. Changed dependencies create one new run; invalid or failed output does not advance the successful dependency set.

```go
func TestConclusionSummaryReusesIdenticalDependencies(t *testing.T) {
    first := summarize(t, service, request("milestone-1", "d2", "d1"))
    second := summarize(t, service, request("milestone-1", "d1", "d2"))
    if first.ID != second.ID || agent.Starts() != 1 { t.Fatalf("summary cache miss: %d", agent.Starts()) }
}
```

- [ ] **Step 3: Run RED.**

Run: `go test ./internal/annotation ./internal/cli ./internal/agent ./internal/reviewjob -run 'Conclusion|Annotation|Dependency' -count=1 && (cd obsidian-plugin && npm test -- cli.test.ts)`

- [ ] **Step 4: Implement the explicit request path through the verified provider-neutral Agent handle.** Send only the selected milestone's bounded visible Q/A excerpts and evidence refs to the invoking Agent or configured Obsidian proposal worker. Require strict `milestone_conclusion_candidate` JSON and reject unknown milestones, invented source refs and prose outside the schema. OpenCode remains proposal-only through its documented supported path; no UI label may imply unsupported execution.

- [ ] **Step 5: Implement confirmation as a narrow HumanPresentation patch.** Confirmation changes only `closed_loop.conclusion.text` and `conclusion_kind=ai_candidate_confirmed`; ignore/restore affect only the private candidate. Review SHA and candidate revision mismatch write nothing.

- [ ] **Step 6: Run full gates and commit when authorized.**

```bash
git add internal/annotation internal/cli internal/agent internal/reviewjob obsidian-plugin/src/cli obsidian-plugin/tests/cli.test.ts
git commit -m "feat: add optional milestone conclusion candidates"
```

---

### Task 5: Migrate old placeholder events explicitly and idempotently

**Files:**
- Modify: `internal/migrationv4/types.go`, `plan.go`, `migrate.go`
- Modify: `internal/migrationv4/migrate_test.go`
- Modify: `internal/problemmap/candidate_codec.go`, `validate.go`
- Create: `testdata/contracts/migration/v3-placeholder-events/`
- Modify: `internal/cli/sync_test.go`

**Interfaces:**

```go
type EventClassification struct {
    EventID string `json:"event_id"`
    Action string `json:"action"` // upgrade_milestone|move_problem|preserve_human|unclassified
    TargetID string `json:"target_id,omitempty"`
    ReasonCodes []string `json:"reason_codes"`
}
```

- [ ] **Step 1: Write failing migration tests.** Include an untouched generated placeholder, a human-patched event, a request followed by verified implementation, an unanswered request and a missing source. Assert all four action counts and stable IDs in dry-run.

- [ ] **Step 2: Run RED.**

Run: `go test ./internal/migrationv4 ./internal/cli -run 'Placeholder|EventClassification|MigrationPreview' -count=1`

- [ ] **Step 3: Implement classification from authenticated dependencies.** Treat exact v3 generated baseline plus no active human patch as automatic. Preserve patched/unknown blocks. Convert plain questions without a confirmed parent into deterministic `problem-map-candidate-v1` records with `recommended_relation=keep_pending`; only an explicit legacy hierarchy may create a formal node. Never delete source refs or treat “已纳入项目脉络索引” as verification.

- [ ] **Step 4: Bind classification to the migration preview digest.** Any chain dependency, human patch, source preimage or classification result change returns `migration_preview_stale` at confirmation.

- [ ] **Step 5: Prove idempotence and byte stability.** Run preview twice, migrate once, then render/sync/reload twice. Require identical v4 bytes and no new revision on the second pass.

- [ ] **Step 6: Run full gates and commit when authorized.**

```bash
git add internal/migrationv4 internal/problemmap internal/cli testdata/contracts/migration
git commit -m "feat: migrate placeholder evolution nodes"
```

---

### Task 6: Render and verify the Obsidian closure detail

**Files:**
- Create: `obsidian-plugin/src/view/render-closure.ts`
- Modify: `obsidian-plugin/src/view/render-evolution.ts`, `render-shell.ts`, `styles.css`
- Modify: `obsidian-plugin/src/cli/runner.ts`
- Create: `obsidian-plugin/tests/evolution-closure.test.ts`
- Modify: `obsidian-plugin/tests/accessibility.test.ts`, `large-history.test.ts`, `cli.test.ts`

**Interfaces:**

```ts
export function renderClosure(event: TimelineV4, model: BrowserModelV4, state: ViewState, actions: ClosureActions): HTMLElement;
export interface ClosureActions {
  loadTurn(key: SessionIdentity, turnUnitId: string, messageCursor?: string): Promise<ConversationChainV1>;
  openSource(key: SessionIdentity, turnUnitId: string): Promise<void>;
  openProblem(problemId: string): void;
}
```

- [ ] **Step 1: Write failing DOM tests.** Assert the five fixed sections, real source badges, missing/partial coverage copy, compact default answer, expand/collapse, stale-generation recovery and absence of the old generic labels.

```ts
it("shows a missing answer honestly", () => {
  const detail = renderClosure(milestone({ conclusionKind: "missing" }), model(), state(), actions());
  expect(detail.textContent).toContain("未捕获 Agent 回答");
  expect(detail.textContent).not.toContain("已纳入项目脉络索引");
});
```

- [ ] **Step 2: Write failing keyboard and CLI tests.** Tab reaches “查看回答正文”, “打开原 Session” and “查看关联问题”; Enter/Space activate them; fixed argv contains provider/session/turn separately and `shell:false`.

- [ ] **Step 3: Run RED.**

Run: `cd obsidian-plugin && npx vitest run tests/evolution-closure.test.ts tests/accessibility.test.ts tests/cli.test.ts`

- [ ] **Step 4: Implement the closure component.** Use semantic headings and lists, preserve Obsidian theme variables, keep on-demand answer正文 collapsed, label 64 KiB truncation and source-unavailable states, announce loaded/error states through one polite live region, and never inject source text through `innerHTML`.

- [ ] **Step 5: Run plugin and repository gates.**

Run: `cd obsidian-plugin && npm run check`; then from repository root run `go test ./... && go vet ./... && go mod tidy -diff && git diff --check`.

- [ ] **Step 6: Install the built bundle into a disposable real Vault.** Open the first AgentWiki milestone and verify: answer content is visible; each segment names its true Session; missing evidence is explicit; answer正文 reading is bounded and non-persistent; keyboard navigation works; restart and reopen preserve selection without changing files.

- [ ] **Step 7: Record evidence and commit when authorized.** Save bundle hash, Obsidian version, fixture/Vault path, screenshots and observed coverage in `docs/session-review/evolution-closure-acceptance.md`.

```bash
git add obsidian-plugin docs/session-review/evolution-closure-acceptance.md
git commit -m "feat: show closed-loop evolution details"
```
