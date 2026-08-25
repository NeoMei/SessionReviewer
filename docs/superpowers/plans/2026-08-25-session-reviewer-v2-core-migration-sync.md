# SessionReviewer v2 Core, Migration, and Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Replace the visible multi-document v1 ledger with two editable Chinese Markdown documents plus a validated hidden machine ledger, migrate existing projects atomically, and keep apply, recovery, accounting, conflict resolution, and Project ↔ Obsidian synchronization working.

**Architecture:** Add an isolated internal/reviewv2 package that owns the v2 document grammar, JSON machine ledger, legacy projection, and migration transaction while continuing to use ledger.State and ledger.ChangeSet as the proposal compatibility model. Then cut apply, recovery, sync inventory, generated publication, and CLI status over to the v2 accepted-state boundary without weakening existing rooted filesystem, receipt, CAS, or three-way-merge protections.

**Tech Stack:** Go 1.26, goldmark 1.8.2, yaml.v3 3.0.1, existing pathguard/atomicfile/sync transaction primitives, JSON Schema, Go test/race/vet, macOS and Windows cross-builds.

## Global Constraints

- Normal human-readable output is exactly docs/session-review/项目回顾.md and docs/session-review/项目历史.md.
- Machine data is docs/session-review/.session-reviewer/ledger.json; it is versioned and published Project → Vault only.
- Migration backups stay under Project .session-reviewer/backups, are excluded from ordinary inventory, Git by default, and Vault publication, and are never deleted automatically.
- Human fields remain editable through Base/Project/Vault three-way merge; machine accounting, hashes, cursor, evidence, and sync metadata are reserved.
- Project history is one reverse-chronological event stream, not Session directories.
- Migration, apply, and sync fail before writes on malformed input, duplicate identity, illegal path, reserved edits, or inconsistent references.
- A failed render or migration leaves no half-v2 output; crash recovery completes or rolls back from a content-addressed journal.
- Cost uses public USD per-million-token pricing; subscription entitlements do not change cost.
- Real acceptance requires dry-run, status, actual sync, repeat status, and repeat dry-run against the configured Project/Vault mapping.
- macOS and Windows are release gates.
- The first release carrying schema v2 and the Obsidian browser is v0.2.0; v0.1.0 remains the legacy schema release.

## File Structure

- Create internal/reviewv2/types.go: v2 human and machine domain types and stable paths.
- Create internal/reviewv2/validate.go: schema, identity, reference, size, and accounting validation.
- Create internal/reviewv2/ledger_json.go: deterministic ledger.json codec.
- Create internal/reviewv2/review_markdown.go: 项目回顾.md structural parser and renderer.
- Create internal/reviewv2/history_markdown.go: 项目历史.md event-block parser and renderer.
- Create internal/reviewv2/load.go: accepted v2 loading and snapshot accounting.
- Create internal/reviewv2/project.go: legacy ledger.State ↔ v2 projection and ChangeSet application.
- Create internal/reviewv2/migrate.go: dry-run plan, backup manifest, transaction stages, recovery.
- Create schemas/review-ledger-v2.schema.json: public machine-ledger contract.
- Create testdata/review-v2/: golden Markdown/JSON and malformed fixtures shared with the plugin plan.
- Modify internal/apply/apply.go: use the v2 accepted-state facade after migration.
- Modify internal/recovery/resume.go and internal/recovery/history.go: load v2 while keeping CLI output contracts.
- Modify internal/syncdoc/document.go and internal/syncdoc/body.go: marker-stable semantic units for the two aggregate documents.
- Modify internal/syncdoc/scan.go: include only two visible v2 entities and exclude .session-reviewer.
- Modify internal/sync/service.go and internal/sync/derived.go: automatic migration gate, one-way machine ledger publication, and current status.
- Modify internal/sync/conflict.go: hidden JSON conflict artifacts instead of visible Markdown notes.
- Modify internal/cli/sync.go and internal/cli/run.go: migration preview/reporting and machine-ledger repair command.
- Modify internal/project/init.go: initialize v2 files for new projects.
- Modify skill/session-reviewer/SKILL.md, README.md, schemas/proposal-v1.schema.json only where the accepted output contract changes; proposal schema stays version 1.
- Modify .github/workflows/ci.yml, scripts/build-release.sh, scripts/build-release.ps1, and release packager tests only for new schema/testdata inclusion and v2 gates.

---

### Task 1: Freeze the v2 Domain and Machine-Ledger Contract

**Files:**
- Create: internal/reviewv2/types.go
- Create: internal/reviewv2/validate.go
- Create: internal/reviewv2/ledger_json.go
- Create: internal/reviewv2/ledger_json_test.go
- Create: schemas/review-ledger-v2.schema.json
- Create: testdata/review-v2/ledger.valid.json
- Create: testdata/review-v2/ledger.invalid-duplicate-id.json

**Interfaces:**
- Consumes: accounting.ProjectSummary, accounting.SessionAccounting, ledger.EvidenceRef.
- Produces: reviewv2.State, reviewv2.MachineLedger, reviewv2.Validate, reviewv2.ParseMachineLedger, reviewv2.RenderMachineLedger and the three stable relative-path constants used by every later task.

