# Zero-token Gate B Projection and Publication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn a prepared Gate-A ProjectView into schema-v3 human-editable Project/Vault Markdown and publish it through a durable cross-root journal, then expose one zero-token `更新项目脉络` action in Obsidian.

**Architecture:** The existing `reviewv2` package remains the compatibility package but gains explicit v2 read-only and v3 read/write branches. A presentation layer captures field-level human patches, rebases them over deterministic ProjectView defaults, and preserves unknown Markdown bytes. A publication service uses preimage CAS, existing Project/Vault sync, post-write verification, and a durable private journal before switching `published_generation`.

**Tech Stack:** Go 1.26, existing review Markdown/parser and sync engine, TypeScript 5.8, Obsidian 1.13, Vitest/jsdom, ESLint, esbuild, rooted atomic writes on macOS and Windows.

**Spec:** `docs/superpowers/specs/2026-08-31-zero-token-session-memory-analysis-design.md`

## Global Constraints

- Gate A must be complete. Gate B consumes only validated prepared generations; it never decodes raw Sessions itself.
- Public projection schema is exactly `3` and declares `minimum_writer_version: 0.3.0`.
- A writer below 0.3.0 or unable to parse schema 3 fails before mutation; no code may rewrite v3 as v2 or strip unknown blocks.
- Presentation precedence is `HumanPresentation > valid AgentAnnotation > deterministic ProjectView`; Gate B has no AgentAnnotation producer, so the effective first-release precedence is human over deterministic.
- Human patches are field-level `set`, `suppress`, or `restore_default`; human values remain highest in visible output.
- Unknown custom Markdown blocks and unsupported fields are byte-preserved.
- A changed generated baseline keeps a patch with `underlay_changed`; a replaced entity retains an unattached `orphan_patch`.
- Private store, Project, and Vault do not share a filesystem transaction. A generation becomes published only after all required outputs verify the same generation and hashes.
- Crash rollback is compare-and-swap and never restores over a human edit made after the journaled write.
- Obsidian remains a concise presentation/editor. It must not expose ObservationStore internals, source paths, raw diagnostics, or Agent configuration.
- The primary action label is exactly `更新项目脉络`; it consumes zero review-run model tokens.
- Gate B performs no v2-to-v3 real-project migration, AgentWiki acceptance, release, tag, push, marketplace submission, deployment, or installation into the user's real Obsidian Vault. Its built bundle is installed only in a disposable test Vault.
- Preserve unrelated worktree changes and stage only files named by the active task.

---

## File Structure and Ownership

- Modify `internal/reviewv2/*`: keep v2 read-only compatibility while adding strict schema-v3 parsing/rendering and minimum-writer checks.
- Create `schemas/review-projection-v3.schema.json`: exact public machine projection contract.
- Create `internal/presentation/patch.go`: capture, validate, rebase, suppress, restore, and orphan human patches.
- Create `internal/presentation/project.go`: merge ProjectView defaults, legacy presentation, and human patches.
- Create `internal/presentation/render.go`: produce all three public projection files and exact preimages.
- Create `internal/publication/journal.go`: durable cross-root intent and recovery codec.
- Create `internal/publication/service.go`: Project CAS, Vault sync, verification, rollback, and published pointer switch.
- Create `internal/contextupdate/service.go`: freeze human state, invoke Gate A, render, and publish.
- Create `internal/scanjob/*`: durable zero-token scan start/status/worker control plane.
- Modify `internal/sync/*` and `internal/syncproject/*`: schema-v3 machine projection and generation-aware verification.
- Modify `internal/cli/scan.go` and `run.go`: public foreground plus durable start/status commands.
- Modify plugin contracts/repository/runner/view code: schema 3, fixed scan argv, `更新项目脉络`, and field operations.
- Add Gate B fixture replay under `test/zerotoken/`.

---

### Task 1: Add Fail-closed Public Projection Schema v3

**Files:**
- Create: `schemas/review-projection-v3.schema.json`
- Modify: `internal/reviewv2/types.go`
- Modify: `internal/reviewv2/load.go`
- Modify: `internal/reviewv2/markers.go`
- Modify: `internal/reviewv2/review_markdown.go`
- Modify: `internal/reviewv2/history_markdown.go`
- Modify: `internal/reviewv2/ledger_json.go`
- Modify: `internal/reviewv2/load_test.go`
- Modify: `internal/reviewv2/markdown_test.go`
- Modify: `internal/reviewv2/ledger_json_test.go`
- Modify: `internal/buildinfo/buildinfo.go`
- Modify: `internal/buildinfo/buildinfo_test.go`

