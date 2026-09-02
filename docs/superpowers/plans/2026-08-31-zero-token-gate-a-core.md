# Zero-token Gate A Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build deterministic Codex Session discovery, an observed-fact store, per-Session materialization, a live project probe, and a project reducer that prepare a complete zero-token generation without touching Project or Obsidian presentation files.

**Architecture:** A provider-neutral source adapter streams authenticated Codex JSONL boundaries into immutable observed revisions. A private content-addressed store prepares project generations containing SessionViews, a ProjectProbeState, and a ProjectView. Gate A stops at `prepared`; Gate B owns cross-root publication and is the only layer allowed to make a generation public.

**Tech Stack:** Go 1.26, standard-library JSON/crypto/fs packages, existing `pathguard`, `atomicfile`, `platform`, `config`, `session`, `accounting`, and `redact` packages; macOS and Windows filesystem tests.

**Spec:** `docs/superpowers/specs/2026-08-31-zero-token-session-memory-analysis-design.md`

## Global Constraints

- The scan path must not import `internal/agent`, invoke an Agent adapter, launch a model process, or make a network request.
- `ObservationStore` contains only directly observed immutable revisions; deterministic conclusions belong only to SessionView or ProjectView.
- Do not persist complete user/assistant transcripts or complete tool outputs. Persist typed fields, authenticated source references/hashes, and bounded redacted excerpts only.
- The first adapter is Codex only. Provider-neutral interfaces must not claim Claude Code or OpenCode support.
- A Session may contribute Observations to multiple projects; association happens per Observation, not per Session.
- SourceCatalog stores Session identity, boundary, availability, usage, and project-affinity metadata exactly once and no conversation body.
- Unchanged source boundaries and component versions create no new Observation, SessionView, ProjectProbeState, or ProjectView version.
- Every frozen Session terminates as `indexed`, `unsupported`, `missing`, `unreadable`, or `ambiguous`; one bad Session never stops later Sessions.
- Private directories/files use `0700`/`0600`, reject symlink/reparse traversal, and stay below the platform SessionReviewer data root.
- Gate A performs no Project/Vault Markdown write, schema migration, plugin change, Git mutation, release, publish, or deployment.
- Preserve unrelated worktree changes and stage only files named by the active task.

---

## File Structure and Ownership

- Create `internal/memory/types.go`: provider-neutral wire/domain types and terminal-state enums.
- Create `internal/memory/digest.go`: canonical validation, deterministic IDs, and dependency digests.
- Create four strict private schemas under `schemas/`: SourceCatalog, Observation, SessionView, and ProjectView v1.
- Create `internal/memorystore/store.go`: rooted private layout, immutable object writes, and prepared-generation manifests.
- Create `internal/memorystore/retention.go`: reachability report and seven-day staging/cache cleanup.
- Create `internal/projectidentity/resolve.go`: stable project-ID and authenticated alias/worktree resolution.
- Create `internal/source/adapter.go`: provider-neutral `Adapter` contract.
- Create `internal/sourcecatalog/catalog.go`: content-free provider Session catalog and shared usage ownership.
- Create `internal/source/codex/adapter.go` and `decode.go`: global Codex discovery, boundary freezing, typed decoding, and per-record project affinity.
- Create `internal/sessionview/materialize.go`: deterministic first-pass SessionView construction.
- Create `internal/projectprobe/probe.go`: read-only live Git/version/file state.
- Create `internal/projectview/reduce.go`: deterministic second-pass project aggregation.
- Create `internal/scan/service.go`: bounded orchestration and failure isolation.
- Create `internal/cli/scan.go`: Gate-A private preparation handler used by tests; public dispatch waits for Gate B.
- Add focused fixtures under `testdata/zero-token/` and package-local tests beside every new file.

---

### Task 1: Freeze the Private Memory Contracts

**Files:**
- Create: `internal/memory/types.go`
- Create: `internal/memory/digest.go`
- Create: `internal/memory/types_test.go`
- Create: `internal/memory/digest_test.go`
- Create: `schemas/source-catalog-v1.schema.json`
- Create: `schemas/observation-v1.schema.json`
- Create: `schemas/session-view-v1.schema.json`
- Create: `schemas/project-view-v1.schema.json`