- [ ] **Step 1: Write failing contract tests**

~~~go
func TestMachineLedgerRoundTripIsDeterministicAndRejectsDuplicateIdentity(t *testing.T) {
    valid := mustFixture(t, "../../testdata/review-v2/ledger.valid.json")
    first, err := ParseMachineLedger(valid)
    if err != nil { t.Fatal(err) }
    rendered, err := RenderMachineLedger(first)
    if err != nil { t.Fatal(err) }
    second, err := ParseMachineLedger(rendered)
    if err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(first, second) { t.Fatalf("round trip changed state") }
    renderedAgain, err := RenderMachineLedger(second)
    if err != nil || !bytes.Equal(rendered, renderedAgain) {
        t.Fatalf("non-deterministic render: err=%v", err)
    }
    if _, err := ParseMachineLedger(mustFixture(t, "../../testdata/review-v2/ledger.invalid-duplicate-id.json")); err == nil {
        t.Fatal("duplicate identity accepted")
    }
}
~~~

- [ ] **Step 2: Run the focused test and verify RED**

Run: go test ./internal/reviewv2 -run TestMachineLedgerRoundTripIsDeterministicAndRejectsDuplicateIdentity -count=1

Expected: FAIL because internal/reviewv2 and ParseMachineLedger do not exist.

- [ ] **Step 3: Define the exact v2 types and paths**

~~~go
const (
    SchemaVersion = 2
    ReviewRelativePath = "docs/session-review/项目回顾.md"
    HistoryRelativePath = "docs/session-review/项目历史.md"
    MachineLedgerRelativePath = "docs/session-review/.session-reviewer/ledger.json"
)

type Risk struct { ID, Title, Status, Detail string }
type Decision struct {
    ID, OccurredAt, Title, Rationale, Impact, Status string
}
type Event struct {
    ID, OccurredAt, Kind, Title, Meaning, Summary, Why, Next string
    Changes, Results, DecisionIDs []string
}
type Review struct {
    ProjectID string
    Revision int
    Name, Goal, Stage, Status, NextAction, LastVerification string
    Risks []Risk
    Decisions []Decision
}
type MachineLedger struct {
    SchemaVersion int `json:"schema_version"`
    ProjectID string `json:"project_id"`
    AcceptedRevision int `json:"accepted_revision"`
    ReviewSHA256 string `json:"review_sha256"`
    HistorySHA256 string `json:"history_sha256"`
    LastSuccessfulSync string `json:"last_successful_sync,omitempty"`
    Accounting accounting.ProjectSummary `json:"accounting"`
    Sessions []ledger.SessionReport `json:"sessions"`
    Evidence map[string][]ledger.EvidenceRef `json:"evidence"`
}
type State struct {
    Review Review
    Events []Event
    Machine MachineLedger
}
~~~

Use a JSON encoder with SetEscapeHTML(false), two-space indentation, a trailing newline, sorted Sessions by SessionID, sorted evidence map keys through a wire slice, and lower-case SHA-256 validation. Reject unknown schema versions, duplicate risk/decision/event/session/evidence IDs, missing decision references, non-finite costs, negative token counts, and any document over 4 MiB or ledger over 16 MiB.

- [ ] **Step 4: Add the public JSON Schema**

The schema must set additionalProperties to false at every machine-owned object, require schema_version=2, project_id, accepted_revision, both document hashes, accounting, sessions, and evidence, and enforce integer minimum 0 for tokens/duration plus number minimum 0 for costs. Validate the valid and invalid fixtures from a Go schema-shape test that checks required property names against MachineLedger JSON tags.

- [ ] **Step 5: Run focused and package tests**

Run: go test ./internal/reviewv2 -count=1

Expected: PASS.

- [ ] **Step 6: Commit**

~~~bash
git add internal/reviewv2 schemas/review-ledger-v2.schema.json testdata/review-v2
git commit -m "feat: define review ledger v2 contract"
~~~

### Task 2: Implement Lossless Two-Document Markdown Codecs

**Files:**
- Create: internal/reviewv2/markers.go
- Create: internal/reviewv2/review_markdown.go
- Create: internal/reviewv2/history_markdown.go
- Create: internal/reviewv2/markdown_test.go
- Create: testdata/review-v2/项目回顾.valid.md
- Create: testdata/review-v2/项目历史.valid.md
- Create: testdata/review-v2/项目历史.invalid-duplicate-event.md

**Interfaces:**
- Consumes: reviewv2.Review, reviewv2.Event, stable paths from Task 1.
- Produces: ParseReview, RenderReview, ParseHistory, RenderHistory, PatchReviewUnit and PatchHistoryUnit. Task 6 uses their stable semantic unit keys.

- [ ] **Step 1: Write failing round-trip and marker tests**

