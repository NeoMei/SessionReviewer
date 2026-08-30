# Multi-agent SessionReviewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Codex, Claude Code, and OpenCode equal SessionReviewer collection and in-agent review experience, with one ledger, durable cross-entrypoint ownership, honest partial pricing, and a configurable Obsidian proposal worker.

**Architecture:** Provider adapters expose opaque candidates and closable canonical-record readers. Codex and Claude read authenticated JSONL; OpenCode invokes only its documented bounded JSON CLI. In-agent review owns a durable run across separate CLI calls, while Obsidian keeps its one-shot job; both coordinate through one owner registry. The clean release cuts directly to evidence v3, proposal v2, and ledger v3 with no legacy aliases or migration path.

**Tech Stack:** Go 1.26, existing `pathguard`/`atomicfile`/`cursor`/`prepare`/`apply`/`reviewjob`, Claude Code CLI, OpenCode CLI JSON export, TypeScript 5.8, Obsidian 1.13, Vitest. No SQLite dependency.

**Spec:** `docs/superpowers/specs/2026-08-30-multi-agent-session-review-design.md`

## Global Constraints

- Canonical session IDs only: `codex.<native>`, `claude.<uuid>`, `opencode.<ses_...>`. Do not add aliases or auto-prefix arbitrary input.
- Final contracts are evidence v3, proposal v2, and ledger v3. Old packets, proposals, ledgers, cursors, and `codexPath` settings are rejected, not migrated.
- Exact tokens survive missing prices. Per-model price/cost is optional; aggregate cost exists only when `pricing_complete=true`.
- Skills receive only prepared evidence. They never read Codex/Claude JSONL, OpenCode list/export JSON, or source paths.
- OpenCode collection uses `session list --format json --pure` and `export SESSION_ID --pure` only. Never open `opencode.db`.
- OpenCode streams only a stable finalized prefix. Pending/running tools and unfinished assistant turns remain pending.
- In-agent begin, prepare, apply, sync, complete, and abort all coordinate through the same durable owner registry as Obsidian jobs.
- Provider dispatch stays at the CLI composition root. `reviewjob.Run` continues to receive a verified `*AgentHandle` and does not import provider implementations.
- Each task ends with `gofmt -w .`, `go test ./...`, `go vet ./...`, and `go mod tidy -diff` before commit. Plugin-changing tasks also run `npm run check` in `obsidian-plugin`.
- Tests use temporary roots, trimmed fixtures, and fake executables. Never read live user session data.
- Preserve unrelated dirty-worktree files. Stage only paths listed by the current task.

Tasks 1-3 form the first delivery slice. Tasks 1 and 2 may add/cut over foundations while Codex still uses its current locator, but they add no compatibility reader; Task 3 completes canonical IDs and records before any multi-agent feature is considered shippable. Every intermediate commit still passes the full repository gates.

## File Structure and Ownership

- `internal/sessionid/`: canonical provider-prefixed IDs.
- `internal/source/`: canonical records, opaque candidates, reader contract, manager, and source diagnostics.
- `internal/source/codex/`: Codex locator/envelope translation.
- `internal/source/claude/`: narrowed Claude discovery and JSONL translation.
- `internal/source/opencode/`: bounded documented-CLI invocation, export validation, stable-prefix translation.
- `internal/reviewowner/`: durable global/project owner record and reserve/touch/release/recovery operations.
- `internal/reviewv3/`: current machine ledger, Markdown, validation, and sync-facing types. Delete `internal/reviewv2/` at cutover.
- `internal/agent/claude/`: fixed-argv isolated Claude proposal adapter.
- `skill/session-reviewer/hosts/`: host installation entrypoints; shared scripts and schemas remain single-source.

---

### Task 1: Canonical IDs and Provider-neutral Reader Contract

**Files:**
- Create: `internal/sessionid/sessionid.go`
- Create: `internal/sessionid/sessionid_test.go`
- Create: `internal/source/record.go`
- Create: `internal/source/adapter.go`
- Create: `internal/source/adapter_test.go`

**Interfaces:**

