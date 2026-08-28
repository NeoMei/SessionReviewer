# Obsidian Agent Review Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add one `总结并同步` action to the existing Obsidian SessionReviewer view that durably reviews every click-time pending Codex session, applies each valid proposal, and synchronizes accepted project context without disturbing the current interface.

**Architecture:** Keep Obsidian as a typed control client. A new `review` CLI control plane creates a private durable job and launches a one-shot worker. The worker freezes session boundaries, calls a proposal-only `AgentAdapter`, then reuses bounded prepare, validated apply, cursor CAS, and deterministic sync services. Codex v1 runs ephemerally with no model override, a read-only sandbox, ignored rules/configured tools, explicit tool-feature disables, structured output, and fail-closed event validation.

**Tech Stack:** Go 1.26, existing `pathguard`/`atomicfile`/`project` locks, `os/exec`, JSON Schema, TypeScript 5.8, Obsidian 1.13, Vitest/jsdom, ESLint, esbuild, macOS and Windows native process control.

**Spec:** `docs/superpowers/specs/2026-08-28-obsidian-agent-review-orchestration-design.md`

## Global Constraints

- Preserve `renderReadyView` order exactly: header → resume → risks → tabs → selected panel.
- Do not alter existing evolution, decision, usage, risk, editing, project-picker, card-grid, or breakpoint behavior.
- The plugin never reads sessions, packets, proposals, prompts, Agent output, source hashes, or private job files.
- The Agent never writes Project, Vault, cursor, ledger, Base, or private state; only trusted Go services prepare, apply, and sync.
- Freeze every selected session at a click-time `(line, source_hash)` boundary before launching Codex; later records remain pending.
- Process frozen sessions by `(started_at, session_id)`, process bounded successor packets in order, sync after every accepted packet, and stop on the first failure.
- A cancellation may terminate Codex before apply; it may not kill apply or sync inside their commit windows.
- One active job per project and one active Codex worker globally in v1.
- Public JSON and plugin messages use fixed safe fields/codes and never concatenate internal errors or paths.
- Existing source-session accounting remains unchanged. Review-run accounting is private job data and must not enter current usage cards.
- No watcher, daemon, Claude/OpenCode adapter, Git mutation, release, publish, or deployment is part of this implementation.
- Preserve all unrelated dirty-worktree changes; stage only files named by the current task.

---

## File Structure and Ownership

- Create `internal/reviewjob/types.go`: private job schema, public status projection, state/phase/error enums, frozen-session records.
- Create `internal/reviewjob/store.go`: private rooted atomic store, latest-project pointer, authenticated compare-and-swap updates.
- Create `internal/reviewjob/lease.go`: project/global advisory lease acquisition and restart-safe owner metadata.
- Create `internal/reviewjob/freeze.go`: project session discovery, click-time upper boundaries, chronological order, pending detection.
- Create `internal/reviewjob/service.go`: prepare → propose → apply → sync orchestration and cancellation safe points.
- Create `internal/reviewjob/accounting.go`: review-run usage aggregation and injected list-price resolution.
- Create `internal/reviewjob/pricing_catalog.go`: date-stamped public list-price entries for Codex models in the supported compatibility contract.
- Create `internal/agent/agent.go`: provider-neutral `AgentAdapter` contract and safe provider errors.
- Create `internal/agent/codex/verify.go`: executable identity/version/capability verification and supported-contract table.
- Create `internal/agent/codex/run.go`: fixed argv, stdin prompt, JSONL event parser, structured proposal extraction.
- Create `internal/agent/codex/process_unix.go` and `process_windows.go`: bounded process-tree cancellation.
- Create `internal/reviewprompt/prompt.go`: versioned bounded prompt bundle built only from accepted context, packet, schema, and invariants.
- Create `schemas/review-job-status-v1.schema.json`: public CLI status contract only.
- Modify `internal/prepare/prepare.go`: enforce an authenticated frozen upper source boundary.
- Create `internal/syncproject/service.go`: typed mapping resolution and deterministic sync service shared by CLI and worker without an import cycle.
- Modify `internal/cli/sync.go`: delegate deterministic sync execution to `internal/syncproject` while preserving CLI formatting.
- Create `internal/cli/review.go`: verify/start/status/cancel/retry and private worker entry point.
- Modify `internal/cli/run.go`: dispatch and document the `review` family.
- Modify `obsidian-plugin/src/cli/runner.ts`: typed review/sync calls, exact argv allowlist, safe status parsing.
- Modify `obsidian-plugin/src/cli/settings.ts` and `src/main.ts`: persist and verify a separate absolute Codex executable path.
- Modify `obsidian-plugin/src/view/render-shell.ts`: add one injected header action without changing normal section order.
- Modify `obsidian-plugin/src/view/project-view.ts`: poll/recover job status and render existing-style banners.
- Modify `obsidian-plugin/styles.css`: add only scoped `.sr-review-*` rules.
- Add focused Go/plugin fixtures and tests; update README/CI/package checks only for this feature.

---

### Task 1: Freeze the Private Job and Public Status Contracts

**Files:**
- Create: `internal/reviewjob/types.go`
- Create: `internal/reviewjob/types_test.go`
- Create: `schemas/review-job-status-v1.schema.json`
- Modify: `scripts/build-release.sh`
- Modify: `scripts/build-release.ps1`

**Interfaces:**
- Consumes: `pathguard.IdentityToken`, `evidence.CursorBoundary`, `accounting.TokenUsage`.
- Produces: `reviewjob.Job`, `reviewjob.PublicStatus`, `reviewjob.FrozenSession`, `reviewjob.SafeError`, `reviewjob.Validate`, and `reviewjob.ProjectStatus`.

- [ ] **Step 1: Write failing schema and redaction tests**