~~~go
func TestTwoDocumentRoundTripPreservesUnknownContentAndStableIDs(t *testing.T) {
    reviewSource := mustFixture(t, "../../testdata/review-v2/项目回顾.valid.md")
    reviewDoc, err := ParseReview(reviewSource)
    if err != nil { t.Fatal(err) }
    if reviewDoc.Model.Decisions[0].ID != "decision-local-cli" { t.Fatalf("decision=%+v", reviewDoc.Model.Decisions[0]) }
    reviewOut, err := reviewDoc.Render()
    if err != nil || !bytes.Equal(reviewSource, reviewOut) { t.Fatalf("review round trip err=%v", err) }

    historySource := mustFixture(t, "../../testdata/review-v2/项目历史.valid.md")
    historyDoc, err := ParseHistory(historySource)
    if err != nil { t.Fatal(err) }
    if historyDoc.Events[0].ID != "timeline-trust-chain" { t.Fatalf("events=%+v", historyDoc.Events) }
    historyOut, err := historyDoc.Render()
    if err != nil || !bytes.Equal(historySource, historyOut) { t.Fatalf("history round trip err=%v", err) }
}
~~~

- [ ] **Step 2: Run the focused test and verify RED**

Run: go test ./internal/reviewv2 -run TestTwoDocumentRoundTripPreservesUnknownContentAndStableIDs -count=1

Expected: FAIL because the Markdown codecs do not exist.

- [ ] **Step 3: Implement the strict visible grammar**

Use these exact frontmatter identities:

~~~yaml
id: project-overview
entity_type: project_review
project_id: project-0123456789abcdef
schema_version: 2
revision: 1
~~~

Use these exact hidden block markers:

~~~html
<!-- session-reviewer:decision id="decision-local-cli" -->
### Skill + 本地 CLI
#### 原因
原始会话不上传。
#### 影响
语义与状态完整性分层。
<!-- /session-reviewer:decision -->

<!-- session-reviewer:event id="timeline-trust-chain" -->
## 2026-08-25 · 信任链与 dry-run 边界修复
### 节点意义
从能运行进入可放心发布。
### 为什么会走到这里
真实 Vault 暴露单元测试未覆盖的边界。
### 发生了什么
- receipt 纳入可信状态判断
### 结果与验证
- 重复 dry-run 为零变更
### 关联决策
- decision-local-cli
### 留下的问题或下一步
验证安装器权限。
<!-- /session-reviewer:event -->
~~~

Parse markers with an exact line scanner, not a broad regular expression. Require one opening and one matching closing marker, stable lower-case IDs, no nesting, no duplicate IDs, no marker inside fenced code, and maximum 20,000 blocks. Parse Markdown headings inside each bounded slice with goldmark. Preserve exact source slices for unchanged frontmatter, preamble, unknown sections, unknown decision subsections, and unknown event subsections.

- [ ] **Step 4: Implement field-level patching with stale-hash protection**

~~~go
type EditUnit struct {
    Document string
    UnitID string
    Field string
    Value string
    ExpectedSHA256 string
}

func PatchReviewUnit(source []byte, edit EditUnit) ([]byte, error)
func PatchHistoryUnit(source []byte, edit EditUnit) ([]byte, error)
~~~

Allow only goal, stage, status, next_action, risk.title, risk.status, risk.detail, decision.title, decision.rationale, decision.impact, event.title, event.meaning, event.summary, event.why, event.changes, event.results, and event.next. Reject schema_version, project_id, revision, hashes, evidence, usage, price, and sync fields before rendering. Reparse rendered bytes before returning.

- [ ] **Step 5: Add hostile and cross-platform fixtures**

Test CRLF input normalization, Chinese paths, quotes, brackets, embedded Mermaid, a fake marker inside a code fence, duplicate event IDs, missing close markers, 4 MiB limits, and a title edit that retains the same hidden event ID.

- [ ] **Step 6: Run package tests**

Run: go test ./internal/reviewv2 -run 'TestTwoDocument|TestPatch|TestMarker' -count=1

Expected: PASS.

- [ ] **Step 7: Commit**

~~~bash
git add internal/reviewv2 testdata/review-v2
git commit -m "feat: add compact review markdown codecs"
~~~

### Task 3: Project Legacy State into v2 and Preserve Accounting

**Files:**
- Create: internal/reviewv2/project.go
- Create: internal/reviewv2/project_test.go
- Create: internal/reviewv2/load.go
- Create: internal/reviewv2/load_test.go
- Modify: internal/ledger/render.go
- Modify: internal/ledger/render_test.go
- Modify: internal/accounting/accounting.go
- Modify: internal/accounting/accounting_test.go

**Interfaces:**
- Consumes: ledger.State, ledger.ChangeSet, ledger.ApplyChangeSetModel, accounting.Aggregate, Task 1/2 codecs.
- Produces: ProjectLegacy, LegacyState, ApplyChangeSet, Load, LoadExpected, SnapshotUsageExpected, and Render accepted-state plans for Task 5.

- [ ] **Step 1: Write the failing projection test**