```go
type Provider string

const (
    ProviderCodex Provider = "codex"
    ProviderClaude Provider = "claude"
    ProviderOpenCode Provider = "opencode"
)

type ID struct {
    Provider Provider
    Native string
}

func Parse(string) (ID, error)
func PrefixNative(Provider, string) (ID, error)
func Valid(string) bool
func (ID) String() string

type RecordType string

const (
    RecordMessage RecordType = "message"
    RecordToolCall RecordType = "tool_call"
    RecordToolResult RecordType = "tool_result"
    RecordCWDChange RecordType = "cwd_change"
    RecordUsage RecordType = "usage"
    RecordSkip RecordType = "skip"
)

type Record struct {
    Sequence int
    Timestamp time.Time
    Type RecordType
    Payload json.RawMessage
    SourceHash string
}

type DecodeOptions struct {
    FromSequence int
    MaxRecordBytes int
}

type Summary struct {
    Records int
    Malformed int
}

type Project struct {
    ID string
    Root string
    Identity pathguard.IdentityToken
}

type Candidate struct {
    ID sessionid.ID
    CWD string
    StartedAt time.Time
    ref candidateRef // unexported and never serialized
}

type candidateRef struct {
    Provider sessionid.Provider
    Key string
}

func NewCandidate(sessionid.ID, string, time.Time, string) (Candidate, error)
func (Candidate) KeyFor(sessionid.Provider) (string, error)

type Discovery struct {
    Candidates []Candidate
    Issues []Issue
}

type Issue struct {
    SessionID sessionid.ID
    Err error
}

type Reader interface {
    Stream(context.Context, DecodeOptions, func(Record) error) (Summary, error)
    Close() error
}

type Adapter interface {
    Kind() sessionid.Provider
    Discover(context.Context, Project) (Discovery, error)
    Open(context.Context, Candidate) (Reader, error)
}
```

`Record.Type` is exactly `message|tool_call|tool_result|cwd_change|usage|skip`. The constructor seals the provider plus private key in `candidateRef`; `KeyFor` returns it only to the matching provider. `Open` must revalidate it.

- [ ] **Step 1: Write canonical-ID and interface invariant tests**

```go
func TestCanonicalIDs(t *testing.T) {
    for _, value := range []string{
        "codex.s1",
        "claude.f981b686-02f8-414f-80a3-2bb191c489ed",
        "opencode.ses_fb2c251beffebUIc5MHYz5gCpz",
    } {
        if _, err := Parse(value); err != nil { t.Fatalf("%s: %v", value, err) }
    }
    for _, value := range []string{"s1", "CODEX.s1", "codex.", "gemini.s1", "codex.has space"} {
        if _, err := Parse(value); err == nil { t.Fatalf("accepted %q", value) }
    }
}
```

Add compile-time fake adapters/readers proving orchestration can stream and close without `os.File` or DB fields.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/sessionid ./internal/source -count=1`
Expected: FAIL because the packages do not exist.

- [ ] **Step 3: Implement the minimal ID, record, adapter, and reader types**

Split IDs on the first dot. Native charset is `[A-Za-z0-9._-]+`, excluding empty, `.`, `..`, and Windows reserved basenames. Record validation requires a positive sequence and lowercase SHA-256 source hash; the manager's reader wrapper rejects a non-increasing stream.

- [ ] **Step 4: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./internal/sessionid ./internal/source -count=1
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 5: Commit**

```bash
git add internal/sessionid internal/source
git commit -m "feat: define canonical session source contracts"
```

---

### Task 2: Clean v3 Accounting and JSON Contract Cutover

**Files:**
- Create: `schemas/evidence-v3.schema.json`
- Create: `schemas/proposal-v2.schema.json`
- Create: `schemas/review-ledger-v3.schema.json`
- Delete: `schemas/evidence-v2.schema.json`, `schemas/proposal-v1.schema.json`, `schemas/review-ledger-v2.schema.json`
- Rename/modify: `internal/reviewv2/` -> `internal/reviewv3/`
- Modify: `internal/accounting/accounting.go` and tests
- Modify: `internal/evidence/`, `internal/proposal/`, `internal/reviewprompt/`, `internal/apply/`, `internal/recovery/`, `internal/sync/`, `internal/syncdoc/`, `internal/project/`
- Modify: `internal/buildinfo/buildinfo.go`
- Modify: `skill/session-reviewer/references/` and package tests
- Modify: `cmd/release-packager/`
- Modify: `obsidian-plugin/src/contracts/`, `obsidian-plugin/src/data/`, usage presentation, and tests
- Modify: `.github/workflows/ci.yml` and `.github/workflows/release.yml`
- Delete after rename: `internal/reviewv3/migrate.go`, `migrate_test.go`, `migration_journal.go`, `migration_journal_test.go`, `migration_mode_posix_test.go`, `migration_privacy_other.go`, `migration_privacy_windows.go`, `migration_privacy_windows_test.go`
- Delete: `internal/reviewjob/legacy_v1.go`; update store tests so historical job fixtures are rejected instead of migrated

**Interfaces:**

```go
type ModelAccounting struct {
    ModelUsage
    Pricing *Pricing `json:"pricing,omitempty"`
    CostUSD *float64 `json:"cost_usd,omitempty"`
}