**Interfaces:**
- Consumes: existing v2 document grammar and `memory.ProjectView` digest/generation IDs.
- Produces: `reviewv2.VersionV3`, v3 `MachineLedger`, `LoadV3`, v2 read-only loading, and `ErrWriterUpgradeRequired`. The v3 ledger carries canonical generated baselines, active/restore patches, orphan patches, and legacy presentation classifications; it remains a public projection artifact, not the machine-fact authority.

- [ ] **Step 1: Write failing version and downgrade tests**

```go
func TestSchemaV3RequiresMinimumWriterAndGeneration(t *testing.T) {
    body := renderV3Fixture(t)
    accepted, err := LoadV3Fixture(body)
    if err != nil { t.Fatal(err) }
    if accepted.State.Machine.MinimumWriterVersion != "0.3.0" || accepted.State.Machine.GenerationID == "" { t.Fatalf("%+v", accepted.State.Machine) }
}

func TestV2WriterFailsClosedBeforeMutatingV3(t *testing.T) {
    before := snapshotTree(t, projectRoot)
    err := runLegacyWritePath(projectRoot)
    if !errors.As(err, new(*ErrWriterUpgradeRequired)) { t.Fatalf("%v", err) }
    assertTreeEqual(t, before, snapshotTree(t, projectRoot))
}
```

Also assert missing/duplicate `minimum_writer_version`, unknown machine fields, schema 4, generated-baseline/generation mismatch, duplicate patch/entity keys, invalid patch operations, baseline-hash mismatch, and semver below 0.3.0 fail without writes. A valid supported human field edit or unknown custom block must load as presentation input rather than being mistaken for machine-hash corruption.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/reviewv2 ./internal/buildinfo -count=1`

Expected: FAIL because schema v3 and writer-version fields do not exist.

- [ ] **Step 3: Implement v3 identity without destroying v2 readability**

Set `SchemaVersion = 3`, add `LegacySchemaVersion = 2`, `MinimumWriterVersion = "0.3.0"`, and `VersionV3`. Extend frontmatter for both human files with exact `minimum_writer_version` and `generation_id`. Extend machine projection with:

```go
type MachineLedger struct {
    SchemaVersion int `json:"schema_version"`
    MinimumWriterVersion string `json:"minimum_writer_version"`
    ProjectID string `json:"project_id"`
    GenerationID string `json:"generation_id"`
    ProjectViewDigest string `json:"project_view_digest"`
    AcceptedRevision int `json:"accepted_revision"`
    ReviewSHA256 string `json:"review_sha256"`
    HistorySHA256 string `json:"history_sha256"`
    LastSuccessfulSync string `json:"last_successful_sync,omitempty"`
    Accounting accounting.ProjectSummary `json:"accounting"`
    Sessions []ledger.SessionReport `json:"sessions"`
    HumanPatches []HumanPatchWire `json:"human_patches"`
    GeneratedBaselines []GeneratedBaselineWire `json:"generated_baselines"`
    LegacyCompatibility LegacyCompatibility `json:"legacy_compatibility"`
}
```

Keep v2 `LoadAnyReadOnly` for Gate C migration. Every mutating API requires v3. `buildinfo.Current().ReviewSchemaVersion` becomes `3`; release validation requires 3.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/reviewv2 internal/buildinfo && go test ./internal/reviewv2 ./internal/buildinfo -count=1`

Expected: PASS; v2 reads remain available only through explicit read-only APIs, while all v3 writers require 0.3.0.

```bash
git add schemas/review-projection-v3.schema.json internal/reviewv2 internal/buildinfo
git commit -m "feat: add fail-closed review projection v3"
```

---

### Task 2: Capture and Rebase HumanPresentationPatch

**Files:**
- Create: `internal/presentation/patch.go`
- Create: `internal/presentation/patch_test.go`
- Create: `internal/presentation/baseline.go`
- Create: `internal/presentation/baseline_test.go`

**Interfaces:**
- Consumes: parsed current Markdown, previous generated baselines, and next deterministic field/entity set.
- Produces: `presentation.Patch`, `Baseline`, `Capture`, `Rebase`, `Apply`, `UnderlayChanged`, and `OrphanPatch`.

- [ ] **Step 1: Write failing set/suppress/restore/orphan tests**