~~~go
func TestProjectLegacyProducesTwoDocumentsAndMachineLedger(t *testing.T) {
    legacy := legacyFixtureState(t)
    state, err := ProjectLegacy(legacy)
    if err != nil { t.Fatal(err) }
    if len(state.Events) != len(legacy.Timeline) { t.Fatalf("events=%d", len(state.Events)) }
    if state.Review.Decisions[0].ID == "" || state.Review.NextAction != legacy.CurrentState.NextAction {
        t.Fatalf("review=%+v", state.Review)
    }
    if state.Machine.Accounting.TotalTokens == 0 || state.Machine.Accounting.TotalCostUSD == 0 {
        t.Fatalf("accounting=%+v", state.Machine.Accounting)
    }
    plan, err := Render("", state)
    if err != nil { t.Fatal(err) }
    if got := plannedPaths(plan); !reflect.DeepEqual(got, []string{
        HistoryRelativePath, MachineLedgerRelativePath, ReviewRelativePath,
    }) { t.Fatalf("paths=%v", got) }
}
~~~

- [ ] **Step 2: Run the focused test and verify RED**

Run: go test ./internal/reviewv2 -run TestProjectLegacyProducesTwoDocumentsAndMachineLedger -count=1

Expected: FAIL because ProjectLegacy and Render do not exist.

- [ ] **Step 3: Implement deterministic projection**

Map CurrentState.Goal, LastVerified, NextAction, blockers and open risks to Review. Map each open loop to a stable Risk. Map decisions to canonical Review decisions. Map each timeline event to one Event and enrich it deterministically from referenced decision/open-loop/session fields without model calls. Sort events by occurred_at descending then ID, decisions by occurred_at descending then ID, and risks by status then ID.

Keep complete SessionReport and EvidenceRef values only in MachineLedger. Compute accounting with accounting.Aggregate and add a ValidateProjectSummary function that re-sums every model and rejects token/cost/share mismatch with a 1e-9 USD tolerance and 1e-6 percentage tolerance.

- [ ] **Step 4: Implement accepted-state loading and legacy compatibility**

~~~go
type Accepted struct {
    State State
    Legacy ledger.State
    Snapshot ledger.SnapshotUsage
}

func Load(projectRoot string) (Accepted, error)
func LoadExpected(projectRoot string, expectedRoot os.FileInfo) (Accepted, error)
func ApplyChangeSet(current Accepted, changes ledger.ChangeSet) (ledger.WritePlan, error)
func SnapshotUsageExpected(projectRoot string, expectedRoot os.FileInfo) (ledger.SnapshotUsage, error)
~~~

LegacyState reconstructs public ledger.State fields from Review, Events, Sessions, Evidence, and accounting so proposal.Validate, resume, and history keep their current semantic inputs. Export ledger.ApplyChangeSetModel(state State, changes ChangeSet) (State, error) as a pure clone-and-validate wrapper around the existing private applyChanges logic; it must not render or require loaded documents. reviewv2.ApplyChangeSet calls that function, reprojects to v2, computes both Markdown hashes, and returns a three-file ledger.WritePlan with exact preimages.

DetectVersion returns legacy, v2, mixed, or empty. Load/LoadExpected accept v2 only and return a typed ErrMigrationRequired for a valid legacy ledger. Add LoadAnyReadOnly for resume/history so users can still inspect a legacy project before migration; all write paths require v2.

- [ ] **Step 5: Verify accounting and accepted-state tests**

Run: go test ./internal/accounting ./internal/reviewv2 -count=1

Expected: PASS.

- [ ] **Step 6: Commit**

~~~bash
git add internal/reviewv2 internal/accounting internal/ledger
git commit -m "feat: project accepted ledger into review v2"
~~~

### Task 4: Add Previewable, Recoverable Legacy Migration

**Files:**
- Create: internal/reviewv2/migrate.go
- Create: internal/reviewv2/migrate_test.go
- Create: internal/reviewv2/migration_journal.go
- Create: internal/reviewv2/migration_journal_test.go
- Modify: internal/pathguard/tree.go
- Modify: .gitignore

**Interfaces:**
- Consumes: ledger.LoadExpected, reviewv2.ProjectLegacy, Task 2 renderers, pathguard.Directory, atomicfile.
- Produces: DetectVersion, PlanMigration, ApplyMigration, RecoverMigration, MigrationPlan and MigrationReport used by sync in Task 7.

- [ ] **Step 1: Write crash-stage and no-write dry-run tests**

~~~go
func TestMigrationDryRunWritesNothingAndCrashRecoveryConverges(t *testing.T) {
    for _, failAfter := range []Stage{StageBackupComplete, StageV2Written, StageLegacyMoved} {
        t.Run(string(failAfter), func(t *testing.T) {
            f := newLegacyMigrationFixture(t)
            before := f.snapshot()
            plan, err := PlanMigration(f.project, f.projectInfo, f.data, f.now)
            if err != nil { t.Fatal(err) }
            if !reflect.DeepEqual(before, f.snapshot()) { t.Fatal("planning mutated filesystem") }
            err = applyMigrationWithHook(plan, func(stage Stage) error {
                if stage == failAfter { return errors.New("injected crash") }
                return nil
            })
            if err == nil { t.Fatal("injected crash was ignored") }
            if err := RecoverMigration(f.project, f.projectInfo, f.data); err != nil { t.Fatal(err) }
            assertV2OnlyVisible(t, f.project)
            assertBackupManifestComplete(t, f.project)
        })
    }
}
~~~

- [ ] **Step 2: Run the focused test and verify RED**

Run: go test ./internal/reviewv2 -run TestMigrationDryRunWritesNothingAndCrashRecoveryConverges -count=1