type SessionAccounting struct {
    StartedAt time.Time `json:"started_at"`
    EndedAt time.Time `json:"ended_at"`
    DurationMS int64 `json:"duration_ms"`
    Models []ModelAccounting `json:"models"`
    TotalTokens int64 `json:"total_tokens"`
    PricingComplete bool `json:"pricing_complete"`
    TotalCostUSD *float64 `json:"total_cost_usd,omitempty"`
}
```

Project/model summaries use pointer `cost_usd` and optional `cost_share_pct`. A priced model has both `pricing` and `cost_usd`; an unpriced model has neither. `pricing_complete=true` requires every model priced and a non-nil exact total. When false, aggregate total and all cost-share percentages are omitted, while prices/costs for individually priced models remain visible.

Evidence warnings add exactly `usage_unavailable:inexact_mapping:N`, `usage_unavailable:totals_mismatch:N`, and `usage_unavailable:missing_model:N`.

- [ ] **Step 1: Write failing accounting and schema tests**

```go
if got.PricingComplete || got.TotalCostUSD != nil {
    t.Fatalf("partial pricing was presented as complete: %+v", got)
}
if got.Models[0].CostUSD == nil || got.Models[1].CostUSD != nil {
    t.Fatalf("per-model pricing projection is wrong: %+v", got.Models)
}
```

Schema tests reject v2/v1/v2 documents, fake zero costs, `pricing_complete=true` with missing prices, and unknown warning reasons.

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/accounting ./internal/evidence ./internal/proposal ./internal/reviewv2 -count=1
cd obsidian-plugin
npm test
cd ..
```

Expected: FAIL on missing v3 types/schemas.

- [ ] **Step 3: Implement accounting invariants and schema files**

Aggregation retains all tokens and per-model known costs, sets aggregate cost only when complete, and never substitutes zero for unavailable cost.

- [ ] **Step 4: Cut all producers and consumers to the new contracts**

Rename the Go ledger package to `reviewv3`, update imports and CI package paths, change `ReviewSchemaVersion` to 3, embed `proposal-v2.schema.json`, synchronize the Skill copy byte-for-byte, and update Markdown frontmatter/schema checks to 3. Remove old schema assets and migration entrypoints.

- [ ] **Step 5: Update Obsidian parsing and presentation**

Rename the TypeScript contract module to `review-v3.ts`. Show tokens regardless of pricing. For an unpriced model render `价格未覆盖`; hide aggregate cost and cost share when `pricingComplete=false`.

- [ ] **Step 6: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./...
go vet ./...
go mod tidy -diff
cd obsidian-plugin
npm run check
cd ..
```

- [ ] **Step 7: Commit**

```bash
git add schemas internal skill cmd/release-packager obsidian-plugin .github
git commit -m "feat: represent incomplete source pricing honestly"
```

---

### Task 3: Canonical Codex Vertical Slice

**Files:**
- Create: `internal/source/codex/adapter.go` and tests
- Modify: `internal/session/` only as a private Codex JSONL locator/decoder
- Modify: `internal/evidence/extract.go` and tests
- Modify: `internal/accounting/accounting.go` and tests
- Modify: `internal/prepare/`, `internal/reviewjob/freeze.go`, `internal/reviewjob/types.go`
- Modify: `internal/cursor/`, `internal/cli/`, `internal/config/`
- Modify: all fixtures/tests using unprefixed SessionReviewer session IDs

**Interfaces:** The Codex adapter prefixes native `session_meta.id` at discovery, emits one canonical record per physical JSONL line, and preserves raw-line SHA-256. Extract/accounting switch only when prepare/freeze switch in the same task.

- [ ] **Step 1: Add an end-to-end failing Codex adapter test**

Fixture: native `s1`, metadata, user text, tool pair, turn context, and token count. Expect `codex.s1` in packet/proposal/cursor while on-disk metadata remains `s1`.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/source/codex ./internal/prepare ./internal/reviewjob -count=1`
Expected: FAIL because Codex is not behind the adapter.