~~~go
func TestPublicStatusIsSchemaValidAndCannotExposePrivateFields(t *testing.T) {
    job := validJobFixture()
    job.PrivateError = "/Users/mei/.codex/sessions/raw secret prompt"
    status, err := ProjectStatus(job)
    if err != nil { t.Fatal(err) }
    body := mustJSON(t, status)
    for _, forbidden := range []string{"/Users/", "raw secret", "source_hash", "prompt", "stdout", "stderr"} {
        if bytes.Contains(body, []byte(forbidden)) { t.Fatalf("public leak %q", forbidden) }
    }
    validateAgainstSchema(t, "../../schemas/review-job-status-v1.schema.json", body)
}
~~~

- [ ] **Step 2: Run RED**

Run: `go test ./internal/reviewjob -run 'Test(PublicStatus|JobValidation)' -count=1`

Expected: FAIL because `internal/reviewjob` and its types do not exist.

- [ ] **Step 3: Implement the exact enums and records**

~~~go
type State string
const (
    Queued State = "queued"; Running State = "running"; Completed State = "completed"
    Failed State = "failed"; CancelRequested State = "cancel_requested"
    Cancelled State = "cancelled"; Retrying State = "retrying"
)
type Phase string
const (
    Preflight Phase = "preflight"; Scanning Phase = "scanning"; Preparing Phase = "preparing"
    Reviewing Phase = "reviewing"; Applying Phase = "applying"; Syncing Phase = "syncing"
)
type FrozenSession struct {
    SessionID string `json:"session_id"`
    StartedAt time.Time `json:"started_at"`
    Upper evidence.CursorBoundary `json:"upper"`
}
type PublicState string
const Idle PublicState = "idle"
type PublicStatus struct {
    SchemaVersion int `json:"schema_version"`
    JobID string `json:"job_id,omitempty"`
    ProjectID string `json:"project_id"`
    State PublicState `json:"state"`
    Phase Phase `json:"phase,omitempty"`
    Attempt int `json:"attempt"`
    SessionIndex int `json:"session_index"`
    SessionCount int `json:"session_count"`
    AcceptedPackets int `json:"accepted_packets"`
    AcceptedSessions int `json:"accepted_sessions"`
    ErrorCode string `json:"error_code,omitempty"`
    CanRetry bool `json:"can_retry"`
    CanCancel bool `json:"can_cancel"`
    CanSyncOnly bool `json:"can_sync_only"`
    ReviewUsage *PublicReviewUsage `json:"review_usage,omitempty"`
}
~~~

`ProjectStatus(nil, projectID)` returns the public-only `idle` state with no job ID. `Job` additionally stores canonical project identity, verified Agent identity/version, frozen sessions, current packet boundary, timestamps, owner metadata, cancellation request, digests, review usage, and `PrivateError`. Validation rejects unknown enum values, unsafe IDs, non-canonical timestamps, invalid identities/hashes, impossible progress, duplicate sessions, and terminal jobs with live ownership.

- [ ] **Step 4: Add the public schema to both release packagers and rerun GREEN**

Run: `gofmt -w internal/reviewjob && go test ./internal/reviewjob -count=1 && ./scripts/build-release.sh 0.2.5 "$(mktemp -d)"`

Expected: PASS; the built archive contains `schemas/review-job-status-v1.schema.json`.

- [ ] **Step 5: Commit**

~~~bash
git add internal/reviewjob/types.go internal/reviewjob/types_test.go schemas/review-job-status-v1.schema.json scripts/build-release.sh scripts/build-release.ps1
git commit -m "feat: define review job status contract"
~~~

---

### Task 2: Build the Durable Private Job Store

**Files:**
- Create: `internal/reviewjob/store.go`
- Create: `internal/reviewjob/store_test.go`

**Interfaces:**
- Consumes: platform data root and validated `Job` values.
- Produces: `Store.Create`, `Store.Load`, `Store.Update(jobID, expectedRevision, mutate)`, and `Store.LatestForProject`.

- [ ] **Step 1: Write RED tests for atomicity, permissions, CAS, recovery, and hostile paths**

Cover canonical JSON, `0700` directories/`0600` files, duplicate-field rejection, primary/backup recovery, symlink/reparse rejection, case collision, same-inode mutation, concurrent stale revision, bounded entry/byte counts, and an authenticated project pointer that cannot name another project's job.

Run: `go test ./internal/reviewjob -run 'TestStore' -count=1`

Expected: FAIL with missing `Store`.

- [ ] **Step 2: Implement the rooted layout**

~~~text
<data>/review-jobs/
  jobs/<job-id>.json
  jobs/<job-id>.json.bak
  projects/<project-id>.json
  locks/global.lock
  locks/projects/<project-id>.lock
  work/<job-id>/
~~~

Use `pathguard.Open`, `atomicfile.EnsureRootDir`, `atomicfile.WriteRoot`, canonical re-read, and revision CAS. `Create` writes the job before the project pointer; a crash between them is repaired by bounded authenticated enumeration. Never enumerate arbitrary files outside `review-jobs`.

- [ ] **Step 3: Prove crash recovery and concurrency**

Run: `gofmt -w internal/reviewjob && go test ./internal/reviewjob -run 'TestStore' -count=20`

Expected: PASS with no orphaned active pointer and exactly one successful CAS writer.

- [ ] **Step 4: Commit**

~~~bash
git add internal/reviewjob/store.go internal/reviewjob/store_test.go
git commit -m "feat: persist private review jobs atomically"
~~~

---

### Task 3: Add Project and Global Worker Leases

**Files:**
- Create: `internal/reviewjob/lease.go`
- Create: `internal/reviewjob/lease_test.go`

**Interfaces:**
- Consumes: `Store`'s pinned data root, project ID, job ID, worker PID/start token.
- Produces: `AcquireLeases(projectID, jobID, timeout)`, `LeaseSet.Release`, and `RecoverInterrupted`.