Expected: FAIL because migration planning and recovery do not exist.

- [ ] **Step 3: Implement migration stages**

~~~go
type Stage string
const (
    StagePlanned Stage = "planned"
    StageBackupComplete Stage = "backup_complete"
    StageV2Written Stage = "v2_written"
    StageLegacyMoved Stage = "legacy_moved"
    StageCommitted Stage = "committed"
)
type MigrationPlan struct {
    ProjectRoot string
    ProjectInfo os.FileInfo
    BackupRoot string
    Legacy []ledger.SnapshotFile
    Writes []ledger.PlannedFile
    ManifestSHA256 string
}
type MigrationReport struct {
    Required bool
    DryRun bool
    BackupRelative string
    Creates, Archives []string
}
~~~

Persist a content-free machine-local journal under the configured project data root. Copy legacy bytes into the Project hidden backup using create-if-absent and verify each manifest hash. Write v2 targets through atomicfile with expected preimages. Move every legacy visible file and directory into the already-verified backup tree, never following redirects. Mark committed only after v2 reparses, the backup manifest matches, and ordinary visible inventory is exactly two Markdown files.

- [ ] **Step 4: Define fail-closed migration rules**

Reject mixed v1/v2 visible state without a matching journal, duplicate legacy identities, unsafe old paths, unexpected files added after planning, backup collisions, Project root replacement, case/NFC collisions, symlink/junction/reparse entries, and total backup growth above the existing 64 MiB/4096-file ledger limits. Recovery must revalidate current bytes against the journal before completing; a user edit after interruption returns a stable stale_migration diagnostic without overwriting it.

- [ ] **Step 5: Exclude machine backup paths**

Add docs/session-review/.session-reviewer/backups/ to .gitignore. Update pathguard pruning so .session-reviewer/backups is not traversed by ordinary Markdown scans, while exact v2 ledger.json remains readable through an explicit rooted path.

- [ ] **Step 6: Run migration, path, and race tests**

Run: go test ./internal/reviewv2 ./internal/pathguard -count=1

Run: go test -race ./internal/reviewv2 -run 'TestMigration|TestRecoverMigration' -count=1

Expected: PASS for both commands.

- [ ] **Step 7: Commit**

~~~bash
git add internal/reviewv2 internal/pathguard .gitignore
git commit -m "feat: add atomic review v2 migration"
~~~

### Task 5: Cut Apply, Resume, and History Over to the v2 Accepted Boundary

**Files:**
- Modify: internal/apply/apply.go
- Modify: internal/apply/apply_test.go
- Modify: internal/apply/receipt.go
- Modify: internal/recovery/resume.go
- Modify: internal/recovery/history.go
- Modify: internal/recovery/recovery_test.go
- Modify: internal/diagram/render.go

**Interfaces:**
- Consumes: reviewv2.LoadExpected, Accepted.Legacy, ApplyChangeSet, SnapshotUsageExpected.
- Produces: unchanged proposal-v1 apply input and unchanged public resume/history CLI output backed by v2 files.

- [ ] **Step 1: Write failing v2 apply and recovery acceptance tests**

~~~go
func TestRunAppliesProposalToOnlyV2VisibleDocuments(t *testing.T) {
    f := newV2ApplyFixture(t)
    result, err := Run(f.options())
    if err != nil { t.Fatal(err) }
    if !reflect.DeepEqual(result.ChangedFiles, []string{
        reviewv2.HistoryRelativePath,
        reviewv2.MachineLedgerRelativePath,
        reviewv2.ReviewRelativePath,
    }) { t.Fatalf("changed=%v", result.ChangedFiles) }
    assertNoLegacyVisibleFiles(t, f.projectRoot)
}

func TestResumeAndHistoryLoadV2WithoutMachineFieldsInHumanOutput(t *testing.T) {
    root := v2RecoveryFixture(t)
    resume, err := ResumeLedgerOnly(root)
    if err != nil { t.Fatal(err) }
    history, err := HistoryLedgerOnly(root)
    if err != nil { t.Fatal(err) }
    output := resume.Markdown() + history.Markdown()
    for _, forbidden := range []string{"source_hash", "cursor", "revision", "evidence_id"} {
        if strings.Contains(output, forbidden) { t.Fatalf("leaked %s", forbidden) }
    }
}
~~~

- [ ] **Step 2: Run tests and verify RED**

Run: go test ./internal/apply ./internal/recovery -run 'TestRunAppliesProposalToOnlyV2|TestResumeAndHistoryLoadV2' -count=1

Expected: FAIL because apply and recovery still call ledger.Load/Render.

- [ ] **Step 3: Replace the accepted-state calls**

In apply.Run, replace ledger.LoadExpected with reviewv2.LoadExpected, pass accepted.Legacy to proposal.Validate, replace ledger.Render with reviewv2.ApplyChangeSet, and replace ledger.SnapshotUsageExpected with reviewv2.SnapshotUsageExpected. Keep the existing prepared/applied receipt sequence, cursor CAS, whole-namespace digest, root identity checks, and recovery behavior unchanged.