```go
func TestRebasePreservesHumanSetWhenGeneratedValueChanges(t *testing.T) {
    patch := Patch{EntityID: "project-overview", Field: "status", Operation: Set, Value: "人工结论", BaseGeneratedHash: hash("old")}
    result, err := Rebase([]Patch{patch}, nextBaselines("status", "new"))
    if err != nil { t.Fatal(err) }
    if result.Active[0].Value != "人工结论" || result.Diagnostics[0].Code != UnderlayChanged { t.Fatalf("%+v", result) }
}

func TestDeletedEntityRetainsUnattachedOrphan(t *testing.T) {
    result, err := Rebase([]Patch{decisionPatch("decision-old")}, noDecisionBaselines())
    if err != nil || len(result.Orphans) != 1 || len(result.Active) != 0 { t.Fatalf("%+v %v", result, err) }
}
```

Also test intentionally empty `set`, explicit `suppress`, equal-to-baseline `restore_default`, duplicate patch rejection, unsupported fields, changed field contracts, stable ordering, and unknown Markdown bytes before/between/after controlled blocks.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/presentation -run 'Test(Capture|Rebase|Apply)' -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement exact patch records and operations**

```go
type Operation string
const ( Set Operation = "set"; Suppress Operation = "suppress"; RestoreDefault Operation = "restore_default" )
type Patch struct {
    EntityID string `json:"entity_id"`
    Field string `json:"field"`
    Operation Operation `json:"operation"`
    Value string `json:"value,omitempty"`
    Values []string `json:"values,omitempty"`
    BaseGeneratedHash string `json:"base_generated_hash"`
}
type RebaseResult struct { Active []Patch; Orphans []Patch; Diagnostics []Diagnostic }
```

`Capture` starts from the prior v3 ledger's canonical patches and generated baselines, then compares parsed supported fields/entities to the exact prior rendered values. A changed value becomes `set`, an intentionally removed prior generated entity/optional section becomes `suppress`, and an explicit restore action becomes `restore_default`. Unchanged human values keep their existing patches even if the new generated baseline differs. Unsupported custom blocks are retained in a byte-slice preservation map and are never converted into patches.

The current patch set, orphan set, baseline values/hashes, and diagnostics are serialized in the schema-v3 ledger and authenticated by its projection hash. Controlled Markdown blocks also carry hidden stable entity/field identity plus explicit `suppress` or `restore_default` intent markers, so one human edit does not require a cross-file transaction. Markdown is the human editing surface and highest presentation authority; the next scan reconciles those bytes/markers against the ledger baseline and publishes the updated canonical patch metadata.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/presentation && go test ./internal/presentation -run 'Test(Capture|Rebase|Apply)' -count=20`

Expected: PASS; reordered map inputs produce identical patch wire order.

```bash
git add internal/presentation/patch.go internal/presentation/patch_test.go internal/presentation/baseline.go internal/presentation/baseline_test.go
git commit -m "feat: preserve human presentation patches"
```

---

### Task 3: Project Deterministic Views into Concise Human Documents

**Files:**
- Create: `internal/presentation/project.go`
- Create: `internal/presentation/project_test.go`
- Create: `internal/presentation/render.go`
- Create: `internal/presentation/render_test.go`
- Modify: `internal/reviewv2/project.go`
- Modify: `internal/reviewv2/project_test.go`

**Interfaces:**
- Consumes: `memory.ProjectView`, current v3/v2-read-only documents, rebased patches, and safe legacy presentation.
- Produces: `presentation.Project(Input) (Output, error)` and `presentation.RenderPlan` containing three desired files plus exact preimages.

- [ ] **Step 1: Write failing precedence and concise-output tests**

Tests prove human status/goal/next action wins over deterministic defaults; suppressed items stay hidden; orphan patches are recoverable diagnostics but not attached; no Agent-only section is invented; review/history remain within 4 MiB each; usage is labeled associated/shared; and unknown custom Markdown blocks remain byte-identical.

Run: `go test ./internal/presentation ./internal/reviewv2 -run 'Test(Project|RenderV3|Human)' -count=1`

Expected: FAIL with missing projector.

- [ ] **Step 2: Implement the fixed first-release projection**

Project only these visible sections: project review, current status/next action, selected deterministic recent progress, human/legacy decisions and risks, project history/evolution, and associated model/token usage. Omit empty semantic sections. Use stable entity IDs from ProjectView or legacy presentation. Do not infer causal rationale, project goal, or next action from command/file frequency.

- [ ] **Step 3: Produce a three-file CAS plan**

```go
type FilePlan struct { Relative string; Expected []byte; ExpectedExists bool; Desired []byte; Mode fs.FileMode }
type RenderPlan struct { ProjectID string; GenerationID string; ProjectViewDigest string; Files []FilePlan; Patches []Patch; Baselines []Baseline }
```

Render review and history first, hash their exact bytes, then render `.session-reviewer/ledger.json` with generated baselines, active/restore/orphan patches, and legacy classifications. Parse all desired bytes again and verify project ID, generation, revision, hashes, patch ordering, baseline hashes, and minimum writer version before returning. Never write from this package.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/presentation internal/reviewv2 && go test ./internal/presentation ./internal/reviewv2 -count=1`