- [ ] **Step 1: Write cross-process RED tests**

Tests prove: two starts for one project cannot win; two projects cannot simultaneously own the global Codex lease; abrupt child exit releases kernel ownership; stale JSON owner metadata never overrides a live advisory lock; restart converts a lock-free `running` job to recoverable `failed/E_APPLY_RECOVERY` or resumes it only after apply-receipt inspection.

Run: `go test ./internal/reviewjob -run 'Test(Lease|Interrupted)' -count=1`

Expected: FAIL with missing lease API.

- [ ] **Step 2: Reuse the existing hardened lock primitive**

Open the pinned data root and call:

~~~go
projectLease, err := project.AcquireProjectLock(root, "review-jobs/locks/projects/"+projectID+".lock", 0)
globalLease, err := project.AcquireProjectLock(root, "review-jobs/locks/global.lock", 0)
~~~

Acquire project first, global second; release in reverse order. Map contention to `E_AGENT_BUSY`. Owner metadata is diagnostic only; kernel locks are authoritative.

- [ ] **Step 3: Run race/stress GREEN**

Run: `gofmt -w internal/reviewjob && go test -race ./internal/reviewjob -run 'Test(Lease|Interrupted)' -count=20`

Expected: PASS with one winner per contention test.

- [ ] **Step 4: Commit**

~~~bash
git add internal/reviewjob/lease.go internal/reviewjob/lease_test.go
git commit -m "feat: serialize automatic review workers"
~~~

---

### Task 4: Freeze Pending Sessions at Click Time

**Files:**
- Create: `internal/reviewjob/freeze.go`
- Create: `internal/reviewjob/freeze_test.go`
- Modify: `internal/prepare/prepare.go`
- Modify: `internal/prepare/prepare_test.go`

**Interfaces:**
- Consumes: `session.Discover`, `session.OpenCandidates`, `cursor.Store.LoadReadOnly`, mapping root/identity, sessions root.
- Produces: `FreezePending(FreezeOptions) ([]FrozenSession, error)` and `prepare.Options.UpperBoundary *evidence.CursorBoundary`.

- [ ] **Step 1: Write RED tests for chronological freeze and active append**

Fixtures include two completed sessions, one active session, one unrelated project, duplicate physical segments, an already accepted cursor, a corrupt matching candidate, and a session appended after freeze. Assert order `(StartedAt, SessionID)` and exact upper hashes.

Run: `go test ./internal/reviewjob ./internal/prepare -run 'Test(Freeze|PrepareHonorsFrozen)' -count=1`

Expected: FAIL because freeze and `UpperBoundary` do not exist.

- [ ] **Step 2: Implement bounded discovery without path persistence**

Stream the discovered pinned candidate handles to the last valid record, record only session ID/start time and boundary, compare the mapped CWD by physical directory identity, and compare the cursor:

~~~go
if current.LastLine > upper.Line || (current.LastLine == upper.Line && current.LastHash != upper.SourceHash) {
    return nil, prepare.ErrCursorSourceDrift
}
if current.LastLine < upper.Line { pending = append(pending, frozen) }
~~~

Do not persist candidate paths. Discovery issues for the mapped project fail closed; unrelated issues remain internal warnings.

- [ ] **Step 3: Make prepare stop and authenticate the frozen upper boundary**

`prepare.Run` must ignore records after `UpperBoundary.Line`, verify the source hash at that line, and compute `HasMore` relative to the frozen line—not the live EOF. A missing or changed frozen line returns `ErrCursorSourceDrift` before writing a usable packet.

- [ ] **Step 4: Run append/drift stress GREEN**

Run: `gofmt -w internal/reviewjob internal/prepare && go test -race ./internal/reviewjob ./internal/prepare -run 'Test(Freeze|PrepareHonorsFrozen)' -count=20`

Expected: PASS; appended records never enter the frozen job and remain pending on the next freeze.

- [ ] **Step 5: Commit**

~~~bash
git add internal/reviewjob/freeze.go internal/reviewjob/freeze_test.go internal/prepare/prepare.go internal/prepare/prepare_test.go
git commit -m "feat: freeze pending session review boundaries"
~~~

---

### Task 5: Define the Provider-Neutral Agent Contract and Versioned Prompt

**Files:**
- Create: `internal/agent/agent.go`
- Create: `internal/agent/agent_test.go`
- Create: `internal/reviewprompt/prompt.go`
- Create: `internal/reviewprompt/prompt_test.go`
- Create: `internal/reviewprompt/testdata/prompt.golden.txt`

**Interfaces:**
- Consumes: `evidence.Packet`, accepted `reviewv2.State`, proposal JSON Schema.
- Produces: `agent.Adapter`, `agent.Capability`, `agent.Request`, `agent.Result`, typed safe errors, and `reviewprompt.Build`.

- [ ] **Step 1: Write RED contract tests**

~~~go
type Adapter interface {
    Verify(context.Context, string) (Capability, error)
    GenerateProposal(context.Context, Request) (Result, error)
    Cancel(context.Context) error
}
type Request struct {
    Prompt []byte
    OutputSchema []byte
    WorkingDirectory string
    Deadline time.Time
}
type Result struct {
    Proposal []byte
    Model string
    Usage accounting.TokenUsage
}
~~~

Prompt tests assert byte stability, size limits, exact packet digest, accepted-context allowlist, untrusted-evidence delimiters, proposal-only instructions, and absence of paths, secrets, plugin instructions, or unrelated accepted documents.

Run: `go test ./internal/agent ./internal/reviewprompt -count=1`

Expected: FAIL because both packages are missing.

- [ ] **Step 2: Implement the minimal contract and prompt builder**