**Interfaces:**
- Consumes: `accounting.SessionUsage` and RFC3339Nano timestamps.
- Produces: `memory.SourceRecord`, `ObservationKey`, `ObservationRevision`, `SessionView`, `ProjectProbeState`, `ProbeCheck`, `ProjectView`, `GenerationManifest`, `Digest`, `ObservationRevisionID`, and `Validate*` functions.

- [ ] **Step 1: Write failing contract and deterministic-ID tests**

```go
func TestObservationRevisionIdentitySeparatesStableKeyFromExtractorRevision(t *testing.T) {
    key := ObservationKey{Provider: "codex", SessionID: "s1", SourceIdentity: "src1", Sequence: 7, ProjectID: "project-a", Kind: "command", Subject: "go-test"}
    first := validObservation(key, "adapter-1", map[string]string{"exit_code": "1"})
    second := validObservation(key, "adapter-2", map[string]string{"exit_code": "0"})
    if first.Key != second.Key { t.Fatal("stable key changed") }
    if ObservationRevisionID(first) == ObservationRevisionID(second) { t.Fatal("revision did not change") }
}

func TestPrivateWireSchemasRejectSemanticAndRawConversationFields(t *testing.T) {
    for _, forbidden := range []string{"rationale", "intent", "full_transcript", "raw_tool_output"} {
        assertSchemaRejectsUnknownProperty(t, "../../schemas/observation-v1.schema.json", forbidden)
    }
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/memory -count=1`

Expected: FAIL because the package and schemas do not exist.

- [ ] **Step 3: Implement the exact domain records**

```go
type TerminalState string
const (
    Indexed TerminalState = "indexed"
    Unsupported TerminalState = "unsupported"
    Missing TerminalState = "missing"
    Unreadable TerminalState = "unreadable"
    Ambiguous TerminalState = "ambiguous"
)

type SourceRef struct {
    Provider string `json:"provider"`
    SessionID string `json:"session_id"`
    SourceIdentity string `json:"source_identity"`
    JSONLLine int `json:"jsonl_line"`
    ByteOffset int64 `json:"byte_offset"`
    SourceHash string `json:"source_hash"`
}

type ObservationKey struct {
    Provider string `json:"provider"`
    SessionID string `json:"session_id"`
    SourceIdentity string `json:"source_identity"`
    Sequence int `json:"sequence"`
    ProjectID string `json:"project_id"`
    Kind string `json:"kind"`
    Subject string `json:"subject"`
}

type ObservationRevision struct {
    SchemaVersion int `json:"schema_version"`
    Key ObservationKey `json:"key"`
    RevisionID string `json:"revision_id"`
    Ref SourceRef `json:"source_ref"`
    Timestamp string `json:"timestamp"`
    Operation string `json:"operation,omitempty"`
    Object string `json:"object,omitempty"`
    Outcome string `json:"outcome,omitempty"`
    Fields map[string]string `json:"fields,omitempty"`
    Excerpt string `json:"excerpt,omitempty"`
    AdapterID string `json:"adapter_id"`
    AdapterVersion string `json:"adapter_version"`
}
```

Also define `SourceRecord` with one `accounting.SessionUsage`, availability, and frozen boundary; `SessionView` with active revision IDs and derived records; `ProjectProbeState` without wall-clock time; `ProbeCheck` with `CheckedAt`; `ProjectView` with ordered SessionView dependencies; and `GenerationManifest` with active/superseded/withdrawn revision maps. All schemas use `additionalProperties: false`, bounded strings/arrays, safe ID patterns, lowercase SHA-256 digests, and exact schema version `1`.

- [ ] **Step 4: Implement canonical hashing and strict validation**

`Digest(value)` JSON-encodes a normalized defensive copy, sorts every semantically unordered slice/map key, rejects NaN/Inf and invalid UTF-8, and returns `sha256:<64 lowercase hex>`. `ObservationRevisionID` hashes stable key, normalized payload, source hash, and adapter version. Validation rejects prose-only semantic kinds, raw conversation fields, duplicate IDs, inactive dependencies selected as active, and impossible terminal counts.

- [ ] **Step 5: Run GREEN and commit**

Run: `gofmt -w internal/memory && go test ./internal/memory -count=1`

Expected: PASS; two identical normalized values have identical digests and every schema fixture validates.

```bash
git add internal/memory schemas/source-catalog-v1.schema.json schemas/observation-v1.schema.json schemas/session-view-v1.schema.json schemas/project-view-v1.schema.json
git commit -m "feat: define zero-token memory contracts"
```