Expected: PASS; rendering twice with unchanged inputs is byte-identical.

```bash
git add internal/presentation internal/reviewv2/project.go internal/reviewv2/project_test.go
git commit -m "feat: project zero-token views into editable markdown"
```

---

### Task 4: Add a Durable Cross-root Publication Journal

**Files:**
- Create: `internal/publication/types.go`
- Create: `internal/publication/journal.go`
- Create: `internal/publication/journal_test.go`
- Modify: `internal/memorystore/store.go`
- Modify: `internal/memorystore/store_test.go`

**Interfaces:**

```go
type Stage string

const (
    StagePrepared         Stage = "prepared"
    StageProjectWritten   Stage = "project_written"
    StageVaultSynced      Stage = "vault_synced"
    StageVerified         Stage = "verified"
    StageCommitted        Stage = "committed"
    StageRollbackRequired Stage = "rollback_required"
)

type Destination struct {
    Side            string `json:"side"`
    Relative        string `json:"relative"`
    PreimageSHA256  string `json:"preimage_sha256,omitempty"`
    DesiredSHA256   string `json:"desired_sha256"`
    PreimageExists  bool   `json:"preimage_exists"`
}

type Intent struct {
    Version           int           `json:"version"`
    ProjectID         string        `json:"project_id"`
    GenerationID      string        `json:"generation_id"`
    ManifestDigest    string        `json:"manifest_digest"`
    ProjectViewDigest string        `json:"project_view_digest"`
    Stage             Stage         `json:"stage"`
    CreatedAt         time.Time     `json:"created_at"`
    Destinations      []Destination `json:"destinations"`
}

func OpenJournal(dataRoot, projectID string) (*Journal, error)
func (j *Journal) Create(Intent) error
func (j *Journal) Load() (Intent, error)
func (j *Journal) Advance(expected, next Stage) error
func (j *Journal) Recover(ctx context.Context, h RecoveryHandler) error
func (s *Store) CommitPublished(generationID string, proof PublicationProof) error
```

- [ ] **Step 1: Write failing codec, permission, and stage-CAS tests**

Test duplicate JSON keys, unknown fields, unsupported journal versions, invalid project/generation IDs, unsorted or duplicate destinations, symlink components, non-private directories/files, creation over an active intent, and `Advance` with a stale expected stage. Assert every rejected input leaves the existing journal bytes unchanged.

Run: `go test ./internal/publication ./internal/memorystore -run 'Test(Journal|CommitPublished)' -count=1`

Expected: FAIL because the publication journal and proof-gated pointer switch do not exist.

- [ ] **Step 2: Implement the private journal and transition table**

Store one canonical JSON intent below the project-scoped private memory directory using `pathguard` plus `atomicfile`; require `0700` directories and `0600` files. Permit only:

```text
prepared -> project_written -> vault_synced -> verified -> committed
    |              |                |
    +--------------+----------------+-> rollback_required
```

Reject every skipped, repeated, or backward transition except idempotent recovery of a byte-identical already-applied transition. Keep exact preimage bytes in a separate private, hash-addressed journal payload so rollback does not depend on mutable Project/Vault state.

- [ ] **Step 3: Require verified publication proof before switching the pointer**

`CommitPublished` must verify that the prepared manifest, ProjectView digest, generation ID, three public-file hashes, and `StageVerified` journal all agree. Switch `published_generation` atomically, advance to `committed`, and leave the previous published generation reachable for retention. A missing/corrupt proof returns `ErrPublicationProofInvalid` without changing either pointer.

- [ ] **Step 4: Add crash-boundary recovery tests**

Inject a crash after every atomic journal/payload/pointer write. Reopen the store and prove recovery reaches either the old fully published generation or the new fully published generation, never a mixed pointer. Repeat recovery twice to prove idempotence.

- [ ] **Step 5: Run GREEN and commit**

Run: `gofmt -w internal/publication internal/memorystore && go test ./internal/publication ./internal/memorystore -count=20`

Expected: PASS with identical journal bytes and terminal state across repeated runs.

```bash
git add internal/publication/types.go internal/publication/journal.go internal/publication/journal_test.go internal/memorystore/store.go internal/memorystore/store_test.go
git commit -m "feat: journal cross-root publication"
```

---

### Task 5: Publish Project and Vault with CAS, Verification, and Recovery