Adapter errors carry an internal cause but expose only one of `E_AGENT_UNCONFIGURED`, `E_AGENT_INCOMPATIBLE`, `E_AGENT_AUTH`, `E_AGENT_BUSY`, `E_AGENT_TIMEOUT`, `E_AGENT_TOOL_FORBIDDEN`, or `E_AGENT_CANCELLED`. The prompt contains the exact schema/invariants and marks packet/context as data, never instructions.

- [ ] **Step 3: Run GREEN and commit**

Run: `gofmt -w internal/agent internal/reviewprompt && go test ./internal/agent ./internal/reviewprompt -count=1`

~~~bash
git add internal/agent internal/reviewprompt
git commit -m "feat: define proposal-only agent boundary"
~~~

---

### Task 6: Verify and Run Codex with No Tools

**Files:**
- Create: `internal/agent/codex/verify.go`
- Create: `internal/agent/codex/run.go`
- Create: `internal/agent/codex/process_unix.go`
- Create: `internal/agent/codex/process_windows.go`
- Create: `internal/agent/codex/codex_test.go`
- Create: `internal/agent/codex/testdata/fake-agent.go`

**Interfaces:**
- Consumes: absolute Codex executable, `agent.Request`, context cancellation.
- Produces: `codex.Adapter.Verify`, `GenerateProposal`, `Cancel`, parsed model/usage, and safe error mapping.

- [ ] **Step 1: Write a fake executable and RED matrix**

The fake modes emit success, malformed JSONL, missing final response, schema-invalid proposal, tool-call event, auth error, timeout, ignored SIGTERM child, huge stdout/stderr, and exit-after-valid-output. Tests assert bounded buffers, process-tree cleanup, and that stderr never enters public errors.

Run: `go test ./internal/agent/codex -count=1`

Expected: FAIL because the adapter is missing.

- [ ] **Step 2: Implement fail-closed verification**

`Verify` requires an absolute regular executable, records a physical file identity, runs `--version`, `exec --help`, `features list`, and a harmless `debug prompt-input` probe. The initial supported contract is `codex-cli >=0.147.0,<0.148.0`; a later version is incompatible until its capability fixture is reviewed. Verification requires all flags used below and the expected stable feature names.

- [ ] **Step 3: Implement one fixed invocation**

~~~go
args := []string{
    "exec", "--ephemeral", "--ignore-user-config", "--ignore-rules",
    "--sandbox", "read-only", "--json", "--color", "never",
    "--skip-git-repo-check", "--output-schema", schemaPath,
    "--disable", "shell_tool", "--disable", "apps",
    "--disable", "browser_use", "--disable", "browser_use_external",
    "--disable", "browser_use_full_cdp_access", "--disable", "computer_use",
    "--disable", "image_generation", "--disable", "workspace_dependencies",
    "--disable", "skill_search", "--disable", "remote_plugin",
    "-",
}
~~~

Do not add `--model`, `--profile`, `--add-dir`, approval flags, or bypass flags. Pass the prompt only on stdin. Use a private per-job work directory outside Project/Vault as `Cmd.Dir`. Reject any JSONL event whose normalized kind is a tool request/call, even if Codex later returns a valid proposal.

- [ ] **Step 4: Implement native cancellation**

Unix starts a process group and sends TERM then KILL after the bounded grace period. Windows creates a new process group and uses a Job Object with kill-on-close. `Cancel` is idempotent and never targets a PID whose start token differs.

- [ ] **Step 5: Run GREEN, race, and cross-build**

Run: `gofmt -w internal/agent/codex && go test -race ./internal/agent/codex -count=20 && GOOS=windows GOARCH=amd64 go test -c -o "$(mktemp -d)/codex.test.exe" ./internal/agent/codex`

Expected: PASS; forbidden-tool mode returns `E_AGENT_TOOL_FORBIDDEN`, and timeout/cancel leave no fake child alive.

- [ ] **Step 6: Commit**

~~~bash
git add internal/agent/codex
git commit -m "feat: run Codex as a no-tools proposal worker"
~~~

---

### Task 7: Record Review-Run Usage Without Double Counting

**Files:**
- Create: `internal/reviewjob/accounting.go`
- Create: `internal/reviewjob/accounting_test.go`
- Create: `internal/reviewjob/pricing_catalog.go`
- Create: `internal/reviewjob/pricing_catalog_test.go`
- Modify: `internal/accounting/accounting.go`
- Modify: `internal/accounting/accounting_test.go`

**Interfaces:**
- Consumes: model and actual token counts from `agent.Result`, injected `PricingResolver`.
- Produces: `ReviewAccounting` plus reusable `accounting.PriceUsage` validation/math.

- [ ] **Step 1: Write RED tests**

Test multiple packets/models, cached tokens, unknown price, overflow/non-finite values, stable totals, and proof that `reviewv2.MachineLedger.Accounting` is byte-identical before/after review-run accounting.

Run: `go test ./internal/accounting ./internal/reviewjob -run 'Test(PriceUsage|ReviewAccounting)' -count=1`

Expected: FAIL with missing pricing API.

- [ ] **Step 2: Implement injected price resolution**

~~~go
type PricingResolver interface {
    Resolve(model string, at time.Time) (accounting.Pricing, bool)
}
type ReviewAccounting struct {
    Models []accounting.ModelAccounting `json:"models"`
    TotalTokens int64 `json:"total_tokens"`
    TotalCostUSD *float64 `json:"total_cost_usd,omitempty"`
    PricingComplete bool `json:"pricing_complete"`
}
~~~

Use `pricing_catalog.go` for one checked-in, date-stamped Codex model price catalog populated from the same official public-list-price convention already documented by SessionReviewer. The catalog contains every model advertised by the supported Codex compatibility fixture and validates HTTPS source/as-of/rates at startup. Unknown models retain actual usage with `PricingComplete=false` and no invented cost; the UI says `费用暂不可用`. Tests inject exact prices and verify math.

- [ ] **Step 3: Run GREEN and commit**