---

### Task 2: Build the Immutable Private Store

**Files:**
- Create: `internal/memorystore/store.go`
- Create: `internal/memorystore/store_test.go`
- Create: `internal/memorystore/testdata_test.go`

**Interfaces:**
- Consumes: validated memory records and an absolute platform data root.
- Produces: `memorystore.Open`, `PutObservationChunk`, `PutSessionView`, `PutProbeState`, `PutProjectView`, `PrepareGeneration`, `LoadPrepared`, `LoadObject`, and `Close`.

- [ ] **Step 1: Write failing privacy, immutability, and crash tests**

Tests cover `0700` directories/`0600` files, symlink/reparse rejection, rooted-path escape, same-digest idempotence, different bytes at an existing digest, interrupted temporary writes, corrupt manifest/backup, duplicate JSON fields, concurrent preparation, and restart after every manifest checkpoint.

Run: `go test ./internal/memorystore -run 'Test(Store|PreparedGeneration)' -count=1`

Expected: FAIL because `memorystore` does not exist.

- [ ] **Step 2: Implement the rooted layout and API**

```text
<data>/source-catalog/
<data>/projects/<project-id>/memory-v1/
  manifest.json
  observations/<sha256>.jsonl
  sessions/<sha256>.json
  project-probes/<sha256>.json
  project-views/<sha256>.json
  generations/<generation-id>.json
  diagnostics/<generation-id>.json
  staging/
  locks/scan.lock
```

```go
type Prepared struct { GenerationID string; ManifestDigest string; ProjectViewDigest string }
func Open(dataRoot, projectID string) (*Store, error)
func (s *Store) PutObservationChunk([]memory.ObservationRevision) (string, error)
func (s *Store) PutSessionView(memory.SessionView) (string, error)
func (s *Store) PutProbeState(memory.ProjectProbeState) (string, error)
func (s *Store) PutProjectView(memory.ProjectView) (string, error)
func (s *Store) PrepareGeneration(memory.GenerationManifest) (Prepared, error)
```

Use `pathguard.Open`, private rooted directories, `atomicfile.WriteRootFileChecked`, canonical re-read, and content-addressed names without caller-controlled path fragments. Gate A must not create `published_generation`.

- [ ] **Step 3: Prove immutable replay and recovery**

Run: `gofmt -w internal/memorystore && go test ./internal/memorystore -run 'Test(Store|PreparedGeneration)' -count=20`

Expected: PASS; exactly one concurrent preparer wins and every interrupted preparation leaves either no generation or one fully readable prepared generation.

- [ ] **Step 4: Commit**

```bash
git add internal/memorystore
git commit -m "feat: add immutable project memory store"
```

---

### Task 3: Resolve Stable Project Identity and SourceCatalog

**Files:**
- Create: `internal/projectidentity/resolve.go`
- Create: `internal/projectidentity/resolve_test.go`
- Create: `internal/sourcecatalog/catalog.go`
- Create: `internal/sourcecatalog/catalog_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `config.ProjectMapping`, authenticated project root, Git common-directory identity, configured aliases/remotes, and `memory.SourceRecord`.
- Produces: `projectidentity.Resolve`, `projectidentity.Binding`, `sourcecatalog.Open`, `UpsertSource`, `GetSource`, `ListCandidates`, and `AssociatedUsage`.

- [ ] **Step 1: Write failing identity and shared-usage tests**

```go
func TestResolveKeepsProjectIDAcrossVerifiedWorktreeAndMove(t *testing.T) {
    binding := mustResolveAlias(t, mappingWithCommonDir("project-a", commonDir), movedWorktree)
    if binding.ProjectID != "project-a" { t.Fatalf("got %q", binding.ProjectID) }
}

func TestCatalogStoresSharedSessionUsageOnce(t *testing.T) {
    catalog := openCatalog(t)
    mustUpsert(t, catalog, sourceRecord("s1", []string{"project-a", "project-b"}, 573135757))
    if got := catalogCountUsageRows(t, catalog, "s1"); got != 1 { t.Fatalf("rows=%d", got) }
}
```

Also test that a mutable path or remote string alone cannot merge stores, conflicting identity evidence returns `ErrAssociationRequired`, trailing-space paths remain exact on macOS, Windows aliases use `platform.PathKey`, and catalog JSON contains no message/tool-output keys.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/projectidentity ./internal/sourcecatalog ./internal/config -count=1`