If reviewv2.LoadExpected returns ErrMigrationRequired, apply must fail before creating a receipt or changing a cursor and print the recovery instruction to run sync --dry-run followed by sync. It must never continue writing v1 files after a v2-capable binary is installed.

In recovery, load reviewv2.Accepted and pass Accepted.Legacy into the existing validation/ordering functions. Do not parse the visible Markdown a second way. Keep CLI render budgets.

- [ ] **Step 4: Prove receipt recovery across three v2 files**

Add injection coverage after each planned file, after applied receipt publication, and before cursor CAS. For every failure point, rerun the same apply and assert the three v2 files, receipt, and cursor converge; a different proposal at the same boundary remains blocked.

- [ ] **Step 5: Run focused and full dependent tests**

Run: go test ./internal/apply ./internal/recovery ./internal/diagram -count=1

Expected: PASS.

- [ ] **Step 6: Commit**

~~~bash
git add internal/apply internal/recovery internal/diagram
git commit -m "feat: use review v2 for apply and recovery"
~~~

### Task 6: Teach Three-Way Merge Stable v2 Semantic Units

**Files:**
- Modify: internal/syncdoc/document.go
- Modify: internal/syncdoc/body.go
- Create: internal/syncdoc/v2_units.go
- Create: internal/syncdoc/v2_units_test.go
- Modify: internal/syncdoc/scan.go
- Modify: internal/syncdoc/scan_test.go
- Modify: internal/sync/merge.go
- Modify: internal/sync/merge_test.go

**Interfaces:**
- Consumes: Task 2 marker grammar and testdata/review-v2 fixtures.
- Produces: syncdoc.Document semantic units keyed by stable decision/event/risk ID even when human-visible headings change.

- [ ] **Step 1: Write failing aggregate-document merge tests**

~~~go
func TestV2HistoryUnitsSurviveTitleEditAndMergeDifferentEvents(t *testing.T) {
    base := parseFixture(t, "项目历史.valid.md")
    project := patchEventTitle(t, base, "timeline-trust-chain", "新的标题")
    vault := patchEventNext(t, base, "timeline-release", "新的下一步")
    result := sync.Merge(MergeInput{
        EntityID: "project-history", ProjectID: "project-0123456789abcdef",
        Base: &base, BasePath: "项目历史.md",
        Project: candidate("项目历史.md", project),
        Vault: candidate("项目历史.md", vault),
        GOOS: "darwin", CaseMode: platform.CaseSensitive,
        OccupiedPathKeys: map[string]string{},
    })
    if result.Kind != MergeWriteBoth || result.Accepted == nil { t.Fatalf("result=%+v", result) }
    assertEventTitle(t, *result.Accepted, "timeline-trust-chain", "新的标题")
    assertEventNext(t, *result.Accepted, "timeline-release", "新的下一步")
}
~~~

- [ ] **Step 2: Run tests and verify RED**

Run: go test ./internal/syncdoc ./internal/sync -run 'TestV2HistoryUnits|TestV2ReviewUnits' -count=1

Expected: FAIL because current section identity derives from visible headings.

- [ ] **Step 3: Add marker-stable unit extraction**

For entity_type project_review and project_history, make SemanticUnits replace each bounded marker block with one UnitSection key:

~~~go
func v2UnitKey(kind, id string) UnitKey {
    return UnitKey{Kind: UnitSection, Name: "session-reviewer/" + kind + "/" + id}
}
~~~

Keep frontmatter human/machine ownership rules. Use the Task 2 parser to locate exact source slices. WithSemanticUnits must rebuild marker blocks at their original position, preserve unknown top-level sections, and reparse before returning. A heading edit inside a block changes the block value but never its UnitKey.

For v2 documents, sync hashes and live sync status stay in Base/machine state and ledger.json, not visible frontmatter. FinalizeHumanMerge increments only the accepted revision/provenance fields and must not inject sync_status, sync_hash, base_hash, project_hash, or vault_hash into either visible Markdown document; this prevents the document hashes stored in ledger.json from becoming self-referential.

- [ ] **Step 4: Restrict inventory to the two v2 documents**

When schema_version=2 exists, Scan must include only 项目回顾.md and 项目历史.md as ordinary entities and prune the entire .session-reviewer subtree. Mixed v1/v2 inventory is an IssueMalformed result. Exact entity IDs are project-overview and project-history; entity types are project_review and project_history.

- [ ] **Step 5: Verify merge conflict granularity**

Test different events merge, different decisions merge, same event concurrent edit conflicts, reserved frontmatter edit conflicts, marker deletion conflicts, same title edit on both sides converges, and rename away from the two stable filenames is rejected.

- [ ] **Step 6: Run syncdoc and merge tests**

Run: go test ./internal/syncdoc ./internal/sync -run 'TestV2|TestMerge' -count=1

Expected: PASS.

- [ ] **Step 7: Commit**

~~~bash
git add internal/syncdoc internal/sync
git commit -m "feat: merge compact review documents by stable units"
~~~

### Task 7: Integrate Automatic Migration, Hidden Conflicts, and One-Way Machine Publication