- [ ] **Step 3: Implement translation and switch extract/accounting**

Map metadata to skip, turn-context cwd to cwd change, user/assistant text to message, custom tool call/output to tool records, token count to usage with the exact last turn-context model, and all other valid envelopes to skip.

- [ ] **Step 4: Switch prepare/freeze/cursors and sweep fixtures atomically**

Validate session IDs with `sessionid.Valid` and use canonical cursor filenames. Delete envelope branches only after every caller streams canonical records.

- [ ] **Step 5: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./internal/source/codex ./internal/prepare ./internal/reviewjob ./internal/evidence ./internal/accounting -count=1
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 6: Commit**

```bash
git add internal testdata
git commit -m "feat: review Codex through canonical source records"
```

---

### Task 4: Durable Interactive Review Ownership

**Files:**
- Create: `internal/reviewowner/types.go`, `store.go`, `owner.go` and tests
- Create: `internal/interactive/types.go`, `service.go` and tests
- Create: `internal/cli/interactive.go` and CLI tests
- Modify: prepare/apply/sync CLI plumbing
- Modify: `internal/cli/review.go` and `internal/reviewjob/service.go`
- Modify: `internal/reviewjob/types.go` for `E_RUN_INVALID`
- Modify: `skill/session-reviewer/SKILL.md` and shared wrappers

**Interfaces:**

```go
type Mode string

const (
    ModeInteractive Mode = "interactive"
    ModeJob Mode = "job"
)

type Store struct {
    Root string
}

type Record struct {
    SchemaVersion int `json:"schema_version"`
    Mode Mode `json:"mode"`
    OwnerID string `json:"owner_id"`
    ProjectID string `json:"project_id"`
    ProjectIdentity pathguard.IdentityToken `json:"project_identity"`
    UpdatedAt time.Time `json:"updated_at"`
    ExpiresAt time.Time `json:"expires_at"`
}

func Reserve(Store, Record, time.Time) error
func Verify(Store, mode, ownerID, projectID string, identity pathguard.IdentityToken, now time.Time) error
func Touch(Store, ownerID string, now time.Time) error
func Release(Store, ownerID string) error
func RecoverExpired(Store, time.Time) (bool, error)
```

`Store` persists the single owner at `review-owner/active.json`; interactive frozen state is `interactive/runs/<run-id>.json`. Both paths are rooted below the authenticated data directory and never supplied by callers. Callers already hold project/global kernel leases. Expiry is six hours after the last successful run command.

- [ ] **Step 1: Write owner state-machine tests**

Cover begin-vs-job busy, job-vs-begin busy, wrong run ID/identity, expiry, abort after one accepted packet, premature complete, and crash between reserve and run-state publication.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/reviewowner ./internal/interactive ./internal/cli -count=1`
Expected: FAIL because packages/commands do not exist.

- [ ] **Step 3: Implement rooted atomic owner persistence**

Reuse `pathguard` identities, `atomicfile` writes, canonical UTC, and exact CAS. Add no daemon or background heartbeat.

- [ ] **Step 4: Add exact commands**

```text
session-reviewer interactive begin --cwd ABS --json
session-reviewer interactive complete --run-id ID --json
session-reviewer interactive abort --run-id ID --json
session-reviewer prepare checkpoint --run-id ID --session ID --until-line N --until-hash HASH ...
```

Apply/sync wrappers also require `--run-id` in interactive mode. Every command reacquires both leases, verifies/touches ownership, then performs one bounded operation.

`interactive begin --json` returns schema version 1, `run_id`, `project_id`, and `sessions[]` containing only canonical `session_id`, `started_at`, and `upper`. Complete/abort return schema version 1, the same run/project IDs, and terminal state `completed|aborted`. No owner-store path or source path crosses stdout.

- [ ] **Step 5: Integrate Obsidian jobs**

`review start` reserves `mode=job` before spawn. The worker touches it while holding existing leases and releases only after terminal state is durable. Keep provider construction in `internal/cli`.

- [ ] **Step 6: Update the Skill**

Begin once, retain only run ID/frozen public boundaries in its private temp directory, never rescan, sync after accepted apply, complete after final sync, and abort on pre-apply semantic failure.

- [ ] **Step 7: Run focused, race, and full GREEN gates**

```bash
gofmt -w .
go test ./internal/reviewowner ./internal/interactive ./internal/cli ./internal/reviewjob -count=1
go test -race ./internal/reviewowner ./internal/interactive ./internal/reviewjob
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 8: Commit**