Run: `gofmt -w internal/accounting internal/reviewjob && go test ./internal/accounting ./internal/reviewjob -run 'Test(PriceUsage|ReviewAccounting)' -count=1`

~~~bash
git add internal/accounting internal/reviewjob/accounting.go internal/reviewjob/accounting_test.go internal/reviewjob/pricing_catalog.go internal/reviewjob/pricing_catalog_test.go
git commit -m "feat: account for automatic review usage separately"
~~~

---

### Task 8: Extract Deterministic Sync and Implement the Happy-Path Worker

**Files:**
- Create: `internal/syncproject/service.go`
- Create: `internal/syncproject/service_test.go`
- Modify: `internal/cli/sync.go`
- Modify: `internal/cli/sync_test.go`
- Create: `internal/reviewjob/service.go`
- Create: `internal/reviewjob/service_test.go`

**Interfaces:**
- Consumes: frozen job, injected `Prepare`, `AgentAdapter`, `Apply`, and `Sync` functions.
- Produces: `reviewjob.Run(context.Context, RunOptions) error` and reusable `syncproject.Run(context.Context, Options) (sync.Report, error)` imported by both `internal/cli` and `internal/reviewjob`.

- [ ] **Step 1: Write RED seam tests**

Assert the existing `sync` command delegates to `SyncProject` without changing output. Worker tests use fakes to assert exact sequence:

~~~text
prepare(s1,p1) → propose → apply → sync
prepare(s1,p2) → propose → apply → sync
prepare(s2,p1) → propose → apply → sync → completed
~~~

Also assert no-pending performs exactly one sync and never calls the Agent.

Run: `go test ./internal/syncproject ./internal/cli ./internal/reviewjob -run 'Test(SyncProjectService|WorkerHappyPath|WorkerNoPending)' -count=1`

Expected: FAIL with missing service seams.

- [ ] **Step 2: Extract sync without changing semantics**

Move mapping/engine construction and `Reconcile` into `internal/syncproject/service.go`. Its `Options` contains explicit `ProjectID`, `CWD`, absolute `DataDir`, `GOOS`, `Now`, and `Trigger`; it loads/authenticates the mapping and returns `sync.Report` without formatting. Keep `runSync` in `internal/cli` responsible for platform-default data-dir resolution, formatting, exit codes, partial-error behavior, and current tests.

- [ ] **Step 3: Implement orchestration with explicit commit boundaries**

For every packet: persist `preparing`; write evidence to private job work storage; persist `reviewing`; generate proposal; persist its digest only; persist `applying`; call `apply.Run`; persist accepted cursor/count; persist `syncing`; reconcile; only then advance session position. Remove packet/proposal bytes after each accepted sync.

- [ ] **Step 4: Run GREEN and ordering stress**

Run: `gofmt -w internal/syncproject internal/cli internal/reviewjob && go test -race ./internal/syncproject ./internal/cli ./internal/reviewjob -run 'Test(SyncProjectService|WorkerHappyPath|WorkerNoPending)' -count=20`

Expected: PASS with exactly one sync after each apply.

- [ ] **Step 5: Commit**

~~~bash
git add internal/syncproject/service.go internal/syncproject/service_test.go internal/cli/sync.go internal/cli/sync_test.go internal/reviewjob/service.go internal/reviewjob/service_test.go
git commit -m "feat: orchestrate review apply and sync"
~~~

---

### Task 9: Add Stop-on-Failure, Cancellation, and Retry Recovery

**Files:**
- Modify: `internal/reviewjob/service.go`
- Create: `internal/reviewjob/recovery.go`
- Create: `internal/reviewjob/recovery_test.go`
- Modify: `internal/reviewjob/service_test.go`

**Interfaces:**
- Consumes: safe Agent/apply/sync errors, cancel flag, apply receipt state, accepted cursor.
- Produces: terminal/retry transitions and contextual `CanSyncOnly`.

- [ ] **Step 1: Write the RED failure matrix**

Cases: preflight/discovery, auth, incompatible, timeout, forbidden tool, malformed proposal, proposal rejection, interrupted apply receipt, sync conflict, partial sync, cancel during review, cancel requested during apply, cancel requested during sync, and later-session failure after earlier success.

Run: `go test ./internal/reviewjob -run 'Test(WorkerFailure|Cancel|Retry|Recovery)' -count=1`

Expected: FAIL until recovery policy is implemented.

- [ ] **Step 2: Implement fixed mappings and safe points**

Before apply, cancellation calls `Adapter.Cancel` and discards unaccepted bytes. During apply/sync, set `cancel_requested`, finish the typed call, persist its outcome, then stop. Proposal validation failure maps to `E_PROPOSAL_REJECTED`; uncertain receipt maps to `E_APPLY_RECOVERY`; sync conflicts/partial reports map to their exact codes. `CanSyncOnly` is true only when accepted Project changes may exist and deterministic sync is safe.

- [ ] **Step 3: Implement retry from accepted state**

Retry increments `Attempt`, reauthenticates project/Agent identities, reacquires leases, runs existing apply receipt recovery before preparing, reloads the accepted cursor, discards stale proposal bytes, and resumes the frozen queue. It never refreezes new sessions into the old job.

- [ ] **Step 4: Run GREEN and commit**

Run: `gofmt -w internal/reviewjob && go test -race ./internal/reviewjob -run 'Test(WorkerFailure|Cancel|Retry|Recovery)' -count=20`

~~~bash
git add internal/reviewjob/service.go internal/reviewjob/service_test.go internal/reviewjob/recovery.go internal/reviewjob/recovery_test.go
git commit -m "feat: recover and cancel automatic reviews safely"
~~~

---

### Task 10: Add the Review CLI Control Plane and Detached Worker