Expected: FAIL with missing resolver and catalog APIs.

- [ ] **Step 3: Implement stable binding and catalog CAS**

```go
type Binding struct {
    ProjectID string
    CanonicalRoot string
    RootIdentity pathguard.IdentityToken
    CommonDirIdentity string
}
func Resolve(mapping config.ProjectMapping, requestedRoot, goos string) (Binding, error)

type AssociatedUsage struct {
    SessionID string `json:"session_id"`
    Usage accounting.SessionUsage `json:"usage"`
    Shared bool `json:"shared"`
}
```

Extend `ProjectMapping` only with versioned authenticated alias metadata needed to preserve stable identity; retain existing `Aliases`, `RemoteIdentities`, and `CommonDirs` on round-trip. SourceCatalog writes one content-free record per `(provider, session_id)` through rooted CAS and stores sorted distinct project IDs. `AssociatedUsage(projectID)` returns `Shared=true` when more than one project ID is present; it never divides tokens.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/projectidentity internal/sourcecatalog internal/config && go test ./internal/projectidentity ./internal/sourcecatalog ./internal/config -count=1`

Expected: PASS; moving a verified worktree preserves `project_id`, while conflicting evidence causes no config or catalog mutation.

```bash
git add internal/projectidentity internal/sourcecatalog internal/config/config.go internal/config/config_test.go
git commit -m "feat: preserve project and source identities"
```

---

### Task 4: Add the Codex Source Adapter and Observed Revision Decoder

**Files:**
- Create: `internal/source/adapter.go`
- Create: `internal/source/adapter_test.go`
- Create: `internal/source/codex/adapter.go`
- Create: `internal/source/codex/adapter_test.go`
- Create: `internal/source/codex/decode.go`
- Create: `internal/source/codex/decode_test.go`
- Create: `testdata/zero-token/codex/session-project-a.jsonl`
- Create: `testdata/zero-token/codex/session-shared.jsonl`
- Create: `testdata/zero-token/codex/session-malformed.jsonl`

**Interfaces:**
- Consumes: `session.Discover`, `session.OpenCandidates`, `session.StreamFiles`, project bindings, redactor, and source catalog.
- Produces: `source.Adapter`, `source.Discovery`, `source.Boundary`, `source.DecodeReport`, and `codex.New(AdapterOptions)`.

- [ ] **Step 1: Write failing global discovery/freeze/decode tests**

Tests prove all JSONL candidates are considered on first catalog refresh; ordered physical segments become one logical boundary; a later append freezes separately; malformed lines increment diagnostics but later lines decode; a Session that changes CWD or touches two rooted projects yields project-specific revisions; missing/duplicate segments become terminal issues; and no persisted excerpt exceeds 512 Unicode code points after redaction.

Run: `go test ./internal/source/... -count=1`

Expected: FAIL because the adapter packages do not exist.

- [ ] **Step 2: Define the provider-neutral contract**

```go
type Adapter interface {
    Discover(context.Context) (Discovery, error)
    Freeze(context.Context, Candidate) (Boundary, error)
    Decode(context.Context, Boundary, func(memory.ObservationRevision) error) (DecodeReport, error)
    Read(context.Context, memory.SourceRef, int64) ([]byte, error)
}
```

`Read` rejects a limit below 1 or above 64 KiB and verifies the exact source hash before returning bounded bytes. Gate A tests `Read`; no caller stores its complete result.

- [ ] **Step 3: Implement exact Codex decoding rules**

| Codex record | Observation kind | Typed fields |
|---|---|---|
| `session_meta` | `session_started` | session ID, initial CWD, started_at |
| `turn_context` | `cwd_changed` | authenticated rooted CWD |
| user `response_item.message` | `user_request` | bounded redacted excerpt only |
| `custom_tool_call` named `exec_command` | `command_started` | normalized executable/argv class, rooted workdir, tool-call ID |
| matching tool output | `command_finished` | exit code, bounded normalized signature, tool-call ID |
| `apply_patch` call/result | `file_change` | rooted target paths and success/failure only |
| Git command/result | `git_observation` | exact branch/HEAD/status/tag fields when grammar-valid |
| test/build/lint command/result | `verification` | normalized operation, component, exit code |
| token-count/accounting payload | source catalog usage | validated `accounting.SessionUsage`, not an Observation copy |

Unknown record/tool kinds advance the boundary and add an `unsupported_record` count without storing raw payload. Project affinity uses current authenticated CWD and rooted file/workdir targets. Ambiguous roots quarantine that revision instead of guessing. Reuse `redact.Redactor`; parse typed hashes/IDs/paths before prose redaction.

- [ ] **Step 4: Prove zero-copy and revision supersession**

Run: `gofmt -w internal/source && go test ./internal/source/... -count=1`

Expected: PASS; fixture output contains typed facts/source hashes but no complete user message or complete tool output. Decoding the same boundary with adapter v2 creates successor revision IDs and withdraws v1 from the active set.

- [ ] **Step 5: Commit**

```bash
git add internal/source testdata/zero-token/codex
git commit -m "feat: decode codex sessions without model calls"
```

---

### Task 5: Materialize Durable SessionViews

**Files:**
- Create: `internal/sessionview/materialize.go`
- Create: `internal/sessionview/materialize_test.go`

**Interfaces:**
- Consumes: one frozen source record, target-project Observation revisions, previous SessionView, catalog usage reference, and materializer version.
- Produces: `sessionview.Materialize(Input) (memory.SessionView, bool, error)` where `bool` is `changed`.

- [ ] **Step 1: Write failing deterministic/incremental tests**

```go
func TestMaterializeReusesUnchangedDependencyDigest(t *testing.T) {
    first, changed, err := Materialize(fixtureInput(nil))
    if err != nil || !changed { t.Fatal(err) }
    second, changed, err := Materialize(fixtureInput(&first))
    if err != nil || changed || second.Digest != first.Digest { t.Fatalf("churn: %#v", second) }
}
```

Also cover append-only successor revisions, missing source preserving prior facts, unsupported/malformed terminal states, deterministic derived failure-recovery links, shared usage references, and source-availability digest changes.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/sessionview -count=1`