```bash
git add internal/reviewowner internal/interactive internal/cli internal/reviewjob skill/session-reviewer
git commit -m "feat: own interactive reviews across CLI calls"
```

---

### Task 5: Claude Source Adapter and Claude Skill

**Files:**
- Create: `internal/source/claude/adapter.go` and tests
- Create: `testdata/sources/claude/basic.jsonl`
- Modify: `internal/source/manager.go` and tests
- Modify: `internal/platform/paths.go` and source option plumbing
- Create: `skill/session-reviewer/hosts/claude/SKILL.md`

**Interfaces:** Default root is `~/.claude/projects` or `%USERPROFILE%\\.claude\\projects`. Overrides are `--claude-sessions-root` and `SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT`. `encodedProjectKey` cleans the absolute project path and replaces `/`, `\\`, and `:` with `-`, preserving the leading root replacement. Only that child directory is scanned. The key is untrusted narrowing; every selected record still proves physical `cwd` and filename UUID/`sessionId` agreement.

- [ ] **Step 1: Write fixture tests**

Cover text, tools, multiple items on one line, cache usage, partial final line, filename mismatch, unrelated corrupt directory ignored, matching-directory corruption rejected, and missing root skipped.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/source/claude ./internal/source ./internal/interactive -count=1`
Expected: FAIL because adapter is absent.

- [ ] **Step 3: Implement discovery and translation**

Expanded records from one line share its raw-line hash and get consecutive sequences. Defer an EOF fragment lacking a final newline; reject invalid newline-terminated JSON. Never derive reasoning tokens from thinking.

- [ ] **Step 4: Merge into manager/freeze and package Skill**

Sort candidates by `(StartedAt, SessionID)` and stop on first configured-source failure. Claude Skill calls the shared interactive flow and does not resolve a current session ID.

- [ ] **Step 5: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./internal/source/claude ./internal/source ./internal/interactive ./internal/prepare ./internal/reviewjob -count=1
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 6: Commit**

```bash
git add internal/source internal/platform internal/interactive internal/prepare internal/reviewjob testdata/sources/claude skill/session-reviewer
git commit -m "feat: collect and review Claude Code sessions"
```

---

### Task 6: OpenCode Documented-CLI Adapter with Stable Prefix

**Files:**
- Create: `internal/source/opencode/command.go`, `export.go`, `adapter.go` and tests
- Create: list/complete/active JSON under `internal/source/opencode/testdata/`
- Create: `internal/source/opencode/testdata/fake-opencode/main.go`
- Modify: `internal/source/manager.go`, `internal/platform/paths.go`, and CLI source options

**Interfaces:**

```go
type CommandLimits struct {
    Timeout time.Duration
    StdoutBytes int64
    StderrBytes int64
}

type Availability string

const (
    Available Availability = "available"
    Missing Availability = "missing"
)

type VerifiedExecutable struct {
    Path string
    Identity pathguard.IdentityToken
    Version string
}