**Files:**
- Create: `internal/cli/review.go`
- Create: `internal/cli/review_test.go`
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/run_test.go`
- Create: `internal/cli/detach_unix.go`
- Create: `internal/cli/detach_windows.go`

**Interfaces:**
- Consumes: the approved public commands and private `review worker --job-id` entry.
- Produces: bounded JSON for verify/start/status/cancel/retry and a detached one-shot worker.

- [ ] **Step 1: Write RED command-contract tests**

Test exact help, mutual exclusions, absolute executable requirement, safe IDs, malformed/extra flags, JSON schema validation, busy idempotency, worker launch failure rollback, and absence of paths/raw errors. The private worker command must require an unguessable launch token stored in the job record.

Run: `go test ./internal/cli -run 'TestRunReview' -count=1`

Expected: FAIL because `review` is unknown.

- [ ] **Step 2: Implement exact public parsing**

Support only:

~~~text
review agent verify --executable ABSOLUTE_PATH --json
review start --project-id ID --agent-executable ABSOLUTE_PATH --json
review status --project-id ID --json
review cancel --job-id ID --json
review retry --job-id ID --agent-executable ABSOLUTE_PATH --json
~~~

Every syntactically valid public `--json` review command writes exactly one canonical safe JSON object to stdout on both success and operational failure; operational failure exits 1 with empty stderr, while usage/flag errors exit 2 with no JSON. `agent verify` uses `{schema_version, kind, compatible, version?, error_code?}`. Project/job operations use the public status schema, including an idle/error projection when preflight fails before job creation.

`start` validates mapping/identity and Agent compatibility before job creation. It freezes sessions before spawning. The child writes a one-byte success/failure handshake through a private inherited pipe only after it verifies the launch token, acquires both leases, and persists worker ownership; the parent has a short bounded handshake deadline. Return success only after job/pointer/launch intent are durable and the worker handshake succeeds. A failed/busy handshake atomically marks the job failed and returns the safe code, so a second start never reports a queued job that cannot own the worker.

- [ ] **Step 3: Implement detached launch safely**

Re-exec the current SessionReviewer binary with exact private argv, stdin closed, stdout/stderr directed to the operating-system null device, the inherited handshake handle, and platform detachment flags. Worker acquires leases and verifies the launch token before changing state. Internal failures are reduced to the safe code plus a bounded private diagnostic in the authenticated job record; raw worker streams are not retained.

- [ ] **Step 4: Run process and cross-platform GREEN**

Run: `gofmt -w internal/cli && go test -race ./internal/cli -run 'TestRunReview' -count=20 && GOOS=windows GOARCH=amd64 go test -c -o "$(mktemp -d)/cli.test.exe" ./internal/cli`

Expected: PASS; `start` returns promptly while the fixture worker completes through `status`.

- [ ] **Step 5: Commit**

~~~bash
git add internal/cli/review.go internal/cli/review_test.go internal/cli/detach_unix.go internal/cli/detach_windows.go internal/cli/run.go internal/cli/run_test.go
git commit -m "feat: expose durable review job commands"
~~~

---

### Task 11: Extend the Obsidian Runner and Settings

**Files:**
- Modify: `obsidian-plugin/src/cli/runner.ts`
- Modify: `obsidian-plugin/src/cli/settings.ts`
- Modify: `obsidian-plugin/src/main.ts`
- Modify: `obsidian-plugin/tests/cli.test.ts`
- Modify: `obsidian-plugin/tests/settings.test.ts`
- Modify: `obsidian-plugin/tests/main.test.ts`

**Interfaces:**
- Consumes: public review JSON and two absolute executable paths.
- Produces: typed `ReviewStatus`, `verifyAgent`, `startReview`, `reviewStatus`, `cancelReview`, `retryReview`, and `syncProject`.

- [ ] **Step 1: Write RED allowlist/parser/settings tests**

Assert exact argv, `shell:false`, fixed control timeout, larger but bounded status output, absolute paths only, project/job regexes, unknown JSON field tolerance with required-field validation, arbitrary `run` rejection, and persistence of `cliPath` plus `codexPath` without API keys.

Run: `cd obsidian-plugin && npm test -- --run tests/cli.test.ts tests/settings.test.ts tests/main.test.ts`

Expected: FAIL because review methods/settings are absent.

- [ ] **Step 2: Implement typed methods only**

~~~ts
export interface ReviewStatus {
  schemaVersion: 1;
  jobId?: string;
  projectId: string;
  state: "idle"|"queued"|"running"|"completed"|"failed"|"cancel_requested"|"cancelled"|"retrying";
  phase?: "preflight"|"scanning"|"preparing"|"reviewing"|"applying"|"syncing";
  attempt: number; sessionIndex: number; sessionCount: number;
  acceptedPackets: number; acceptedSessions: number;
  errorCode?: string; canRetry: boolean; canCancel: boolean; canSyncOnly: boolean;
  reviewUsage?: { totalTokens: number; totalCostUsd?: number; pricingComplete: boolean };
}
~~~

The parser explicitly maps the CLI's snake_case JSON keys into this TypeScript shape and rejects missing/wrong required values. A private `runJSON` parses safe stdout even when `execFile` reports exit 1 and otherwise substitutes a fixed generic failure; it never forwards CLI stderr/error text to the view. Keep raw `run` internal/private so UI code cannot supply arbitrary argv. Add `sync --project-id ID` as one exact action.

- [ ] **Step 3: Add Codex verification to the existing CLI settings section**

Use a separate absolute path field and `review agent verify`; save only after verification. Keep the current heading/layout and description style.

- [ ] **Step 4: Run GREEN and commit**

Run: `cd obsidian-plugin && npm test -- --run tests/cli.test.ts tests/settings.test.ts tests/main.test.ts && npm run build`

~~~bash
git add obsidian-plugin/src/cli/runner.ts obsidian-plugin/src/cli/settings.ts obsidian-plugin/src/main.ts obsidian-plugin/tests/cli.test.ts obsidian-plugin/tests/settings.test.ts obsidian-plugin/tests/main.test.ts
git commit -m "feat: connect Obsidian to review jobs"
~~~

---

### Task 12: Add the Header Action and Recoverable Job Banner Without Layout Drift

**Files:**
- Modify: `obsidian-plugin/src/view/render-shell.ts`
- Modify: `obsidian-plugin/src/view/project-view.ts`
- Modify: `obsidian-plugin/src/view/status-banner.ts`
- Modify: `obsidian-plugin/styles.css`
- Modify: `obsidian-plugin/tests/view.test.ts`
- Modify: `obsidian-plugin/tests/styles.test.ts`
- Modify: `obsidian-plugin/tests/accessibility.test.ts`
- Create: `obsidian-plugin/tests/review-job-view.test.ts`

**Interfaces:**
- Consumes: typed runner methods and `ReviewStatus`.
- Produces: one `总结并同步` header action, bounded polling, cancel/retry/sync-only banner actions.

- [ ] **Step 1: Snapshot the current no-job DOM and write RED UI tests**

Lock the existing child order/selectors and assert only one new node appears inside `.sr-header-meta`. Test idle/running/completed/failed/reload states, polling teardown, project switch, double-click suppression, fixed Chinese messages, live region, focus, and contextual `仅同步已有修改`.

Run: `cd obsidian-plugin && npm test -- --run tests/view.test.ts tests/review-job-view.test.ts tests/accessibility.test.ts`

Expected: FAIL because the action/status UI is absent.

- [ ] **Step 2: Inject the action without restructuring `renderReadyView`**

Extend the renderer with an optional header-action descriptor. Do not move or wrap current header identity/meta children. `ProjectEvolutionView` owns start/cancel/retry/sync callbacks and prepends the review banner through the existing banner area.

- [ ] **Step 3: Implement bounded polling and reload recovery**

Poll only while queued/running/retrying/cancel-requested, with one in-flight request, 1s→2s→5s backoff, immediate stop on close/project switch, and refresh repository data on terminal change. Idle refresh performs one status call but no session scan.

Use fixed phase labels `正在检查` / `正在扫描会话` / `正在准备材料` / `正在总结` / `正在写入` / `正在同步`. Use these exact safe failures:

| Code | Message |
|---|---|
| `E_AGENT_UNCONFIGURED` | `尚未配置 Codex，请先在设置中验证。` |
| `E_AGENT_INCOMPATIBLE` | `当前 Codex 版本暂不兼容自动总结。` |
| `E_AGENT_AUTH` | `Codex 登录已失效，请先在终端重新登录。` |
| `E_AGENT_BUSY` | `已有自动总结任务正在运行。` |
| `E_AGENT_TIMEOUT` | `自动总结等待超时，可重试。` |
| `E_AGENT_TOOL_FORBIDDEN` | `自动总结尝试调用工具，已安全停止。` |
| `E_AGENT_CANCELLED` | `自动总结已取消。` |
| `E_PROPOSAL_REJECTED` | `总结结果未通过校验，未写入项目。` |
| `E_APPLY_RECOVERY` | `写入状态需要恢复，请重试。` |
| `E_SYNC_CONFLICT` | `已总结，但同步存在冲突，请先处理冲突。` |
| `E_SYNC_PARTIAL` | `已总结，但部分内容尚未同步。` |

- [ ] **Step 4: Add only scoped styles**

Add `.sr-review-action`, `.sr-review-banner`, `.sr-review-actions`, and `.sr-review-meta`. Do not modify existing selector declarations or the 860px breakpoint/card grids. New narrow rules only wrap controls within `.sr-header-meta`.

- [ ] **Step 5: Run visual-regression GREEN**

Run: `cd obsidian-plugin && npm test -- --run tests/view.test.ts tests/review-job-view.test.ts tests/accessibility.test.ts tests/styles.test.ts`

Expected: PASS at desktop, 860px, and 390px fixture widths with no horizontal overflow and unchanged existing geometry assertions.

- [ ] **Step 6: Commit**

~~~bash
git add obsidian-plugin/src/view/render-shell.ts obsidian-plugin/src/view/project-view.ts obsidian-plugin/src/view/status-banner.ts obsidian-plugin/styles.css obsidian-plugin/tests/view.test.ts obsidian-plugin/tests/styles.test.ts obsidian-plugin/tests/accessibility.test.ts obsidian-plugin/tests/review-job-view.test.ts
git commit -m "feat: add Obsidian summarize and sync action"
~~~

---

### Task 13: Add End-to-End Security, Recovery, and Documentation Gates

**Files:**
- Create: `test/reviewjob/acceptance_test.go`
- Create: `test/reviewjob/testdata/fake-codex.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `obsidian-plugin/tests/package.test.ts`