Expected: FAIL with missing materializer.

- [ ] **Step 3: Implement first-pass rules**

`Materialize` sorts active revisions by source sequence/revision ID; deduplicates only exact typed identity; links a failure to a later success only when normalized operation and component match; records files/commands/commits/tests/errors/artifacts as references; keeps user-request excerpts bounded; references catalog usage rather than copying it; and sets exactly one terminal state. Its dependency digest includes active revision IDs, frozen source-availability digest, usage-record digest, and `MaterializerVersion = "session-view-v1"`.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/sessionview && go test ./internal/sessionview -count=20`

Expected: PASS with byte-identical JSON and `changed=false` on unchanged input.

```bash
git add internal/sessionview
git commit -m "feat: materialize deterministic session views"
```

---

### Task 6: Probe Live Project State Without Executing Project Code

**Files:**
- Create: `internal/projectprobe/probe.go`
- Create: `internal/projectprobe/probe_test.go`
- Create: `internal/projectprobe/git.go`
- Create: `internal/projectprobe/version_files.go`

**Interfaces:**
- Consumes: authenticated project binding, declared version-file allowlist, time source, and command runner restricted to read-only Git argv.
- Produces: `projectprobe.Run(context.Context, Options) (memory.ProjectProbeState, memory.ProbeCheck, error)`.

- [ ] **Step 1: Write failing safety and no-churn tests**

Tests prove only `git rev-parse --show-toplevel`, `git symbolic-ref --short -q HEAD`, `git rev-parse HEAD`, `git status --porcelain=v1 -z`, and `git remote get-url --all origin` are allowed; no shell is used; package managers/tests/scripts/network are rejected; version files are read by rooted handle; and two checks with identical state have one state digest but different `ProbeCheck.CheckedAt`.

Run: `go test ./internal/projectprobe -count=1`

Expected: FAIL with missing probe API.

- [ ] **Step 2: Implement content state versus check metadata**

```go
type Options struct {
    Binding projectidentity.Binding
    VersionFiles []string
    RequiredProjectionFiles []string
    Now func() time.Time
    RunGit func(context.Context, string, ...string) ([]byte, error)
}
```

Normalize only grammar-valid Git outputs. Hash version/required files without copying contents. `ProjectProbeState` contains branch, HEAD, dirty-path counts, remote identity hashes, file existence/hashes, probe version, and typed diagnostics. `ProbeCheck` contains UTC check time, state digest, and availability diagnostics. Exclude time from the state digest.

- [ ] **Step 3: Run GREEN and commit**

Run: `gofmt -w internal/projectprobe && go test ./internal/projectprobe -count=20`

Expected: PASS; the fake runner records no unapproved executable/argument sequence.

```bash
git add internal/projectprobe
git commit -m "feat: add read-only project probe"
```

---

### Task 7: Reduce SessionViews into ProjectView

**Files:**
- Create: `internal/projectview/reduce.go`
- Create: `internal/projectview/reduce_test.go`
- Create: `internal/projectview/ranking.go`
- Create: `internal/projectview/recovery.go`

**Interfaces:**
- Consumes: ordered SessionViews, one ProjectProbeState, associated usage records, previous ProjectView, and reducer version.
- Produces: `projectview.Reduce(Input) (memory.ProjectView, bool, error)`.

- [ ] **Step 1: Write failing reduction tests**

Cover exact coverage reconciliation, deterministic total ordering, exact-ID deduplication, compatible recovery only, structural phase boundaries, module ranking, associated/shared usage, historical-versus-live state separation, no semantic rationale invention, and unchanged-result reuse.

Run: `go test ./internal/projectview -count=1`

Expected: FAIL with missing reducer.

- [ ] **Step 2: Implement explicit reducer formulas**

Use `(occurred_at, session_id, observation_sequence, revision_id)` as ascending event order. Rank modules by `4*session_coverage + 2*verification_count + change_count + recency_bucket`, with recency buckets `3` (last 7 days), `2` (8–30), `1` (31–90), `0` (older), then break ties by normalized path. Create structural boundaries only at validated version/tag/release/branch changes or a gap greater than 30 days; name them by date/version, never inferred prose. Sum project-associated usage once per provider/session ID and set `Shared=true` on shared rows.

- [ ] **Step 3: Keep ProjectProbe and history claims separate**

The reducer stores `LiveState` from ProjectProbe with its state digest and stores `WitnessedState` from Sessions with observation timestamps. It must never turn current HEAD/status into a claim that an earlier release, deployment, or test succeeded.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/projectview && go test ./internal/projectview -count=20`

