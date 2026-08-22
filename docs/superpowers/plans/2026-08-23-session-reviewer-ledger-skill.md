# SessionReviewer Ledger and Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert bounded evidence plus Codex-generated, schema-valid proposals into an incremental editable Markdown ledger, deterministic resume/history views, and packaged review/checkpoint/resume Skill workflows.

**Architecture:** The Skill performs semantic classification over one bounded packet and emits JSON; the Go CLI validates it, renders all target bytes, writes stable Markdown, records a receipt, and advances the accepted cursor with compare-and-swap. Markdown entities are durable truth, Mermaid and ledger-only views are derived, and unknown user content survives accepted updates.

**Tech Stack:** Go 1.26, Go standard library, `gopkg.in/yaml.v3 v3.0.1`, Markdown/YAML frontmatter, Mermaid, POSIX shell, PowerShell, JSON Schema draft 2020-12.

## Global Constraints

- Target macOS 13+ on Intel/Apple Silicon and Windows 10 22H2+/Windows 11 x86-64 without administrator privileges.
- The Go CLI has no model client, makes no OpenAI API call, and never invents semantic entities.
- Neither CLI nor Skill automatically mutates Git state, including add, commit, push, reset, checkout, switch, restore, branch, tag, stash, merge, or rebase.
- Raw JSONL remains local/read-only; the Skill reads bounded redacted packets, never whole raw logs.
- Hidden reasoning, system/developer instructions, and encrypted/opaque compaction remain excluded.
- Every material proposal change cites evidence from the exact packet; inference cannot become verified without verification evidence.
- Validate, render all bytes, persist a prepared transaction receipt, write ledger, mark the receipt applied, then CAS the cursor; an earlier failure never advances it.
- Reapplying the same packet/proposal is idempotent and creates no meaningful Markdown or Mermaid diff.
- Preserve unknown frontmatter keys, unknown sections, and user titles/statuses/tags/narrative.
- Reserved fields are `id`, `entity_type`, `project_id`, `revision`, `source_sessions`, and synchronization hashes.
- Mermaid is derived and never overrides entity facts.
- `resume --ledger-only` and `history --ledger-only` read accepted Markdown only.
- Do not implement sync, conflicts, SQLite, watcher, startup registration, notification, or release packaging here.
- Foundation hardening must pass first.

## File Structure

```text
schemas/evidence-v2.schema.json                 cursor-bound packet schema
schemas/proposal-v1.schema.json                 Skill proposal schema
schemas/entity-v1.schema.json                   entity frontmatter schema
internal/evidence/{types,extract}.go             cursor boundaries/digest
internal/prepare/prepare.go                      populate packet boundaries
internal/ledger/{types,document,load,render}.go  stable domain and Markdown
internal/proposal/{types,validate}.go             strict proposal validation
internal/apply/{apply,receipt,lock}.go            lock, journaled writes, cursor CAS
internal/diagram/render.go                       derived Mermaid
internal/recovery/{resume,history}.go             ledger-only views
internal/cli/{apply,recovery}.go                 deterministic CLI stages
skill/session-reviewer/SKILL.md                  semantic workflow
skill/session-reviewer/references/proposal-v1.schema.json
skill/session-reviewer/scripts/*.{sh,ps1}        argument-safe wrappers
testdata/proposals/*.json                        fixed semantic samples
README.md                                        workflows/boundaries
```

---

### Task 1: Bind Packets to Exact Cursor States

**Files:**
- Create: `schemas/evidence-v2.schema.json`
- Modify: `internal/evidence/types.go`
- Modify: `internal/evidence/extract.go`
- Modify: `internal/evidence/extract_test.go`
- Modify: `internal/prepare/prepare.go`
- Modify: `internal/prepare/prepare_test.go`

**Interfaces:**
- Consumes: accepted `cursor.Cursor` and every consumed `session.Record`.
- Produces: `evidence.CursorBoundary`; `Packet.ExpectedCursor`, `Packet.NextCursor`; `evidence.Digest(Packet) (string,error)`; schema version 2.

- [ ] **Step 1: Write failing boundary tests**

```go
func TestPacketTracksExcludedRecordBoundary(t *testing.T) {
	x, _ := NewWithProjectID("project-1111111111111111", "s1", "/p", 3, redact.Default(), DefaultLimits())
	if err := x.SetExpectedCursor(CursorBoundary{Line: 2, SourceHash: strings.Repeat("a", 64)}); err != nil { t.Fatal(err) }
	r := session.Record{Line: 3, Type: "response_item", SourceHash: strings.Repeat("b", 64), Payload: json.RawMessage(`{"type":"reasoning"}`)}
	if err := x.Add(r); err != nil { t.Fatal(err) }
	if got := x.Packet().NextCursor; got.Line != 3 || got.SourceHash != r.SourceHash { t.Fatalf("boundary=%+v", got) }
}
func TestDigestChangesWithCursorHash(t *testing.T) {
	p := Packet{SchemaVersion: 2, ProjectID: "p1", SessionID: "s1", Events: []Item{}, NextCursor: CursorBoundary{Line: 1, SourceHash: strings.Repeat("a", 64)}}
	a, _ := Digest(p); p.NextCursor.SourceHash = strings.Repeat("b", 64); b, _ := Digest(p)
	if a == b { t.Fatal("digest ignored cursor hash") }
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/evidence -run 'Test(PacketTracksExcludedRecordBoundary|DigestChangesWithCursorHash)$' -count=1`