**Interfaces:**
- Consumes: packaged CLI/plugin, fake Codex, isolated Project/Vault/data/session fixtures.
- Produces: reproducible acceptance evidence for complete, no-op, stop-first, cancellation, recovery, and redaction flows.

- [ ] **Step 1: Write RED end-to-end scenarios**

Scenarios prove: multiple chronological sessions; active click boundary; bounded successor packets; immediate per-packet sync; no-pending sync without Agent; later failure preserves earlier accepted/synced state; cancel/retry; restart recovery; forbidden tool; exact repeated no-op; review usage separation; and public-output secret/path canaries.

Run: `go test ./test/reviewjob -count=1`

Expected: FAIL until the full assembled flow satisfies the contract.

- [ ] **Step 2: Document the user and recovery contract**

Chinese and English README sections explain `总结并同步`, Codex path verification, sent information, no-tools/write boundary, source-vs-review accounting, stop-first behavior, cancellation/retry, contextual sync-only, and manual Skill fallback. State explicitly that there is no watcher/daemon and no other v1 adapter.

- [ ] **Step 3: Add CI gates**

Add focused reviewjob/Codex race tests, Windows test-binary compilation, fake-Agent acceptance, public schema/package checks, and deterministic plugin packaging. Never run a real authenticated Codex call in CI; keep `SESSION_REVIEWER_CODEX_SMOKE=1` as an opt-in local test.