**Files:**
- Modify: internal/sync/service.go
- Modify: internal/sync/service_test.go
- Replace responsibility in: internal/sync/derived.go
- Modify: internal/sync/derived_test.go
- Modify: internal/sync/conflict.go
- Modify: internal/sync/conflict_test.go
- Modify: internal/sync/transaction.go
- Modify: internal/sync/transaction_test.go
- Modify: internal/sync/events.go

**Interfaces:**
- Consumes: reviewv2.DetectVersion, PlanMigration, ApplyMigration, MachineLedgerRelativePath, existing Engine transactions.
- Produces: Report.Migration, Report.Machine, hidden JSON conflict records, and deterministic repair semantics for Task 8 and the plugin plan.

- [ ] **Step 1: Write failing end-to-end sync tests**

~~~go
func TestReconcileDryRunPlansMigrationAndRealSyncConvergesV2(t *testing.T) {
    f := newLegacyServiceFixture(t)
    dry, err := f.engine.Reconcile(context.Background(), ReconcileRequest{DryRun:true, Trigger:TriggerCLI})
    if err != nil { t.Fatal(err) }
    if !dry.Migration.Required || !dry.DryRun { t.Fatalf("report=%+v", dry) }
    assertLegacyUnchanged(t, f)

    real, err := f.engine.Reconcile(context.Background(), ReconcileRequest{Trigger:TriggerCLI})
    if err != nil { t.Fatal(err) }
    if real.Migration.Required || real.Machine.State != MachineCurrent { t.Fatalf("report=%+v", real) }
    assertProjectVaultV2Converged(t, f)

    repeat, err := f.engine.Reconcile(context.Background(), ReconcileRequest{DryRun:true, Trigger:TriggerCLI})
    if err != nil || len(repeat.Operations) != 0 || len(repeat.Machine.Operations) != 0 {
        t.Fatalf("repeat=%+v err=%v", repeat, err)
    }
}
~~~

- [ ] **Step 2: Run the focused test and verify RED**

Run: go test ./internal/sync -run TestReconcileDryRunPlansMigrationAndRealSyncConvergesV2 -count=1

Expected: FAIL because Report has no Migration or Machine fields.

- [ ] **Step 3: Add migration as the first reconcile gate**

~~~go
type MigrationReport struct {
    Required bool `json:"required"`
    DryRun bool `json:"dry_run"`
    Creates []string `json:"creates"`
    Archives []string `json:"archives"`
}
type MachinePublishState string
const (
    MachineCurrent MachinePublishState = "current"
    MachinePending MachinePublishState = "pending"
    MachineBlocked MachinePublishState = "blocked"
)
type MachineReport struct {
    State MachinePublishState `json:"state"`
    Operations []Operation `json:"operations"`
}
~~~

Recover any migration journal before entity transactions. For v1: dry-run returns the complete migration plan without writes; normal CLI trigger migrates under the project lock, reloads v2, then continues reconcile. Watcher/periodic triggers return migration_required without writing.

- [ ] **Step 4: Replace derived navigation publication**

Remove navigation artifact generation from the active v2 path. Project ledger.json is canonical. If Vault ledger is missing, plan add_vault; if hashes match, current; if Vault differs, return machine_ledger_modified and do not overwrite. Add an explicit Engine.RepairMachineLedger method that revalidates Project ledger and writes only the Vault canonical copy through CAS and transaction recovery.

Update LastSuccessfulSync only when a real entity/migration/repair operation commits. Create the next Project ledger bytes once, atomically write them, publish those exact bytes to Vault in the same machine transaction, and verify both hashes. A no-op sync reuses the existing timestamp and bytes so repeated status/dry-run cannot churn ledger.json.

- [ ] **Step 5: Hide conflict records**

Serialize ConflictRecord as content-bounded JSON under .session-reviewer/conflicts/<id>.json in Project and Vault. Do not include candidate contents in the public status output. Keep candidate bytes in the hidden record only after redaction checks. Existing resolve actions continue to validate live Project/Vault hashes and stale conflict identity before writing.

- [ ] **Step 6: Verify security and recovery**

Test machine-ledger Vault tampering blocks normal sync, explicit repair restores it, concurrent replacement is not overwritten, hidden conflict paths are pruned from ordinary inventory/events, crash recovery converges, and diagnostics do not contain candidate text or absolute roots.

- [ ] **Step 7: Run sync and race tests**

Run: go test ./internal/sync -count=1

Run: go test -race ./internal/sync -run 'TestReconcileDryRunPlansMigration|TestMachineLedger|TestHiddenConflict' -count=1

Expected: PASS for both commands.

- [ ] **Step 8: Commit**

~~~bash
git add internal/sync
git commit -m "feat: migrate and sync compact review state"
~~~

### Task 8: Update Init, CLI, Skill, Packaging, and Real Acceptance

**Files:**
- Modify: internal/project/init.go
- Modify: internal/project/init_test.go
- Modify: internal/cli/run.go
- Modify: internal/cli/sync.go
- Modify: internal/cli/sync_test.go
- Modify: skill/session-reviewer/SKILL.md
- Modify: README.md
- Modify: .github/workflows/ci.yml
- Modify: scripts/build-release.sh
- Modify: scripts/build-release.ps1
- Modify: cmd/release-packager/main.go
- Modify: cmd/release-packager/main_test.go
- Create: docs/release/acceptance-review-v2-core.md