**Files:**
- Create: `internal/publication/service.go`
- Create: `internal/publication/service_test.go`
- Create: `internal/publication/recovery_test.go`
- Modify: `internal/sync/types.go`
- Modify: `internal/sync/derived.go`
- Modify: `internal/sync/service.go`
- Modify: `internal/sync/service_test.go`
- Modify: `internal/syncproject/service.go`
- Modify: `internal/syncproject/service_test.go`

**Interfaces:**

```go
type Options struct {
    ProjectID          string
    PreparedGeneration string
    Plan               presentation.RenderPlan
    Mapping            config.ProjectMapping
    DataRoot           string
    Now                func() time.Time
}

type Result struct {
    GenerationID string
    ProjectFiles []VerifiedFile
    VaultFiles   []VerifiedFile
    Recovered    bool
}

func Publish(ctx context.Context, opts Options) (Result, error)
```

- [ ] **Step 1: Write failing success, conflict, and recovery tests**

Cover a clean publish, Project preimage changed before the first write, Vault human edit between Project write and sync, crash after each journal stage, corrupt destination after sync, missing destination, symlink swap, and two concurrent publishers. Assert conflicts return typed `ErrPublicationConflict` with side/path/expected/actual hashes and never overwrite the human edit.

Run: `go test ./internal/publication ./internal/sync ./internal/syncproject -run 'Test(Publish|RecoverPublication|SchemaV3)' -count=1`

Expected: FAIL because only the journal exists.

- [ ] **Step 2: Make schema-v3 sync generation-aware**

Teach `sync` and `syncproject` to validate schema 3, `minimum_writer_version`, project ID, generation ID, ProjectView digest, and review/history hashes as one machine projection. Preserve the existing byte-safe human merge and rooted writer. Refuse schema 2 writes through the new path and reject mixed-generation source files before computing operations.

- [ ] **Step 3: Implement the publication state machine**

Acquire the existing per-project lock only after extraction/reduction is finished. Re-read and compare all frozen preimages, create the intent, CAS-write the three Project files, advance `project_written`, invoke `syncproject.Run`, advance `vault_synced`, then parse and hash all six Project/Vault outputs. Require the same generation and machine hashes on both sides before `verified`; only then call `CommitPublished`.

- [ ] **Step 4: Implement conservative recovery and rollback**

On startup or before a new publish, recover an active journal. Complete forward only when every observed byte is either its recorded preimage or desired image. Otherwise CAS-restore each destination still equal to the journaled desired bytes and mark `rollback_required`; never restore a file whose bytes changed after publication. Surface such a file as a human-edit conflict with exact recovery guidance.

- [ ] **Step 5: Prove concurrency and repeated-run behavior**

Run 50 concurrent publisher pairs against the same project and assert exactly one generation commits. Publish the same generation twice and assert the second call performs zero writes and returns the first verified hashes.

- [ ] **Step 6: Run GREEN and commit**

Run: `gofmt -w internal/publication internal/sync internal/syncproject && go test ./internal/publication ./internal/sync ./internal/syncproject -count=20 && go test -race ./internal/publication ./internal/sync ./internal/syncproject`

Expected: PASS; race detector reports no races.

```bash
git add internal/publication internal/sync internal/syncproject
git commit -m "feat: publish zero-token project generations"
```

---

### Task 6: Orchestrate Context Updates as Durable Scan Jobs