- [ ] **Step 4: Run the complete repository gate**

~~~bash
go test ./... -count=1
go test -race ./internal/reviewjob ./internal/agent/codex ./internal/cli -count=1
go vet ./...
go mod tidy -diff
(cd obsidian-plugin && npm run check)
./scripts/build-obsidian-plugin.sh 0.2.5 "$(mktemp -d)"
GOOS=darwin GOARCH=amd64 go build -o "$(mktemp -d)/session-reviewer" ./cmd/session-reviewer
GOOS=darwin GOARCH=arm64 go build -o "$(mktemp -d)/session-reviewer" ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o "$(mktemp -d)/session-reviewer.exe" ./cmd/session-reviewer
git diff --check
~~~

Expected: every command passes; `git status --short` contains only intended implementation files plus the user's pre-existing unrelated changes.

- [ ] **Step 5: Run credential and public-output leak scans**

Run repository high-entropy/credential checks already used by release CI, plus:

~~~bash
rg -n '/Users/|AppData|source_hash|private_error|prompt|stdout|stderr' schemas/review-job-status-v1.schema.json obsidian-plugin/src
~~~

Expected: no private field is part of the public status/plugin contract; intentional test canaries are confined to tests.

- [ ] **Step 6: Commit**

~~~bash
git add test/reviewjob .github/workflows/ci.yml README.md README.zh-CN.md obsidian-plugin/tests/package.test.ts
git commit -m "test: gate automatic review orchestration"
~~~

---

### Task 14: Validate the Installed Bundle in Real Obsidian

**Files:**
- Verify only: built `main.js`, `manifest.json`, `styles.css`
- Verify only: installed `NeoMei-Docs/.obsidian/plugins/session-reviewer/`
- Record evidence in the implementation task report; do not add screenshots, Vault content, sessions, or private job data to Git.

**Interfaces:**
- Consumes: candidate plugin bundle, local CLI, verified Codex executable, authorized real Project/Vault mapping.
- Produces: real UI/runtime acceptance evidence; no release or publish.

- [ ] **Step 1: Capture the pre-install baseline**

Record title/goal/resume/risks/tabs/timeline-detail/decision/usage/editing/project-picker behavior and bounding boxes at desktop and 390px-equivalent width. Record the currently installed plugin version and checksums before copying anything.

- [ ] **Step 2: Install the candidate bundle and reload Obsidian**

Copy only the three packaged plugin assets into the exact plugin directory, retaining the captured prior bundle as a recoverable backup outside the Vault. Confirm the loaded asset checksums match the candidate.

- [ ] **Step 3: Prove the existing interface is unchanged**

Repeat every baseline interaction and geometry check. Verify header action wrapping introduces no horizontal overflow and that editing/status/conflict controls still work.

- [ ] **Step 4: Prove the real job flow**

Verify the real Codex executable, run one safe fixture job, then one authorized real `总结并同步` job. Confirm frozen session order, accepted packet/session counts, cursor advancement, review-run usage separation, Project/Vault convergence, and a repeated no-op click that performs deterministic sync without launching Codex.

- [ ] **Step 5: Prove cancellation and recovery**

Use the safe fake/fixture delay to cancel during review, then exercise one recoverable failure and retry. Confirm no stale lease/process/banner remains after reload.

- [ ] **Step 6: Final verification commit only if evidence required code/doc corrections**

If acceptance reveals a defect, return to the relevant TDD task, add a regression test, fix it, rerun Tasks 13–14, and commit that focused correction. Otherwise do not create an empty commit.

## Spec Coverage Map

| Approved contract | Implemented and proved by |
|---|---|
| Durable one-shot job; no daemon/watcher | Tasks 2, 3, 10, 13 |
| All click-time pending sessions, including active boundary | Task 4 and Task 13 |
| Codex only behind `AgentAdapter`; no original-session resume | Tasks 5 and 6 |
| Local auth/default model; no Obsidian API key | Tasks 6 and 11 |
| Proposal-only/no-tools/no-write boundary | Tasks 5, 6, and 13 |
| Automatic apply plus immediate sync | Task 8 |
| Stop first; retain earlier accepted/synced work | Tasks 8, 9, and 13 |
| No-pending deterministic sync | Tasks 8, 12, and 13 |
| Cancel/retry/apply-receipt recovery | Tasks 9, 10, and 13 |
| Private state and redacted public status | Tasks 1, 2, 10, and 13 |
| Separate review-run usage/list-price accounting | Task 7 and Task 13 |
| Single permanent action and contextual sync-only | Tasks 11 and 12 |
| Preserve current Obsidian interface | Task 12 and Task 14 |
| macOS/Windows/process/package gates | Tasks 6, 10, and 13 |
| Real installed-bundle acceptance | Task 14 |

## Final Completion Checklist

- [ ] Every confirmed design decision maps to at least one passing automated test.
- [ ] Public status schema contains no private path/content fields.
- [ ] Freeze authentication proves active-session append behavior.
- [ ] Codex compatibility and no-tools enforcement fail closed.
- [ ] Apply/cursor/sync remain trusted Go-owned boundaries.
- [ ] Cancellation never interrupts apply/sync commit windows.
- [ ] Existing Obsidian DOM order, selectors, grids, and interactions remain intact.
- [ ] Source usage cards do not include review-run usage.
- [ ] Full Go/plugin/race/vet/tidy/cross-build/package gates pass.
- [ ] Real installed-bundle Obsidian acceptance passes.
- [ ] No release, publish, deployment, watcher, or extra Agent adapter was performed.