**Interfaces:**
- Consumes: all Task 1–7 interfaces.
- Produces: public v2 init/sync/status/repair behavior, updated Skill workflow, packaged schema, and acceptance evidence required before plugin implementation.

- [ ] **Step 1: Write failing CLI acceptance tests**

~~~go
func TestRunSyncReportsMigrationAndRepairsMachineLedger(t *testing.T) {
    f := newCLILegacyFixture(t)
    code, out, errOut := f.run("sync", "--dry-run")
    if code != 0 || !strings.Contains(out, "migration=required") || errOut != "" {
        t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
    }
    code, out, errOut = f.run("sync")
    if code != 0 || !strings.Contains(out, "machine=current") || errOut != "" {
        t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
    }
    f.tamperVaultLedger()
    code, _, errOut = f.run("sync")
    if code == 0 || !strings.Contains(errOut, "machine_ledger_modified") { t.Fatalf("code=%d err=%q", code, errOut) }
    code, _, errOut = f.run("sync", "repair-machine-ledger")
    if code != 0 || errOut != "" { t.Fatalf("code=%d err=%q", code, errOut) }
}
~~~

- [ ] **Step 2: Run CLI tests and verify RED**

Run: go test ./internal/cli ./internal/project -run 'TestRunSyncReportsMigration|TestInitCreatesReviewV2' -count=1

Expected: FAIL because init/help/reporting/repair still expose v1 behavior.

- [ ] **Step 3: Implement exact CLI and init behavior**

New init --write creates two visible Markdown files and ledger.json. Sync output ends with:

~~~text
migration=current
operations: 0
conflicts: 0
issues: 0
errors: 0
queue_depth: 0
machine=current files=1
~~~

Add session-reviewer sync repair-machine-ledger with no arbitrary path arguments. JSON status includes migration, machine state, last successful sync, pending operations, and hidden conflict IDs.

Add --project-id to sync, status, resolve, repair-machine-ledger, and dry-run as a mutually exclusive alternative to --cwd. resolveSyncMapping loads the local config, selects exactly one stable project ID, then pins and verifies that configured Project root; this is the only plugin-facing way to locate the code repository, so no absolute Project path enters Vault Markdown or ledger.json.

- [ ] **Step 4: Update Skill and user documentation**

Replace multi-file navigation instructions with: open the project evolution browser when installed; otherwise read 项目回顾.md then 项目历史.md. Document which fields are human editable, how code-side edits reach Obsidian, migration dry-run/backup behavior, machine-ledger repair, cost calculation, and Windows PowerShell examples.

- [ ] **Step 5: Package schemas and run reproducibility tests**

Include schemas/review-ledger-v2.schema.json and test the exact source/release archive path and checksum on macOS shell and PowerShell packaging scripts. Update CI to run migration-focused tests twice and cross-build Windows.

- [ ] **Step 6: Run the complete local gate**

Run: go test ./...

Run: go test -race ./... -skip '^TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB$'

Run: go vet ./...

Run: GOOS=windows GOARCH=amd64 go test ./internal/reviewv2 ./internal/sync ./internal/cli -run '^$'

Run: GOOS=darwin GOARCH=amd64 go test ./internal/reviewv2 ./internal/sync ./internal/cli -run '^$'

Expected: every command exits 0 with no test failure or vet diagnostic.

- [ ] **Step 7: Perform real Project/Vault migration acceptance**

Record sanitized evidence in docs/release/acceptance-review-v2-core.md:

1. Back up current configured Project and Vault.
2. Run session-reviewer sync --dry-run --cwd <real-project> and record migration creates/archives with zero writes.
3. Run session-reviewer sync --cwd <real-project>.
4. Confirm normal Project and Vault directories each show only 项目回顾.md and 项目历史.md.
5. Confirm Project hidden backup manifest and hashes; confirm no backup in Vault.
6. Run sync status --json, repeat sync --dry-run, and a recursive Project/Vault comparison; require zero pending/conflict/malformed/blocked and byte-identical two Markdown plus ledger.json.
7. Edit different semantic units on Project and Vault, sync, and verify both changes converge.
8. Edit the same unit on both sides, verify hidden conflict, resolve each action in isolated repetitions, and return to zero pending state.

- [ ] **Step 8: Commit**

~~~bash
git add internal/project internal/cli skill/session-reviewer README.md .github/workflows/ci.yml scripts cmd/release-packager docs/release/acceptance-review-v2-core.md
git commit -m "feat: complete review v2 core migration"
~~~

## Final Core Verification

- [ ] Run git diff --check against the complete implementation range.
- [ ] Run go test ./..., race, vet, Windows cross-build, macOS cross-build, package reproducibility, and the real Project/Vault acceptance again after the final review fix.
- [ ] Confirm git status contains no generated binary, temporary Vault, migration backup, or unrelated pre-existing user change.
- [ ] Confirm proposal-v1 remains accepted and no raw Session, high-entropy token, absolute private path, or hidden reasoning entered the v2 human documents.
- [ ] Confirm repeat apply, sync, status, and dry-run converge without byte, hash, revision, or modification-time drift.