func ResolveExecutable(explicit string, env map[string]string) (VerifiedExecutable, Availability, error)
func List(context.Context, VerifiedExecutable, CommandLimits) ([]SessionInfo, error)
func Export(context.Context, VerifiedExecutable, string, CommandLimits) (ExportedSession, error)
func StableRecords(ExportedSession) ([]source.Record, error)
```

List: five seconds/4 MiB. Export: 30 seconds/64 MiB. Stderr: 32 KiB. Decode strict UTF-8 JSON with duplicate-key/trailing-data rejection.

- [ ] **Step 1: Build fake CLI and RED command tests**

Assert exact argv `session list --format json --pure` and `export ses_fixture --pure`, with the authenticated project root as child cwd. Test timeout, nonzero exit, oversize, invalid UTF-8, successful empty list as zero candidates, empty export as failure, wrong schema/directory, and explicit-invalid vs unconfigured-missing behavior.

- [ ] **Step 2: Write RED stable-prefix tests**

Completed first turn plus second turn with running tool must freeze at the first assistant. Changing that tool to completed must preserve every old sequence/hash and append new call/result records.

- [ ] **Step 3: Implement bounded invocation and strict DTOs**

Authenticate the resolved absolute executable. Capture in memory only. Never create an export file or add SQLite.

- [ ] **Step 4: Implement stable canonicalization**

Order by `(info.time.created, info.id)` and part array order. Update `stableRecordCount` only after a finished assistant whose turn has no pending/running tool. Text emits message; completed/error tool emits call+result; reasoning/steps emit skip; exact assistant tokens/model emit usage. Return only the stable prefix.

- [ ] **Step 5: Merge into manager/freeze**

Filter list metadata by physical project directory before export. Missing unconfigured OpenCode skips; explicit incompatible OpenCode fails begin/freeze.

- [ ] **Step 6: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./internal/source/opencode ./internal/source ./internal/interactive ./internal/reviewjob -count=1
go test ./...
go vet ./...
go mod tidy -diff
```

Assert `go.mod` has no SQLite dependency.

- [ ] **Step 7: Commit**

```bash
git add internal/source/opencode internal/source/manager.go internal/platform internal/cli internal/interactive internal/reviewjob
git commit -m "feat: collect stable OpenCode session exports"
```

---

### Task 7: Shared Skill Packaging for All Three Hosts

**Files:**
- Modify: `skill/session-reviewer/SKILL.md`
- Create/modify: `skill/session-reviewer/hosts/codex/`, `hosts/claude/`, `hosts/opencode/`
- Modify: shared workflow scripts and package tests
- Modify: `cmd/release-packager/`

- [ ] **Step 1: Write failing archive/equivalence tests**

Require all three entrypoints, one proposal-v2 schema, no old schema assets, and no wrapper calling standalone `pending` or guessing current session ID.

- [ ] **Step 2: Run RED**

Run: `go test ./skill/session-reviewer/tests ./cmd/release-packager -count=1`.

- [ ] **Step 3: Implement host entrypoints**

Document host install locations. Delegate to one shared begin -> packet loop -> apply/sync -> complete workflow; abort before mutation on semantic failure.

- [ ] **Step 4: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./skill/session-reviewer/tests ./cmd/release-packager -count=1
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 5: Commit**

```bash
git add skill scripts cmd/release-packager
git commit -m "feat: package one review workflow for three agents"
```

---

### Task 8: Agent-kind CLI and Claude Obsidian Worker

**Files:**
- Create: `internal/agent/claude/verify.go`, `run.go`, and tests
- Modify: `internal/cli/review.go` and tests
- Modify: `internal/reviewjob/agent_handle.go` only for provider-neutral verified metadata

**Interfaces:** `adapterFor(kind, executable)` lives in `internal/cli` and returns `*reviewjob.AgentHandle`. `reviewjob.RunOptions.Agent` stays injected.

Claude fixed argv:

```text
-p
--output-format stream-json
--verbose
--json-schema <schema>
--safe-mode
--tools ""
--strict-mcp-config
--mcp-config {"mcpServers":{}}
--no-session-persistence
```

Never include `--bare`. Run from the authenticated private worker directory.

- [ ] **Step 1: Write RED fake-Claude tests**

Cover exact argv, existing-auth success, auth failure, unsupported flags/schema, any tool event in the stream, malformed/missing final structured result, timeout/cancel, Windows command-line size bound, and no Project/Vault path leak.

- [ ] **Step 2: Add explicit `--agent-kind` tests**