**Files:**
- Create: `internal/contextupdate/service.go`
- Create: `internal/contextupdate/service_test.go`
- Create: `internal/scanjob/types.go`
- Create: `internal/scanjob/store.go`
- Create: `internal/scanjob/store_test.go`
- Create: `internal/scanjob/service.go`
- Create: `internal/scanjob/service_test.go`
- Modify: `internal/cli/scan.go`
- Modify: `internal/cli/scan_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**

```text
session-reviewer scan --project-id <id> [--sessions-root <path>] [--data-dir <path>] --json
session-reviewer scan start --project-id <id> [--data-dir <path>] --json
session-reviewer scan status --project-id <id> [--data-dir <path>] --json
session-reviewer scan worker --job-id <id> --data-dir <path>
```

```go
type PublicStatus struct {
    SchemaVersion int    `json:"schema_version"`
    JobID         string `json:"job_id"`
    ProjectID     string `json:"project_id"`
    State         string `json:"state"` // queued|running|completed|completed_with_issues|failed
    Phase         string `json:"phase"` // discovering|extracting|reducing|rendering|syncing
    SessionCount  int    `json:"session_count"`
    IndexedCount  int    `json:"indexed_count"`
    IssueCount    int    `json:"issue_count"`
    GenerationID string `json:"generation_id,omitempty"`
    ErrorCode     string `json:"error_code,omitempty"`
}
```

- [ ] **Step 1: Write failing orchestration and CLI contract tests**

Assert foreground scan calls Gate A, presentation, and publication in order; `start` returns a durable job before work; `status` emits only the public fields above; one project cannot run two jobs; malformed IDs/unknown flags/missing mapping fail before a worker starts; and root help exposes `scan` without exposing `worker`.

Run: `go test ./internal/contextupdate ./internal/scanjob ./internal/cli -run 'Test(ContextUpdate|Scan)' -count=1`

Expected: FAIL with missing packages/command.

- [ ] **Step 2: Freeze the source and human boundaries**

At job start, record SourceCatalog revision, candidate source sizes/mtimes, current public preimage hashes, and human-patch digest. Extraction may run without the Project publication lock. Before rendering, reject or restart once if source boundaries changed; before publishing, rebase on the latest human bytes and fail with `E_HUMAN_CONCURRENT_EDIT` if they change again after the render plan freezes.

- [ ] **Step 3: Implement durable job lifecycle and detached worker**

Persist job records below the private project directory with `0700/0600` permissions and strict JSON decoding. Reuse the existing platform-specific detached-process pattern from `reviewjob`, but pass only the fixed worker argv above. On process restart, resume from a validated prepared generation or active publication journal; never repeat already committed writes.

- [ ] **Step 4: Connect foreground and asynchronous paths**

Both paths call one `contextupdate.Service`; neither imports an Agent launcher, prompt builder, API client, or model setting. Map typed extraction issues to `completed_with_issues`; reserve `failed` for an unusable ProjectView or publication failure. Keep issue details in the private store and expose only count/code in public status.

- [ ] **Step 5: Run GREEN and commit**

Run: `gofmt -w internal/contextupdate internal/scanjob internal/cli && go test ./internal/contextupdate ./internal/scanjob ./internal/cli -count=20 && go test -race ./internal/contextupdate ./internal/scanjob`

Expected: PASS; a second unchanged foreground scan reports the same generation and zero destination writes.

```bash
git add internal/contextupdate internal/scanjob internal/cli/scan.go internal/cli/scan_test.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: add durable zero-token scan command"
```

---

### Task 7: Move the Obsidian Data Boundary to Schema 3 and Scan Jobs

**Files:**
- Create: `obsidian-plugin/src/contracts/review-v3.ts`
- Delete: `obsidian-plugin/src/contracts/review-v2.ts`
- Modify: `obsidian-plugin/src/data/ledger.ts`
- Modify: `obsidian-plugin/src/data/markdown.ts`
- Modify: `obsidian-plugin/src/data/repository.ts`
- Modify: `obsidian-plugin/src/cli/discovery.ts`
- Modify: `obsidian-plugin/src/cli/runner.ts`
- Modify: `obsidian-plugin/src/main.ts`
- Modify: `obsidian-plugin/tests/contracts.test.ts`
- Modify: `obsidian-plugin/tests/markdown.test.ts`
- Modify: `obsidian-plugin/tests/repository.test.ts`
- Modify: `obsidian-plugin/tests/discovery.test.ts`
- Modify: `obsidian-plugin/tests/cli.test.ts`
- Modify: `obsidian-plugin/tests/main.test.ts`

**Interfaces:**

```ts
export interface DiscoveredRuntime { runner: SessionReviewerRunner }
export interface ScanStatus {
  schema_version: 1
  job_id: string
  project_id: string
  state: 'queued' | 'running' | 'completed' | 'completed_with_issues' | 'failed'
  phase: 'discovering' | 'extracting' | 'reducing' | 'rendering' | 'syncing'
  session_count: number
  indexed_count: number
  issue_count: number
  generation_id?: string
  error_code?: string
}
```

- [ ] **Step 1: Write failing schema and runtime-discovery tests**

Require schema 3, `minimum_writer_version >= 0.3.0`, matching generation/hash fields, and shared/associated usage fields. Assert schema 2, schema 4, duplicate keys, unknown machine fields, and a binary below 0.3.0 produce an upgrade state without mutating files. Assert discovery succeeds without Codex or any Agent executable.

Run: `cd obsidian-plugin && npm test -- --run tests/contracts.test.ts tests/markdown.test.ts tests/repository.test.ts tests/discovery.test.ts tests/cli.test.ts tests/main.test.ts`

Expected: FAIL because the plugin still consumes v2 and discovers an Agent.

- [ ] **Step 2: Replace v2 imports and enforce fail-closed loading**

Move all plugin machine contracts to `review-v3.ts`, delete the v2 module after no imports remain, and make repository discovery admit only internally consistent v3 triplets. Preserve unknown Markdown blocks as opaque byte slices. Keep associated and shared usage separate through parsing and rendering.

- [ ] **Step 3: Reduce runtime discovery to SessionReviewer only**

Remove Agent path/configuration from `DiscoveredRuntime`, startup gating, and the one-time `codexPath` legacy migration. Preserve one-time `cliPath` cleanup only if old plugin data still contains it. Verify the candidate executable with `--version --json`, exact product identity, review schema 3, and semantic version at least 0.3.0.

- [ ] **Step 4: Add fixed scan command wrappers**

Replace the Agent review methods with `startScan(projectID)` and `getScanStatus(projectID)`. Construct argv arrays internally from validated IDs, reject newline/NUL/path-like IDs, cap stdout/stderr, enforce existing timeouts, and strict-parse one JSON object. Retain the existing fixed sync status/resolve methods for explicit human conflicts; no caller may append arbitrary flags or select an executable.

- [ ] **Step 5: Run GREEN and commit**

Run: `cd obsidian-plugin && npm test -- --run tests/contracts.test.ts tests/markdown.test.ts tests/repository.test.ts tests/discovery.test.ts tests/cli.test.ts tests/main.test.ts && npm run check`

Expected: PASS; `rg -n 'agentExecutable|review-v2|Codex.*path|agent.*setting' src` returns no active runtime dependency.

```bash
git add obsidian-plugin/src/contracts obsidian-plugin/src/data obsidian-plugin/src/cli obsidian-plugin/src/main.ts obsidian-plugin/tests/contracts.test.ts obsidian-plugin/tests/markdown.test.ts obsidian-plugin/tests/repository.test.ts obsidian-plugin/tests/discovery.test.ts obsidian-plugin/tests/cli.test.ts obsidian-plugin/tests/main.test.ts
git commit -m "feat: consume zero-token schema v3 in obsidian"
```

---

### Task 8: Make `更新项目脉络` the Single Obsidian Update Path

**Files:**
- Modify: `obsidian-plugin/src/view/render-shell.ts`
- Modify: `obsidian-plugin/src/view/project-view.ts`
- Modify: `obsidian-plugin/src/view/status-banner.ts`
- Modify: `obsidian-plugin/src/view/edit-modal.ts`
- Modify: `obsidian-plugin/src/data/editor.ts`
- Modify: `obsidian-plugin/styles.css`
- Modify: `obsidian-plugin/tests/view.test.ts`
- Modify: `obsidian-plugin/tests/review-job-view.test.ts`
- Modify: `obsidian-plugin/tests/editor.test.ts`
- Modify: `obsidian-plugin/tests/main.test.ts`
- Modify: `obsidian-plugin/tests/styles.test.ts`
- Modify: `obsidian-plugin/tests/accessibility.test.ts`

- [ ] **Step 1: Write failing action, progress, and edit-precedence tests**

Assert the sole primary action says `更新项目脉络`; click calls `scan start`, polls `scan status`, and never invokes an Agent/review command. Verify progress text for all five phases, reload-safe polling, disabled duplicate start, and terminal messages `项目脉络已更新 · N 个 Session` or `项目脉络已更新 · N 个 Session · I 已索引 · M 需检查`.

Also test field-level set, suppress, and restore-default operations; a subsequent generation must keep the human value/suppression, while restore-default reveals the new deterministic value. Unknown custom Markdown remains byte-identical.

Run: `cd obsidian-plugin && npm test -- --run tests/view.test.ts tests/review-job-view.test.ts tests/editor.test.ts tests/main.test.ts tests/styles.test.ts tests/accessibility.test.ts`

Expected: FAIL because the current header starts Agent review and the editor has no suppress/restore contract.

- [ ] **Step 2: Replace the active review workflow**

Rename the header action component/props to update terminology, start one durable scan job, persist only job/project IDs in plugin state, and resume polling when the view reopens. Keep the last verified projection readable during scanning. On failure, show the stable error code plus retry; do not expose private issue records or raw stderr.

- [ ] **Step 3: Add explicit human presentation controls**

Keep normal text editing as `set`. Add contextual `从呈现中隐藏` and `恢复自动内容` actions only for patchable generated entities. The guarded editor CAS-updates one Markdown file: hiding leaves a hidden stable-identity `suppress` marker while omitting visible content; restoring writes a `restore_default` marker and the current ledger baseline. If the Markdown preimage changed, write nothing and reload the explicit sync conflict. The next scan validates the marker against ledger identity/baseline and canonically republishes the patch set. Refresh from disk after each success. Orphan patches appear only in a concise recoverable notice, never silently attach to a replacement entity.

- [ ] **Step 4: Remove nonessential Agent/plugin settings UI**

Keep the settings tab absent. Remove the legacy `codexPath` field and all Agent validation/help text; after one-time compatibility cleanup, persist only view presentation state. Do not add executable paths, source scopes, memory diagnostics, or scan tuning to Obsidian.

- [ ] **Step 5: Preserve the current human-readable layout**

Keep project review, next action, recent history, evolution, and one full-width card per model. Preserve compact localized token totals together with the exact integer in parentheses, for example `5.73 亿（573,135,757）`. Label associated and shared usage explicitly; permit an incomplete final row and retain the full clickable pricing-source link. Verify narrow/wide layouts and keyboard focus.

- [ ] **Step 6: Run GREEN and commit**

Run: `cd obsidian-plugin && npm test -- --run && npm run check && npm run build`

Expected: PASS; built `main.js` contains `更新项目脉络` and does not contain the removed Agent setting labels or `总结并同步`.

```bash
git add obsidian-plugin/src obsidian-plugin/styles.css obsidian-plugin/tests/view.test.ts obsidian-plugin/tests/review-job-view.test.ts obsidian-plugin/tests/editor.test.ts obsidian-plugin/tests/main.test.ts obsidian-plugin/tests/styles.test.ts obsidian-plugin/tests/accessibility.test.ts
git commit -m "feat: update project context without an agent"
```

---

### Task 9: Lock Gate B with Cross-root and Plugin Acceptance Tests

**Files:**
- Create: `test/zerotoken/gate_b_test.go`
- Create: `test/zerotoken/fixtures/schema-v3/README.md`
- Create: `test/zerotoken/fixtures/schema-v3/project-review.md`
- Create: `test/zerotoken/fixtures/schema-v3/project-history.md`
- Create: `test/zerotoken/fixtures/schema-v3/ledger.json`
- Create: `obsidian-plugin/tests/gate-b-acceptance.test.ts`
- Create: `docs/architecture/zero-token-memory.md`

- [ ] **Step 1: Add end-to-end synthetic publication fixtures**

Generate source events in the test process, prepare one ProjectView, seed human patches plus unknown Markdown, publish to two distinct temporary roots, and verify exact generation/hash agreement. Do not commit real Session text or absolute local paths in fixtures.

- [ ] **Step 2: Add adversarial Gate B acceptance cases**

Cover crash/restart at every journal stage; Project and Vault edits before/after writes; symlink substitution; schema-2 writer against schema 3; schema-3 projection opened by an old plugin fixture; duplicate JSON keys; patch underlay change/orphan; and repeated unchanged update. Require zero partial publication, zero clobbered human bytes, and zero writes on the final repeat.

- [ ] **Step 3: Add plugin-to-CLI contract acceptance**

Run the built plugin runner against the built test CLI and assert exact argv, status transitions, reload recovery, terminal copy, associated/shared usage display, field edit persistence, and absence of Agent configuration. Use temporary Project/Vault/data roots only.

Install `main.js`, `manifest.json`, and `styles.css` into a disposable Vault's `.obsidian/plugins/session-reviewer/` directory, reload the test host, and run the same contract through the installed bytes. Assert installed hashes equal the just-built assets; delete the disposable Vault after the test.

- [ ] **Step 4: Run the complete Gate B verification matrix**

Run:

```bash
go test ./... -count=1
go test -race ./internal/publication ./internal/contextupdate ./internal/scanjob ./internal/sync ./internal/syncproject
go vet ./...
cd obsidian-plugin && npm ci && npm test -- --run && npm run check && npm run build
cd .. && go test ./test/zerotoken -run TestGateB -count=20
rg -n 'TB[D]|TO[DO]|implement la[t]er|GATE_B_CONTINU[E]' docs/superpowers/plans/2026-08-31-zero-token-gate-b-projection-publication.md
```

Expected: all test/build commands PASS; the final search returns no matches.

- [ ] **Step 5: Document Gate B recovery and commit**

Document journal stages, safe retry behavior, human-edit conflict handling, schema-v3 fail-closed behavior, and the exact `scan` commands. State clearly that this gate used only a disposable test-Vault installation and has not migrated a real project, installed into the user's Vault, or released anything.

```bash
git add test/zerotoken obsidian-plugin/tests/gate-b-acceptance.test.ts docs/architecture/zero-token-memory.md
git commit -m "test: verify zero-token publication gate"
```

Gate B is complete only when every command above passes and the repository still has no tag, push, marketplace submission, or installation into the user's real Obsidian Vault attributable to this plan.