Expected: PASS; shuffled equivalent inputs produce the same ProjectView digest.

```bash
git add internal/projectview
git commit -m "feat: reduce project session history deterministically"
```

---

### Task 8: Orchestrate a Complete Prepared Scan

**Files:**
- Create: `internal/scan/service.go`
- Create: `internal/scan/service_test.go`
- Create: `internal/scan/status.go`
- Create: `internal/cli/scan.go`
- Create: `internal/cli/scan_test.go`

**Interfaces:**
- Consumes: project ID/root, sessions root, data root, Codex adapter, source catalog, store, materializer, probe, reducer, bounded worker count, and time source.
- Produces: `scan.Run(context.Context, Options) (scan.Result, error)` and private `cli.runScanCore` JSON formatting.

- [ ] **Step 1: Write failing isolation and zero-token tests**

```go
func TestRunCompletesEveryFrozenSessionWithoutAgent(t *testing.T) {
    result, err := Run(context.Background(), fixtureOptions(154))
    if err != nil { t.Fatal(err) }
    if result.SourceSessions != 154 || result.TerminalSessions != 154 { t.Fatalf("%+v", result) }
    if result.ReviewRunTokens != 0 || result.State != CompletedWithIssues { t.Fatalf("%+v", result) }
}
```

Inject unsupported, missing, unreadable, ambiguous, malformed, cross-project, append-only, and unchanged candidates. Assert later Sessions still run, foreign Observations never enter the target store, worker concurrency never exceeds `min(4, GOMAXPROCS)`, and cancellation leaves a readable prepared-or-no-generation state.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/scan ./internal/cli -run 'Test(Run|ScanCore)' -count=1`

Expected: FAIL with missing scan service.

- [ ] **Step 3: Implement the lifecycle and result contract**

```go
type State string
const (
    Completed State = "completed"
    CompletedWithIssues State = "completed_with_issues"
    Failed State = "failed"
)
type Result struct {
    SchemaVersion int `json:"schema_version"`
    ProjectID string `json:"project_id"`
    GenerationID string `json:"generation_id"`
    State State `json:"state"`
    SourceSessions int `json:"source_sessions"`
    IndexedSessions int `json:"indexed_sessions"`
    TerminalSessions int `json:"terminal_sessions"`
    IssueSessions int `json:"issue_sessions"`
    ProjectViewDigest string `json:"project_view_digest"`
    ReviewRunTokens int64 `json:"review_run_tokens"`
    Prepared bool `json:"prepared"`
}
```

`Run` acquires the per-project scan lock, refreshes the content-free catalog, freezes all candidate boundaries, decodes with bounded parallelism, persists target-project Observation chunks, materializes every terminal SessionView, probes, reduces, verifies count reconciliation, and prepares one generation. Project/Vault files are never opened for writing. `runScanCore` accepts exact flags `--project-id`, `--sessions-root`, `--data-dir`, and `--json`; it is not added to root dispatch until Gate B can publish safely.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/scan internal/cli/scan.go internal/cli/scan_test.go && go test ./internal/scan ./internal/cli -run 'Test(Run|ScanCore)' -count=1`