Expected: FAIL because schema-v1 lacks boundaries.

- [ ] **Step 3: Add boundary types and digest**

```go
type CursorBoundary struct { Line int `json:"line"`; SourceHash string `json:"source_hash,omitempty"` }
type Packet struct {
	SchemaVersion int `json:"schema_version"`; ProjectID string `json:"project_id"`; SessionID string `json:"session_id"`; CWD string `json:"cwd"`
	FromCursor int `json:"from_cursor"`; ToCursor int `json:"to_cursor"`; ExpectedCursor CursorBoundary `json:"expected_cursor"`; NextCursor CursorBoundary `json:"next_cursor"`
	HasMore bool `json:"has_more"`; Events []Item `json:"events"`; Warnings []string `json:"warnings,omitempty"`
}
func Digest(p Packet) (string, error) { b, err := json.Marshal(p); if err != nil { return "", err }; sum := sha256.Sum256(b); return "sha256:"+hex.EncodeToString(sum[:]), nil }
```

Set schema version 2. Add `SetExpectedCursor(CursorBoundary) error`. Change extractor advancement to accept a full record so excluded records update both line and hash; a full packet stops at the last fully consumed record. In `prepare.Run`, set the expected boundary from the read-only stored cursor before streaming. The schema requires all identity/cursor/event fields, forbids extra packet fields, and requires a lowercase 64-hex hash when line > 0.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/evidence internal/prepare && go test ./internal/evidence ./internal/prepare -count=1`

Expected: PASS; prepare remains cursor-side-effect free and byte-stable.

- [ ] **Step 5: Commit**

```bash
git add schemas/evidence-v2.schema.json internal/evidence internal/prepare
git commit -m "feat: bind evidence packets to cursors"
```

### Task 2: Define Stable Editable Markdown Entities

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `schemas/entity-v1.schema.json`
- Create: `internal/ledger/types.go`
- Create: `internal/ledger/document.go`
- Create: `internal/ledger/document_test.go`

**Interfaces:**
- Consumes: UTF-8 Markdown with YAML frontmatter and level-2 sections.
- Produces: exact types below; `ParseDocument([]byte) (Document,error)`; `Document.SetReserved`; `Document.SetEditable`; `Document.UpsertSection`; `Document.Render`.

- [ ] **Step 1: Write failing preservation tests**

```go
func TestDocumentPreservesUnknownContentAndIsStable(t *testing.T) {
	src := []byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1111111111111111\nrevision: 2\ncustom_rating: gold\n---\n\n# Title\n\n## Rationale\n\nOld.\n\n## My notes\n\nKeep exactly.\n")
	doc, err := ParseDocument(src); if err != nil { t.Fatal(err) }; doc.UpsertSection("Rationale", "New.\n")
	a, _ := doc.Render(); parsed, _ := ParseDocument(a); b, _ := parsed.Render()
	for _, s := range []string{"custom_rating: gold", "## My notes", "Keep exactly.", "New."} { if !bytes.Contains(a, []byte(s)) { t.Fatalf("render=%s", a) } }
	if !bytes.Equal(a, b) { t.Fatal("render is not stable") }
}
func TestDocumentRejectsReservedIdentityChange(t *testing.T) {
	doc, _ := ParseDocument([]byte("---\nid: decision-1\nentity_type: decision\nproject_id: project-1111111111111111\nrevision: 2\n---\n\n# T\n"))
	if err := doc.SetReserved(map[string]any{"id":"decision-2","entity_type":"decision","project_id":"project-1111111111111111","revision":3}); !errors.Is(err, ErrReservedFieldChanged) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/ledger -run '^TestDocument' -count=1`

Expected: FAIL because the package is absent.

- [ ] **Step 3: Add exact domain types and YAML-node document model**

```go
type FactClass string
const ( Verified FactClass="verified"; DecisionFact FactClass="decision"; Inference FactClass="inference"; Superseded FactClass="superseded"; PendingConfirmation FactClass="pending_confirmation" )
type EvidenceRef struct { EvidenceID string `json:"evidence_id" yaml:"evidence_id"`; SessionID string `json:"session_id" yaml:"session_id"`; JSONLLine int `json:"jsonl_line" yaml:"jsonl_line"`; SourceHash string `json:"source_hash" yaml:"source_hash"`; Summary string `json:"summary" yaml:"summary"` }
type Decision struct { ID string `json:"id"`; ProjectID string `json:"project_id"`; Title string `json:"title"`; Status string `json:"status"`; Revision int `json:"revision"`; Tags []string `json:"tags"`; Supersedes []string `json:"supersedes"`; SourceSessions []string `json:"source_sessions"`; Evidence []EvidenceRef `json:"evidence"`; Context string `json:"context"`; Rationale string `json:"rationale"`; Consequences string `json:"consequences"`; ReevaluateWhen string `json:"reevaluate_when"`; Alternatives []string `json:"alternatives"`; RejectedPaths []string `json:"rejected_paths"` }
type OpenLoop struct { ID string `json:"id"`; ProjectID string `json:"project_id"`; Title string `json:"title"`; Status string `json:"status"`; Revision int `json:"revision"`; Tags []string `json:"tags"`; SourceSessions []string `json:"source_sessions"`; Evidence []EvidenceRef `json:"evidence"`; Question string `json:"question"`; Attempts []string `json:"attempts"`; Blocker string `json:"blocker"`; NextExperiment string `json:"next_experiment"`; CompletionCriterion string `json:"completion_criterion"` }
type TimelineEvent struct { ID string `json:"id"`; OccurredAt string `json:"occurred_at"`; Revision int `json:"revision"`; Class FactClass `json:"class"`; Title string `json:"title"`; Summary string `json:"summary"`; Evidence []EvidenceRef `json:"evidence"`; DecisionIDs []string `json:"decision_ids"`; OpenLoopIDs []string `json:"open_loop_ids"` }
type CurrentState struct { ProjectID string `json:"project_id"`; Revision int `json:"revision"`; Goal string `json:"goal"`; LastVerified string `json:"last_verified"`; Branch string `json:"branch"`; UncommittedChanges []string `json:"uncommitted_changes"`; Blockers []string `json:"blockers"`; OpenRisks []string `json:"open_risks"`; NextAction string `json:"next_action"`; FirstInspection string `json:"first_inspection"`; LastUpdated string `json:"last_updated"`; SourceSessions []string `json:"source_sessions"`; Evidence []EvidenceRef `json:"evidence"` }
type SessionPhase struct { Title string `json:"title"`; Summary string `json:"summary"`; Evidence []EvidenceRef `json:"evidence"` }
type SessionReport struct { ID string `json:"id"`; ProjectID string `json:"project_id"`; SessionID string `json:"session_id"`; Revision int `json:"revision"`; InitialGoal string `json:"initial_goal"`; GoalChanges []string `json:"goal_changes"`; Phases []SessionPhase `json:"phases"`; Files []string `json:"files"`; Commits []string `json:"commits"`; Verification []string `json:"verification"`; DecisionsAdded []string `json:"decisions_added"`; DecisionsRevised []string `json:"decisions_revised"`; OpenLoopsCreated []string `json:"open_loops_created"`; OpenLoopsClosed []string `json:"open_loops_closed"`; PreviousSessionID string `json:"previous_session_id"`; NextSessionID string `json:"next_session_id"`; Evidence []EvidenceRef `json:"evidence"` }
type State struct { ProjectID string; CurrentState CurrentState; Timeline []TimelineEvent; Decisions map[string]Decision; OpenLoops map[string]OpenLoop; Sessions map[string]SessionReport }
type ChangeSet struct { Current *CurrentState; Timeline []TimelineEvent; Decisions []Decision; OpenLoops []OpenLoop; Sessions []SessionReport }
type PlannedFile struct { RelativePath string; Data []byte; Perm fs.FileMode }; type WritePlan struct { ProjectRoot string; Files []PlannedFile }
```

`Document` stores frontmatter as a `yaml.Node` mapping and ordered `Section{Name,Heading,Body string}`. `SetEditable` updates only title/status/tags. `SetReserved` compares ID/type/project and requires update revision = old+1. Rendering uses LF and one final newline. Unknown nodes/sections stay ordered. Entity schema requires common reserved keys, conditionally validates decision/open-loop/session types, and intentionally permits unknown user properties.

- [ ] **Step 4: Run GREEN**

Run: `go mod tidy && gofmt -w internal/ledger && go test ./internal/ledger -count=1 && git diff --check`

Expected: PASS; parse-render is byte-stable and unknown content survives.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum schemas/entity-v1.schema.json internal/ledger
git commit -m "feat: add stable Markdown ledger model"
```

### Task 3: Define and Validate Proposal JSON

**Files:**
- Create: `schemas/proposal-v1.schema.json`
- Create: `internal/proposal/types.go`
- Create: `internal/proposal/validate.go`
- Create: `internal/proposal/validate_test.go`
- Create: `testdata/proposals/valid-first.json`
- Create: `testdata/proposals/invalid-evidence.json`

**Interfaces:**
- Consumes: strict proposal JSON, exact packet, existing state.
- Produces: `proposal.Decode(io.Reader) (Proposal,error)`; `proposal.Validate(Proposal,evidence.Packet,ledger.State) (ledger.ChangeSet,error)`.

- [ ] **Step 1: Write failing fixed-sample tests**

```go
func TestValidateExactPacket(t *testing.T) { p, packet, state := fixedProposalPacketState(t, "valid-first.json"); changes, err := Validate(p, packet, state); if err != nil || len(changes.Decisions)!=1 { t.Fatalf("changes=%+v err=%v", changes, err) } }
func TestValidateRejectsUnknownEvidence(t *testing.T) { p, packet, state := fixedProposalPacketState(t, "invalid-evidence.json"); if _, err := Validate(p, packet, state); err == nil { t.Fatal("accepted unknown evidence") } }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/proposal -run '^TestValidate' -count=1`

Expected: FAIL because protocol code is absent.

- [ ] **Step 3: Add exact protocol types and checks**

```go
type Proposal struct { SchemaVersion int `json:"schema_version"`; ProjectID string `json:"project_id"`; SessionID string `json:"session_id"`; FromCursor int `json:"from_cursor"`; ToCursor int `json:"to_cursor"`; EvidencePacketSHA256 string `json:"evidence_packet_sha256"`; NewDecisions []ledger.Decision `json:"new_decisions"`; UpdatedDecisions []DecisionPatch `json:"updated_decisions"`; OpenLoops []OpenLoopChange `json:"open_loops"`; TimelineEvents []ledger.TimelineEvent `json:"timeline_events"`; CurrentStatePatch CurrentStatePatch `json:"current_state_patch"`; SessionReport ledger.SessionReport `json:"session_report"`; EvidenceLinks []EvidenceLink `json:"evidence_links"` }
type DecisionPatch struct { ID string `json:"id"`; ExpectedRevision int `json:"expected_revision"`; Title *string `json:"title,omitempty"`; Status *string `json:"status,omitempty"`; Tags *[]string `json:"tags,omitempty"`; Supersedes *[]string `json:"supersedes,omitempty"`; SourceSessions *[]string `json:"source_sessions,omitempty"`; Evidence *[]ledger.EvidenceRef `json:"evidence,omitempty"`; Context *string `json:"context,omitempty"`; Rationale *string `json:"rationale,omitempty"`; Consequences *string `json:"consequences,omitempty"`; ReevaluateWhen *string `json:"reevaluate_when,omitempty"`; Alternatives *[]string `json:"alternatives,omitempty"`; RejectedPaths *[]string `json:"rejected_paths,omitempty"` }
type OpenLoopChange struct { Operation string `json:"operation"`; Entity *ledger.OpenLoop `json:"entity,omitempty"`; Patch *OpenLoopPatch `json:"patch,omitempty"` }
type OpenLoopPatch struct { ID string `json:"id"`; ExpectedRevision int `json:"expected_revision"`; Title *string `json:"title,omitempty"`; Status *string `json:"status,omitempty"`; Tags *[]string `json:"tags,omitempty"`; SourceSessions *[]string `json:"source_sessions,omitempty"`; Evidence *[]ledger.EvidenceRef `json:"evidence,omitempty"`; Question *string `json:"question,omitempty"`; Blocker *string `json:"blocker,omitempty"`; NextExperiment *string `json:"next_experiment,omitempty"`; CompletionCriterion *string `json:"completion_criterion,omitempty"`; Attempts *[]string `json:"attempts,omitempty"` }
type CurrentStatePatch struct { ExpectedRevision int `json:"expected_revision"`; Goal *string `json:"goal,omitempty"`; LastVerified *string `json:"last_verified,omitempty"`; Branch *string `json:"branch,omitempty"`; NextAction *string `json:"next_action,omitempty"`; FirstInspection *string `json:"first_inspection,omitempty"`; LastUpdated *string `json:"last_updated,omitempty"`; UncommittedChanges *[]string `json:"uncommitted_changes,omitempty"`; Blockers *[]string `json:"blockers,omitempty"`; OpenRisks *[]string `json:"open_risks,omitempty"`; SourceSessions *[]string `json:"source_sessions,omitempty"`; Evidence *[]ledger.EvidenceRef `json:"evidence,omitempty"` }
type EvidenceLink struct { EntityID string `json:"entity_id"`; EvidenceID string `json:"evidence_id"`; Relation string `json:"relation"` }
```

`Decode` limits input to 4 MiB, uses `DisallowUnknownFields`, and rejects trailing JSON. Schema requires every top-level field, forbids protocol-owned extra fields, requires exactly one entity/patch by operation, and enumerates statuses/classes. `Validate` checks schema/version/digest and packet identity/cursors; redaction findings; unique project-scoped IDs; every evidence tuple; create/update existence and revision; decision transitions `proposed -> accepted|archived`, `accepted -> superseded|archived`, `superseded -> archived`; loop transitions `open <-> blocked`, `open|blocked -> resolved|abandoned`, terminal -> archived; valid supersedes targets; no inference upgrade without verification evidence. It returns a fully validated ID-sorted change set or none.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/proposal && go test ./internal/proposal ./internal/redact -count=1`

Expected: PASS for fixed valid sample and all invalid evidence/transition/revision/redaction cases.

- [ ] **Step 5: Commit**

```bash
git add schemas/proposal-v1.schema.json internal/proposal testdata/proposals
git commit -m "feat: validate semantic change proposals"
```

### Task 4: Load and Render Every Durable Ledger Document

**Files:**
- Create: `internal/ledger/load.go`
- Create: `internal/ledger/render.go`
- Create: `internal/ledger/render_test.go`

**Interfaces:**
- Consumes: existing Markdown, state, validated change set.
- Produces: `ledger.Load(projectRoot string) (State,error)`; `ledger.Render(State,ChangeSet) (WritePlan,error)`; `ledger.Apply(WritePlan) ([]string,error)`.

- [ ] **Step 1: Write failing layout/idempotence tests**

```go
func TestRenderCompleteLayoutPreservesUserContent(t *testing.T) { root:=ledgerFixture(t); state,_:=Load(root); plan,err:=Render(state,completeChanges(t)); if err!=nil {t.Fatal(err)}; files,err:=Apply(plan); if err!=nil {t.Fatal(err)}; for _,p:=range []string{"docs/session-review/current-state.md","docs/session-review/evolution-timeline.md","docs/session-review/decisions/decision-1.md","docs/session-review/open-loops/loop-1.md","docs/session-review/sessions/session-s1.md"} { if !slices.Contains(files,p){t.Fatalf("missing %s",p)} } }
func TestRenderUnchangedReturnsEmptyPlan(t *testing.T) { root:=ledgerFixture(t); state,_:=Load(root); first,_:=Render(state,completeChanges(t)); Apply(first); next,_:=Load(root); second,_:=Render(next,ChangeSet{}); if len(second.Files)!=0 {t.Fatalf("files=%+v",second.Files)} }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/ledger -run '^TestRender' -count=1`

Expected: FAIL because loaders/renderers are absent.

- [ ] **Step 3: Implement full load/render/apply**

`Load` uses `pathguard`, rejects redirected/non-regular/>4 MiB/duplicate/mismatched/malformed entities, and leaves bad files untouched. `Render` clones state and renders all bytes before returning. Decision sections: Context, Alternatives, Rationale, Rejected paths, Evidence, Consequences, Conditions for reevaluation. Loop sections: Question, Available evidence, Attempted paths, Blocking condition, Recommended next experiment, Completion criterion. Current state leads with goal/verified/repository/blockers/next action. Timeline sorts `(OccurredAt,ID)` and omits routine turns. Session reports cover every typed field.

```go
func Apply(plan WritePlan) ([]string,error) { files:=append([]PlannedFile(nil),plan.Files...); sort.Slice(files,func(i,j int)bool{return files[i].RelativePath<files[j].RelativePath}); var written []string; for _,f:=range files { if err:=validateLedgerRelativePath(f.RelativePath);err!=nil{return written,err}; path:=filepath.Join(plan.ProjectRoot,filepath.FromSlash(f.RelativePath)); old,err:=os.ReadFile(path); if err==nil&&bytes.Equal(old,f.Data){continue}; if err!=nil&&!errors.Is(err,os.ErrNotExist){return written,err}; if err:=atomicfile.Write(path,f.Data,f.Perm);err!=nil{return written,err}; written=append(written,f.RelativePath) }; return written,nil }
```

No automatic deletion: archived entities remain files. Known edits apply through parsed `Document`, never regenerated structs alone.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/ledger && go test ./internal/ledger -count=1`

Expected: PASS; five document classes render, unsafe state fails before writes, repeat has no diff.

- [ ] **Step 5: Commit**

```bash
git add internal/ledger
git commit -m "feat: render durable project ledger"
```

### Task 5: Apply with Durable Receipts and Cursor CAS

**Files:**
- Create: `internal/apply/apply.go`
- Create: `internal/apply/receipt.go`
- Create: `internal/apply/lock_unix.go`
- Create: `internal/apply/lock_windows.go`
- Create: `internal/apply/apply_test.go`

**Interfaces:**
- Consumes: proposal/evidence paths, initialized roots, validator, renderer, `cursor.Store.Commit`.
- Produces: exact `apply.Options`, `apply.Result`, `apply.Run` below.

- [ ] **Step 1: Write failing transaction tests**

```go
func TestRunWritesThenAdvancesCursor(t *testing.T) { f:=applyFixture(t); got,err:=Run(f.Options()); if err!=nil||!got.CursorAdvanced {t.Fatalf("got=%+v err=%v",got,err)}; c,_:=(cursor.Store{Root:f.ProjectData}).Load(f.SessionID); if c.LastLine!=f.Packet.NextCursor.Line||c.LastHash!=f.Packet.NextCursor.SourceHash {t.Fatalf("cursor=%+v",c)} }
func TestRunRepeatIsIdempotent(t *testing.T) { f:=applyFixture(t); Run(f.Options()); before:=hashLedger(t,f.Project); got,err:=Run(f.Options()); if err!=nil||!got.AlreadyApplied||len(got.ChangedFiles)!=0||before!=hashLedger(t,f.Project){t.Fatalf("got=%+v err=%v",got,err)} }
func TestRunStaleCursorWritesNothing(t *testing.T) { f:=applyFixture(t); f.AdvancePastPacket(t); before:=hashLedger(t,f.Project); _,err:=Run(f.Options()); if !errors.Is(err,cursor.ErrStale)||before!=hashLedger(t,f.Project){t.Fatalf("err=%v",err)} }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/apply -run '^TestRun' -count=1`

Expected: FAIL because apply is absent.

- [ ] **Step 3: Implement exact ordering and recovery**

```go
type Options struct { ProposalPath,EvidencePath,ProjectRoot,DataDir string; Now func() time.Time }
type Result struct { ProjectID,SessionID string; FromCursor,ToCursor int; ChangedFiles []string; CursorAdvanced,AlreadyApplied bool }
func Run(opts Options) (Result,error) {
	ctx,err:=openInputs(opts); if err!=nil{return Result{},err}; lock,err:=acquireProjectApplyLock(opts.DataDir,ctx.Packet.ProjectID); if err!=nil{return Result{},err}; defer lock.Release()
	store:=cursor.Store{Root:filepath.Join(opts.DataDir,"projects",ctx.Packet.ProjectID)}; current,err:=store.Load(ctx.Packet.SessionID); if err!=nil{return Result{},err}
	receipt,found,err:=loadReceipt(opts.DataDir,ctx.ProposalDigest); if err!=nil{return Result{},err}; if found { return recoverReceipt(store,current,receipt,ctx,opts) }
	if current.LastLine!=ctx.Packet.ExpectedCursor.Line||current.LastHash!=ctx.Packet.ExpectedCursor.SourceHash{return Result{},cursor.ErrStale}
	state,err:=ledger.Load(opts.ProjectRoot); if err!=nil{return Result{},err}; changes,err:=proposal.Validate(ctx.Proposal,ctx.Packet,state); if err!=nil{return Result{},err}; plan,err:=ledger.Render(state,changes); if err!=nil{return Result{},err}
	receipt=newPreparedReceipt(ctx,plan); if err:=saveReceipt(opts.DataDir,receipt);err!=nil{return Result{},err}; written,err:=applyPreparedFiles(plan,receipt); if err!=nil{return Result{},err}; receipt.State="applied"; receipt.ChangedFiles=written; if err:=saveReceipt(opts.DataDir,receipt);err!=nil{return Result{},err}; return finishReceipt(store,current,receipt,opts)
}
```

Inputs are bounded regular files and both digests are recomputed. Hold an OS-level per-project lock for the entire operation. Receipt path is `projects/<project-id>/applied-proposals/<proposal-digest>.json`; its `state` is `prepared` or `applied`, and it stores input IDs/digests, expected/next boundaries, plus preimage and target SHA-256 for every planned file. Retry writes a file only when it still equals its preimage or target; an intervening user edit fails closed. `finishReceipt` verifies target hashes and calls `Store.Commit` with `next.UpdatedAt=Now().UTC()`. Retry after prepared/partial/applied receipt finishes safely; retry after CAS returns already-applied. A later cursor accepts the receipt only when its digest/file hashes match. Add injected failure hooks after render, prepared receipt, each file, applied receipt, and before CAS.

- [ ] **Step 4: Run GREEN/race**

Run: `gofmt -w internal/apply && go test ./internal/apply -count=1 && go test -race ./internal/apply ./internal/cursor -count=1`

Expected: PASS; stale writes nothing, interrupted receipt recovers, repeat has no diff.

- [ ] **Step 5: Commit**

```bash
git add internal/apply
git commit -m "feat: apply proposals with cursor CAS"
```

### Task 6: Derive Mermaid from Accepted State

**Files:**
- Create: `internal/diagram/render.go`
- Create: `internal/diagram/render_test.go`
- Modify: `internal/ledger/render.go`

**Interfaces:**
- Consumes: `ledger.State` only.
- Produces: `diagram.Render(ledger.State) ([]byte,error)` for `diagrams/project-evolution.md`, containing causal `flowchart LR` and relationship `graph TD`.

- [ ] **Step 1: Write failing stability/escaping test**

```go
func TestRenderStableAndEscaped(t *testing.T) { s:=diagramFixture(`Choose "A" [safely]`); a,err:=Render(s); if err!=nil{t.Fatal(err)}; b,_:=Render(s); if !bytes.Equal(a,b)||!bytes.Contains(a,[]byte("flowchart LR"))||!bytes.Contains(a,[]byte("graph TD"))||!bytes.Contains(a,[]byte("&quot;A&quot;")){t.Fatalf("diagram=%s",a)} }
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/diagram -run '^TestRenderStableAndEscaped$' -count=1`

Expected: FAIL because package is absent.

- [ ] **Step 3: Implement derived graphs**

```go
func nodeID(kind,id string)string{sum:=sha256.Sum256([]byte(kind+"\x00"+id));return kind+"_"+hex.EncodeToString(sum[:6])}
func escapeLabel(s string)string{return strings.NewReplacer("&","&amp;",`"`,"&quot;","[","&#91;","]","&#93;","<","&lt;",">","&gt;","\n"," ").Replace(s)}
```

Sort timeline `(OccurredAt,ID)`, other entities by ID, tags bytewise. Causal edges use timeline and explicit decision/loop IDs. Relationship edges connect project, sessions, decisions, loops, blockers, next experiments. Never parse diagrams back. `ledger.Render` adds the derived file only when bytes differ.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/diagram internal/ledger/render.go && go test ./internal/diagram ./internal/ledger -count=1`

Expected: PASS; arbitrary text is safe and repeat is identical.

- [ ] **Step 5: Commit**

```bash
git add internal/diagram internal/ledger/render.go
git commit -m "feat: derive project evolution diagrams"
```

### Task 7: Add Ledger-Only Resume and History

**Files:**
- Create: `internal/recovery/resume.go`
- Create: `internal/recovery/history.go`
- Create: `internal/recovery/recovery_test.go`

**Interfaces:**
- Consumes: `ledger.Load(projectRoot)` only.
- Produces: `ResumeLedgerOnly(projectRoot string) (ResumeCard,error)`; `(ResumeCard).Markdown() string`; `HistoryLedgerOnly(projectRoot string) (HistoryView,error)`; `(HistoryView).Markdown() string`; exact view fields below.

- [ ] **Step 1: Write failing accepted-only tests**

```go
func TestResumeIgnoresPendingEvidence(t *testing.T){root:=recoveryFixture(t);card,err:=ResumeLedgerOnly(root);if err!=nil||card.Goal!="Ship ledger"||strings.Contains(card.Markdown(),"UNREVIEWED-CANARY"){t.Fatalf("card=%+v err=%v",card,err)}}
func TestHistoryGroupsEditableTags(t *testing.T){root:=recoveryFixture(t);view,err:=HistoryLedgerOnly(root);if err!=nil||len(view.Themes)!=1||view.Themes[0].Name!="durability"{t.Fatalf("view=%+v err=%v",view,err)}}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/recovery -run 'Test(ResumeIgnoresPendingEvidence|HistoryGroupsEditableTags)$' -count=1`

Expected: FAIL because views are absent.

- [ ] **Step 3: Implement exact types**

```go
type ResumeCard struct { ProjectID,Goal,StopPoint,LastVerified string; Drift,Blockers,OpenQuestions []string; NextAction,FirstInspection string; SourceSessions []string }
type Theme struct { Name string; DecisionIDs,OpenLoopIDs []string }
type HistoryView struct { ProjectID string; Timeline []ledger.TimelineEvent; Decisions []ledger.Decision; OpenLoops []ledger.OpenLoop; Themes []Theme }
```

Implement the exact public functions declared in **Interfaces**: `ResumeLedgerOnly(projectRoot string) (ResumeCard,error)`, `(ResumeCard).Markdown() string`, `HistoryLedgerOnly(projectRoot string) (HistoryView,error)`, and `(HistoryView).Markdown() string`. Stop point is final phase of latest accepted session report; drift uses accepted uncommitted changes/open risks; open questions use unresolved loop titles. History follows explicit supersedes links and groups non-empty user tags only, with bytewise-sorted names/IDs. Neither function reads JSONL, evidence, receipts, Git, environment, or diagrams.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/recovery && go test ./internal/recovery -count=1`

Expected: PASS; output is compact, ordered, accepted-only.

- [ ] **Step 5: Commit**

```bash
git add internal/recovery
git commit -m "feat: add ledger-only resume and history"
```

### Task 8: Expose Deterministic Apply/Resume/History CLI Commands

**Files:**
- Create: `internal/cli/apply.go`
- Create: `internal/cli/recovery.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**
- Produces: `apply --proposal P --evidence E [--project . --data-dir D]`; `resume --ledger-only [--project .]`; `history --ledger-only [--project .]`.

- [ ] **Step 1: Write failing boundary tests**

```go
func TestRecoveryRequiresLedgerOnly(t *testing.T){for _,cmd:=range []string{"resume","history"}{var out,errOut bytes.Buffer;code:=Run([]string{cmd,"--project",t.TempDir()},&out,&errOut);if code!=2||!strings.Contains(errOut.String(),"--ledger-only"){t.Fatalf("%s code=%d err=%q",cmd,code,errOut.String())}}}
func TestApplyReportsDeterministicResult(t *testing.T){f:=cliApplyFixture(t);var out,errOut bytes.Buffer;code:=Run([]string{"apply","--proposal",f.Proposal,"--evidence",f.Evidence,"--project",f.Project,"--data-dir",f.Data},&out,&errOut);if code!=0||!strings.Contains(out.String(),"cursor_advanced: true")||strings.Contains(strings.ToLower(out.String()),"summarized"){t.Fatalf("code=%d out=%q err=%q",code,out.String(),errOut.String())}}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/cli -run 'Test(RecoveryRequiresLedgerOnly|ApplyReportsDeterministicResult)$' -count=1`

Expected: FAIL because commands are absent.

- [ ] **Step 3: Add strict parsing/routing**

```go
case "apply": return runApply(args[1:],stdout,stderr)
case "resume": return runRecovery("resume",args[1:],stdout,stderr)
case "history": return runRecovery("history",args[1:],stdout,stderr)
```

Apply requires proposal/evidence, defaults project/data safely, rejects positionals, and prints only IDs, cursor range, changed relative paths, `cursor_advanced`, `already_applied`. Recovery requires `--ledger-only`, rejects prompts/positionals, and prints `Markdown()`. Safe diagnostics never print proposal/evidence content. Help says apply validates a Skill proposal and ledger-only modes do not process pending sessions.

- [ ] **Step 4: Run GREEN/privacy**

Run: `gofmt -w internal/cli && go test ./internal/cli ./internal/apply ./internal/recovery -count=1`

Expected: PASS; `.git` before/after hash is unchanged and no content canary leaks.

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat: expose ledger application and recovery"
```

### Task 9: Package Review, Checkpoint, and Resume Skill Workflows

**Files:**
- Create: `skill/session-reviewer/SKILL.md`
- Create: `skill/session-reviewer/references/proposal-v1.schema.json`
- Create: `skill/session-reviewer/scripts/prepare-workflow.sh`
- Create: `skill/session-reviewer/scripts/prepare-workflow.ps1`
- Create: `skill/session-reviewer/scripts/apply-proposal.sh`
- Create: `skill/session-reviewer/scripts/apply-proposal.ps1`
- Create: `skill/session-reviewer/tests/package_test.go`

**Interfaces:**
- Consumes: natural-language intent, installed binary, bounded packet, packaged schema.
- Produces: argument-preserving wrappers and one-proposal-per-packet instructions.

- [ ] **Step 1: Write failing packaging tests**

```go
func TestSchemaCopyMatches(t *testing.T){a,_:=os.ReadFile("../../../schemas/proposal-v1.schema.json");b,_:=os.ReadFile("../references/proposal-v1.schema.json");if !bytes.Equal(a,b){t.Fatal("schema drift")}}
func TestInstructionsCoverContracts(t *testing.T){b,_:=os.ReadFile("../SKILL.md");for _,s:=range []string{"review","checkpoint","resume","has_more","evidence_packet_sha256","Never edit ledger files directly","Never run Git mutation commands"}{if !bytes.Contains(b,[]byte(s)){t.Fatalf("missing %q",s)}}}
```

- [ ] **Step 2: Run RED**

Run: `go test ./skill/session-reviewer/tests -count=1`

Expected: FAIL because package is absent.

- [ ] **Step 3: Add exact wrappers**

```sh
#!/bin/sh
set -eu
if [ "$#" -lt 2 ]; then echo "usage: prepare-workflow.sh <review|checkpoint> <output> [flags]" >&2; exit 2; fi
mode=$1; output=$2; shift 2
case "$mode" in review|checkpoint) ;; *) exit 2 ;; esac
exec session-reviewer prepare "$mode" --output "$output" "$@"
```

```sh
#!/bin/sh
set -eu
if [ "$#" -lt 2 ]; then echo "usage: apply-proposal.sh <proposal> <evidence> [flags]" >&2; exit 2; fi
proposal=$1; evidence=$2; shift 2
exec session-reviewer apply --proposal "$proposal" --evidence "$evidence" "$@"
```

PowerShell wrappers use mandatory typed parameters, `[Parameter(ValueFromRemainingArguments=$true)][string[]]$Rest`, invoke with `@Rest`, and exit `$LASTEXITCODE`. No wrapper reads JSONL, calls an API, edits Markdown, or invokes Git.

- [ ] **Step 4: Write complete Skill algorithm and verify**

`SKILL.md` must: classify review/checkpoint/resume; for resume render ledger-only first; create a private temporary directory; prepare one packet; read that packet plus accepted entities; create exact schema JSON with evidence/digest/cursors; apply only via wrapper; delete only explicit temporary files after success; if `has_more` repeat prepare/synthesize/apply after CAS; on failure stop without claiming acceptance; report entities/cursor. It explicitly prohibits direct ledger edits, raw-log reads, hidden-reasoning interpretation, Git mutation, and claiming ledger-only processed pending sessions.

Run: `chmod +x skill/session-reviewer/scripts/*.sh && cp schemas/proposal-v1.schema.json skill/session-reviewer/references/proposal-v1.schema.json && go test ./skill/session-reviewer/tests -count=1 && sh -n skill/session-reviewer/scripts/*.sh`

Expected: PASS; schema matches, all workflows/idempotence boundaries are named, shell parses.

- [ ] **Step 5: Commit**

```bash
git add skill/session-reviewer
git commit -m "feat: package semantic review workflows"
```

### Task 10: Prove Multi-Packet Incrementality and Document the Milestone

**Files:**
- Create: `testdata/proposals/valid-second.json`
- Modify: `internal/apply/apply_test.go`
- Modify: `internal/prepare/acceptance_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: two bounded packets/proposals for one session.
- Produces: acceptance evidence for exact progression, one coherent report, stable diagrams, repeated-apply no-diff, manual watcher-free recovery.

- [ ] **Step 1: Write failing two-packet test**

```go
func TestTwoPacketWorkflowIsIncrementalAndIdempotent(t *testing.T){f:=multiPacketFixture(t);p1:=f.Prepare(t);r1,err:=Run(f.ApplyOptions(t,p1,"valid-first.json"));if err!=nil||!r1.CursorAdvanced||!p1.HasMore{t.Fatalf("r1=%+v err=%v",r1,err)};p2:=f.Prepare(t);if p2.ExpectedCursor!=p1.NextCursor||p2.FromCursor!=p1.ToCursor+1{t.Fatalf("p1=%+v p2=%+v",p1,p2)};opts:=f.ApplyOptions(t,p2,"valid-second.json");r2,err:=Run(opts);if err!=nil||!r2.CursorAdvanced||p2.HasMore{t.Fatalf("r2=%+v err=%v",r2,err)};before:=hashLedger(t,f.Project);again,err:=Run(opts);if err!=nil||!again.AlreadyApplied||before!=hashLedger(t,f.Project){t.Fatalf("again=%+v err=%v",again,err)};state,_:=ledger.Load(f.Project);if len(state.Timeline)!=2||len(state.Sessions)!=1{t.Fatalf("state=%+v",state)}}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/apply -run '^TestTwoPacketWorkflowIsIncrementalAndIdempotent$' -count=1`

Expected: FAIL until second fixture/harness exists.

- [ ] **Step 3: Add second proposal and exact README workflow**

Second proposal updates the existing session report revision, adds one timeline event, patches current state, and updates/closes one loop without duplicating decision 1; fixture binds its digest/cursors at runtime. README shows prepare -> Skill proposal -> apply -> ledger-only resume/history; says `has_more` loops only after apply; unknown content survives; diagrams derive; watcher is unnecessary; CLI has no model/Git mutation; sync/watcher/release remain future work.

- [ ] **Step 4: Run final gate**

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer.exe ./cmd/session-reviewer
go test ./skill/session-reviewer/tests -count=1
sh -n skill/session-reviewer/scripts/*.sh
git diff --check
```

Expected: all exit 0; native macOS/Windows CI passes; repeated packet 2 has no diff; Git state is untouched.

- [ ] **Step 5: Commit**

```bash
git add testdata/proposals/valid-second.json internal/apply/apply_test.go internal/prepare/acceptance_test.go README.md
git commit -m "test: verify incremental ledger workflows"
```

## Ledger and Skill Completion Gate

- Packets bind proposals to exact cursor hashes.
- Proposal schema/types/evidence/transitions/revisions/redaction fail closed.
- All target bytes render before writes; receipts plus CAS recover retries.
- Current state, timeline, decisions, loops, sessions, and two Mermaid graphs are stable.
- Unknown fields/sections and user titles/statuses/tags/narrative survive.
- Resume/history are accepted-ledger-only; themes derive from tags, not CLI inference.
- Skill covers review/checkpoint/resume and repeated `has_more` packets.
- The CLI has no model/API client and no automatic Git mutation.

Plan complete and saved to `docs/superpowers/plans/2026-08-23-session-reviewer-ledger-skill.md`. Two execution options:

1. **Subagent-Driven (recommended)** - dispatch a fresh subagent per task and review between tasks.
2. **Inline Execution** - execute using executing-plans with checkpoints.