Verify/start/retry require `codex|claude|opencode`. OpenCode returns incompatible without spawn. Retry kind must match the persisted verified handle.

- [ ] **Step 3: Implement Claude adapter and CLI composition**

Parse only fixed structured output and pass proposal bytes to the provider-neutral service.

- [ ] **Step 4: Run focused and full GREEN gates**

```bash
gofmt -w .
go test ./internal/agent/... ./internal/cli ./internal/reviewjob -count=1
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/claude internal/cli internal/reviewjob
git commit -m "feat: verify Claude as an isolated review worker"
```

---

### Task 9: Obsidian Default Worker and Source Labels

**Files:**
- Modify: `obsidian-plugin/src/cli/runner.ts`, `settings.ts`
- Modify: `obsidian-plugin/src/main.ts`
- Modify: project-view/presentation/usage rendering and tests
- Modify: `internal/ledger/diagram.go` only if raw IDs lack labels

- [ ] **Step 1: Write failing plugin tests**

Cover `--agent-kind` argv, mismatch rejection, ignored `codexPath`, Claude save only after compatible verify, OpenCode refusal, and labels from canonical prefixes.

- [ ] **Step 2: Implement settings/runner allowlists**

Settings are `cliPath` plus optional `agentKind`/`agentPath`. Empty worker is sync-only.

- [ ] **Step 3: Implement incomplete-pricing presentation**

Keep full-width model cards, known price links, `价格未覆盖` for unknown models, and no aggregate cost/share when incomplete.

- [ ] **Step 4: Run plugin and repository GREEN gates**

```bash
cd obsidian-plugin
npm run check
cd ..
gofmt -w .
go test ./...
go vet ./...
go mod tidy -diff
```

- [ ] **Step 5: Commit**

```bash
git add obsidian-plugin internal/ledger
git commit -m "feat: configure and display multi-agent reviews"
```

---

### Task 10: Documentation, Cross-platform Gates, and Real Acceptance

**Files:**
- Modify: `README.md`, `README.zh-CN.md`
- Modify: `.github/workflows/ci.yml` if final package names changed
- Create: `docs/acceptance/multi-agent-session-review.md`

- [ ] **Step 1: Run final gates**

```bash
gofmt -w .
go test ./...
go test -race ./... -skip '^TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB$'
go vet ./...
go mod tidy -diff
git diff --check
cd obsidian-plugin
npm run check
cd ..
```

Build macOS arm64/amd64 and Windows amd64 with release scripts, run credential scan, and verify release archives contain only v3/v2/v3 contracts and all host entrypoints.

- [ ] **Step 2: Document setup/failures**

Document canonical IDs, Claude root, OpenCode executable, owner expiry/abort, incomplete pricing, Claude auth without `--bare`, and OpenCode's Skill-only worker limit.

- [ ] **Step 3: Perform authorized real acceptance**

1. Claude Skill accepts Codex + Claude sessions into one installed Obsidian view.
2. OpenCode Skill adds all three sources to that ledger.
3. Active interactive run makes Obsidian return `E_AGENT_BUSY`.
4. Running-to-completed OpenCode tool appends without old-prefix drift.
5. Unpriced model retains tokens with `pricing_complete=false` and no fake total.
6. OpenCode JSON is bounded valid UTF-8 on macOS and Windows.
7. Claude Obsidian worker reuses existing auth without `--bare` or tool events.

Record versions, sanitized summaries, and installed bundle hash; never copy transcripts.

- [ ] **Step 4: Commit**

```bash
git add README.md README.zh-CN.md docs/acceptance .github
git commit -m "docs: verify multi-agent session review"
```

---

## Spec Coverage

- Canonical IDs and opaque Reader: Tasks 1 and 3
- Honest partial pricing and schema cutover: Task 2
- Codex vertical slice with no broken intermediate commit: Task 3
- Durable cross-process ownership: Task 4
- Narrowed Claude collection and Skill: Task 5
- Documented OpenCode CLI and stable prefix: Task 6
- Shared packaging without current-session guessing: Task 7
- Claude no-tools worker and composition-root dispatch: Task 8
- Obsidian selection, labels, and pricing UI: Task 9
- Full gates and installed acceptance: Task 10