Expected: PASS; output is one bounded JSON object with `review_run_tokens:0` and no paths/session text.

```bash
git add internal/scan internal/cli/scan.go internal/cli/scan_test.go
git commit -m "feat: prepare complete zero-token project scans"
```

---

### Task 9: Add Retention Reporting and Gate A Acceptance

**Files:**
- Create: `internal/memorystore/retention.go`
- Create: `internal/memorystore/retention_test.go`
- Create: `test/zerotoken/gate_a_test.go`
- Create: `test/zerotoken/doc.go`
- Create: `testdata/zero-token/manifest.json`
- Modify: `docs/release/acceptance-review-v2-core.md`

**Interfaces:**
- Consumes: all committed/prepared manifests and live publication journals.
- Produces: `memorystore.RetentionReport`, `Store.ReportRetention(now)`, `Store.CleanupUnreachable(now)`, and the Gate A fixture replay.

- [ ] **Step 1: Write failing reachability and grace-period tests**

Tests prove committed lineage is never a cleanup candidate; prepared and live-journal objects remain reachable; duplicate cache and never-published staging younger than seven days remain; objects at or older than seven days are removed by rooted CAS; dry report mutates nothing; and corrupt reachability metadata fails closed without deletion.

Run: `go test ./internal/memorystore -run 'TestRetention' -count=1`

Expected: FAIL with missing retention API.

- [ ] **Step 2: Implement report-before-delete cleanup**

```go
type RetentionReport struct {
    ReachableObjects int `json:"reachable_objects"`
    ReachableBytes int64 `json:"reachable_bytes"`
    CleanupCandidates int `json:"cleanup_candidates"`
    CleanupBytes int64 `json:"cleanup_bytes"`
}
```

Enumerate only canonical object/staging/cache names below the pinned memory root. `CleanupUnreachable` recomputes reachability immediately before every deletion and rejects any namespace or mtime change. It never deletes an object referenced by committed lineage.

- [ ] **Step 3: Add the sanitized Gate A replay**

The fixture manifest defines 154 logical Sessions: 151 indexed, one unsupported, one malformed-but-terminal, and one cross-project ambiguous. It also exercises one shared Session, one append, one adapter supersession, one missing-source replay, and one unchanged replay. The acceptance test asserts exact terminal reconciliation, zero Agent imports/calls, zero review-run tokens, no transcript/tool-output copy, stable digests on unchanged replay, successor-only writes on append, and platform-equivalent path identities.

Run: `go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1 -v`

Expected: PASS with one safe line: `Gate A: 154/154 terminal, 151 indexed, zero model tokens`.

- [ ] **Step 4: Run the complete Gate A verification**

```bash
go test ./internal/memory ./internal/memorystore ./internal/projectidentity ./internal/sourcecatalog ./internal/source/... ./internal/sessionview ./internal/projectprobe ./internal/projectview ./internal/scan ./internal/cli ./test/zerotoken -count=1
go test -race ./internal/memorystore ./internal/sourcecatalog ./internal/scan -count=1
go vet ./...
git diff --check
```

Expected: all commands exit 0. No Project/Vault fixture bytes change during Gate A replay.

- [ ] **Step 5: Commit**

```bash
git add internal/memorystore/retention.go internal/memorystore/retention_test.go test/zerotoken testdata/zero-token/manifest.json docs/release/acceptance-review-v2-core.md
git commit -m "test: accept zero-token core gate"
```

Gate A is complete only when the prepared private generation is deterministic and fully verified. Do not expose the scan command publicly or publish a release before Gates B and C.
