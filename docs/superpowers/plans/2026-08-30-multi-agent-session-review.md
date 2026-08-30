# Multi-agent SessionReviewer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Codex, Claude Code, and OpenCode equal SessionReviewer experience: one project ledger, canonical provider-prefixed session IDs, source adapters, in-agent Skills, and an Obsidian default worker that is not hardcoded to Codex.

**Architecture:** Each host has a source adapter that emits canonical records (strictly increasing sequence, source hash, message/tool/cwd/usage/skip). Prepare, freeze, pending, extract, and accounting consume only that stream. In-agent Skills freeze with `pending` plus until-line/hash flags and write proposals themselves. Obsidian still runs a durable proposal-only job; Claude may be saved as the worker after verification, OpenCode may not.

**Tech Stack:** Go 1.26, existing pathguard/atomicfile/cursor/prepare/apply/reviewjob, CGO-free SQLite for OpenCode only, TypeScript 5.8, Obsidian 1.13, Vitest.

**Spec:** `docs/superpowers/specs/2026-08-30-multi-agent-session-review-design.md`

## Global Constraints

- Canonical session IDs only: `codex.<native>`, `claude.<uuid>`, `opencode.<ses_...>`. Unprefixed IDs fail closed. No alias, dual lookup, or rewrite path.
- Evidence, proposal, and ledger schemas stay v2. `jsonl_line` is the adapter-assigned sequence number.
- Skills never read Codex JSONL, Claude JSONL, or OpenCode SQLite. The only evidence is a prepared packet.
- Missing source roots are skipped. Corrupt in-project source data fails the whole freeze.
- One review processes every pending session for the project, sorted by `(StartedAt, SessionID)`, and stops on first failure.
- Obsidian workers must prove proposal-only / no-tools. OpenCode returns `compatible: false`.
- Ignore legacy plugin `codexPath`. Do not migrate it.
- Tests use trimmed fixtures only. Never open the live OpenCode database under the user profile.
- Preserve unrelated dirty-worktree files. Stage only files named by the current task.
- Repository gates after each slice: focused tests, then `go test ./...`, `go vet ./...`, `gofmt`. Plugin tasks also run the plugin test/lint suite.

## File Structure and Ownership

- Create `internal/sessionid/`: parse, format, and validate canonical IDs. Job/project IDs stay on `reviewjob.validID`.
- Create `internal/source/`: provider-neutral Discover/Open/Stream adapter and merged freeze input.
- Create `internal/source/codex/`: translate Codex JSONL envelopes into canonical records. Sequence equals JSONL line.
- Create `internal/source/claude/`: Claude project JSONL adapter. Sequence is stream order; one source line may yield several records that share the line hash.
- Create `internal/source/opencode/`: read-only SQLite adapter. Hash the canonical payload, not stored JSON bytes.
- Modify `internal/evidence/extract.go`: consume canonical record types only. Delete Codex envelope switching.
- Modify `internal/accounting`: observe canonical `usage` records instead of Codex `event_msg`/`token_count`.
- Modify `internal/session`, `internal/prepare`, `internal/reviewjob/freeze.go`, `internal/platform/paths.go`, `internal/cli`: canonical IDs, extra source roots, `pending`, until-line/hash flags, `--agent-kind`.
- Modify `internal/reviewjob/types.go`: frozen session IDs use `sessionid.Valid`, not lowercase job `validID`.
- Create `internal/agent/claude/`: Obsidian proposal-only Claude adapter, fail closed unless no tool events are proven.
- Modify Obsidian plugin settings/runner/presentation: default worker kind plus path; source labels from ID prefix.
- Package the same Skill semantics for Claude and OpenCode. Wrappers differ; prepare/apply scripts do not.
- Update every test fixture that used unprefixed IDs such as `s1` to `codex.s1`. Native JSONL `payload.id` stays unprefixed.

---

### Task 1: Canonical Session IDs

**Files:**
- Create: `internal/sessionid/sessionid.go`
- Create: `internal/sessionid/sessionid_test.go`

**Interfaces:**
- Consumes: raw strings from CLI, env, adapters, packets, cursors, and ledger fields.
- Produces:

```go
package sessionid

type Provider string

const (
    ProviderCodex    Provider = "codex"
    ProviderClaude   Provider = "claude"
    ProviderOpenCode Provider = "opencode"
)

type ID struct {
    Provider Provider
    Native   string
}

func Parse(value string) (ID, error)
func (id ID) String() string
func Valid(value string) bool
func PrefixNative(provider Provider, native string) (ID, error)
```

- [ ] **Step 1: Write the failing tests**

```go
func TestParseCanonicalIDs(t *testing.T) {
    got, err := Parse("codex.s1")
    if err != nil || got.Provider != ProviderCodex || got.Native != "s1" || got.String() != "codex.s1" {
        t.Fatalf("got=%+v err=%v", got, err)
    }
    claude, err := Parse("claude.f981b686-02f8-414f-80a3-2bb191c489ed")
    if err != nil || claude.Provider != ProviderClaude {
        t.Fatalf("claude=%+v err=%v", claude, err)
    }
    open, err := Parse("opencode.ses_fb2c251beffebUIc5MHYz5gCpz")
    if err != nil || open.Provider != ProviderOpenCode || open.Native != "ses_fb2c251beffebUIc5MHYz5gCpz" {
        t.Fatalf("opencode=%+v err=%v", open, err)
    }
}

func TestParseRejectsUnprefixedAndUnknown(t *testing.T) {
    for _, value := range []string{"s1", "session-1", "codex.", ".s1", "gemini.s1", "CODEX.s1", "codex.s1/extra"} {
        if _, err := Parse(value); err == nil {
            t.Fatalf("accepted %q", value)
        }
    }
}

func TestParseAllowsDotsInsideNativeID(t *testing.T) {
    got, err := Parse("codex.thread.with.dots")
    if err != nil || got.Native != "thread.with.dots" {
        t.Fatalf("got=%+v err=%v", got, err)
    }
}

func TestPrefixNativeRejectsBlankOrUnsafeNative(t *testing.T) {
    if _, err := PrefixNative(ProviderCodex, ""); err == nil {
        t.Fatal("expected error")
    }
    if _, err := PrefixNative(ProviderCodex, "has space"); err == nil {
        t.Fatal("expected error")
    }
}
```

Allowed native charset is the current cursor set `[A-Za-z0-9._-]+`, not empty, not `.` / `..`, and not a Windows reserved basename. Mixed case is required for OpenCode. `Parse` must not auto-prefix.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/sessionid -count=1`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement Parse/String/Valid/PrefixNative**

Split on the first `.`. Provider must be exactly `codex`, `claude`, or `opencode`. Native is the remainder and may contain more dots. `Valid` is `Parse == nil`.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/sessionid && go test ./internal/sessionid -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/sessionid/sessionid.go internal/sessionid/sessionid_test.go
git commit -m "feat: parse canonical multi-agent session IDs"
```

---

### Task 2: Canonical Records and Extract

**Files:**
- Create: `internal/source/record.go`
- Modify: `internal/session/record.go` (keep `Record`; `Type` becomes canonical)
- Modify: `internal/evidence/extract.go`
- Modify: `internal/evidence/extract_test.go`
- Modify: `internal/accounting/accounting.go`
- Modify: `internal/accounting/accounting_test.go`

**Interfaces:**
- Consumes: `session.Record` whose `Type` is one of `message`, `tool_call`, `tool_result`, `cwd_change`, `usage`, `skip`.
- Produces: unchanged evidence packet v2. Extract no longer understands Codex `response_item` / `turn_context` / `event_msg`.

Canonical payloads:

- `message`: `{"id":"...","role":"user|assistant","text":"..."}`
- `tool_call`: `{"id":"...","name":"...","input":"..."}`
- `tool_result`: `{"id":"...","output":"..."}`
- `cwd_change`: `{"cwd":"..."}`
- `usage`: token counters plus `model`
- `skip`: `{}` — advance cursor only

- [ ] **Step 1: Rewrite extract tests onto canonical records**

Replace helpers so they emit `Type: "message"` payloads, not Codex `response_item`. Cover tool pairs, skip-without-event, non-increasing sequence rejection, and accounting `usage` records. `Accumulator.Observe` requires `Type == "usage"` and a model field. Do not invent `unknown` for blank models.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/evidence ./internal/accounting -count=1`

Expected: FAIL on envelope-shaped fixtures and missing canonical switches.

- [ ] **Step 3: Implement extract/accounting on canonical types**

`Add` switches on `record.Type` only. `usage` records update session usage then advance. Keep redaction, packet limits, and `jsonl_line = record.Line`.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/evidence internal/accounting internal/source && go test ./internal/evidence ./internal/accounting -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/record.go internal/session/record.go internal/evidence internal/accounting
git commit -m "feat: extract evidence from canonical source records"
```

---

### Task 3: Source Adapter Interface and Codex Adapter

**Files:**
- Create: `internal/source/adapter.go`
- Create: `internal/source/adapter_test.go`
- Create: `internal/source/codex/adapter.go`
- Create: `internal/source/codex/adapter_test.go`
- Keep `session.Stream*` as the JSONL reader; discovery of Codex files moves behind the Codex adapter.

**Interfaces:**

```go
package source

type Adapter interface {
    Kind() sessionid.Provider
    Discover(ctx context.Context, root string, limits session.DiscoveryLimits) (session.Discovery, error)
    Open(root string, candidate session.Candidate) ([]*os.File, error)
    Stream(handles StreamHandles, opts session.DecodeOptions, visit func(session.Record) error) (session.DecodeSummary, error)
}

type StreamHandles struct {
    Files     []*os.File
    DBPath    string
    SessionID sessionid.ID
}
```

Native JSONL `session_meta.id` stays unprefixed on disk. The adapter prefixes at the boundary: `codex.<native>`. Native Codex IDs that already look like a canonical ID fail closed.

- [ ] **Step 1: RED tests for Codex translation**

Temp JSONL with native id `s1`, a user message, and a reasoning item. Expect discovery ID `codex.s1`, message at line 2, skip at the reasoning line, hashes equal to SHA-256 of each raw line, sequence equal to line number.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/source ./internal/source/codex -count=1`

Expected: FAIL because adapters do not exist.

- [ ] **Step 3: Implement Codex adapter**

Translate `session_meta` to skip, `turn_context` cwd changes to `cwd_change`, user/assistant text to `message`, custom tool call/output to tool records, `token_count` to `usage` using the last `turn_context` model, everything else to skip. Keep discovery budgets, symlink/reparse rejection, and segmented-session behavior.

- [ ] **Step 4: Run GREEN**

Run: `gofmt -w internal/source internal/source/codex && go test ./internal/source ./internal/source/codex ./internal/session -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source internal/session
git commit -m "feat: add Codex source adapter with canonical records"
```

---

### Task 4: Prepare, Freeze, Cursors, and Tests on Canonical Codex IDs

**Files:**
- Modify: `internal/prepare/prepare.go`, `internal/prepare/prepare_test.go`
- Modify: `internal/reviewjob/freeze.go`, `internal/reviewjob/types.go`, `internal/reviewjob/freeze_test.go`
- Modify: cursor validation to `sessionid.Valid` if it still uses the unprefixed charset only
- Modify: every Go test/fixture that treats `s1` / `session-1` as a ledger/packet/cursor ID

**Interfaces:**
- `prepare.Options.SessionID` must parse as canonical.
- `FrozenSession.SessionID` validates with `sessionid.Valid` (mixed case allowed).
- Cursor filenames are the canonical ID, for example `codex.s1.json`.

- [ ] **Step 1: Change one prepare test to `codex.s1` and reject unprefixed `--session s1`**

Native JSONL `payload.id` remains `s1`. Packet/cursor IDs become `codex.s1`. Do not add an alias path.

- [ ] **Step 2: Run RED, then wire prepare/freeze through the Codex adapter**

Replace `session.Discover(opts.SessionsRoot, opts.SessionID)` with the Codex adapter. Load/store cursors under the canonical ID. Change `validateFrozenSessions` to `sessionid.Valid`.

- [ ] **Step 3: Sweep remaining tests**

Apply, proposal, ledger, CLI, sync, and reviewjob tests that expected `s1` as a SessionReviewer ID must expect `codex.s1`.

- [ ] **Step 4: Run GREEN**

Run: `go test ./internal/prepare ./internal/reviewjob ./internal/cursor ./internal/apply ./internal/evidence ./internal/source/... -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal testdata
git commit -m "feat: require canonical Codex session IDs in prepare and freeze"
```

---

### Task 5: Pending Command and Frozen Upper CLI Flags

**Files:**
- Create: `internal/cli/pending.go`
- Modify: `internal/cli/run.go`, `internal/cli/prepare.go`, `internal/cli/diagnostic.go`
- Modify: `internal/prepare/prepare.go` to accept CLI until-line/hash into `UpperBoundary`
- Modify: `internal/platform/paths.go`
- Modify: `skill/session-reviewer/SKILL.md` and the prepare-workflow wrappers
- Test: `internal/cli/run_test.go`

**Interfaces:**

```text
session-reviewer pending --cwd PROJECT [--data-dir DIR] [--sessions-root PATH] --json
session-reviewer prepare checkpoint --session ID --until-line N --until-hash HASH ...
```

Pending JSON schema version 1 includes `project_id` and `sessions[]` with `session_id`, `started_at`, and `upper`. No source paths and no record bodies.

Lease rule: each CLI process acquires and releases project plus global leases. `pending` cannot hold the lock across later prepare processes. The frozen until-line/hash list is the consistency mechanism. If an Obsidian job already holds the lease, `pending` fails with `E_AGENT_BUSY`.

`--session` must already be canonical. Env vars stay native (`CODEX_THREAD_ID`); `PrefixNative(ProviderCodex, env)` is the only auto-prefix, and only from a known host variable. Both until flags are required together. `--from-start` cannot combine with them.

- [ ] **Step 1: RED CLI tests for pending JSON, no path leaks, until-hash drift, busy lease, and unprefixed session rejection**

- [ ] **Step 2: Implement pending via FreezePending and copy until flags into UpperBoundary**

- [ ] **Step 3: Update the Skill**

Resolve `PROJECT_ROOT` -> write pending JSON to a private temp file -> for each session, prepare with that upper bound -> proposal -> apply -> sync if the cursor advanced -> repeat while `has_more`. Never reread pending. Never pass `--from-start` on successor packets. If pending is empty, only sync.

- [ ] **Step 4: GREEN**

Run: `go test ./internal/cli ./internal/prepare ./internal/reviewjob -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli internal/prepare internal/platform skill/session-reviewer
git commit -m "feat: freeze pending sessions for in-agent review"
```

---

### Task 6: Claude Source Adapter

**Files:**
- Create: `internal/source/claude/adapter.go`
- Create: `internal/source/claude/adapter_test.go`
- Create: `testdata/sources/claude/basic.jsonl`
- Modify: `internal/platform/paths.go` for `~/.claude/projects` and `SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT`
- Modify: prepare options / CLI `--claude-sessions-root`
- Modify: freeze to merge Claude discovery

**Claude mapping:** file `<uuid>.jsonl`; filename UUID must equal record `sessionId`; candidate ID is `claude.<uuid>`; project membership uses physical `cwd`; text -> message; `tool_use` / `tool_result` -> tool records; thinking, queue, attachment -> skip; multiple evidence-bearing parts on one JSONL line get consecutive sequences and the same line hash; assistant `message.usage` -> `usage` with `message.model`; `cache_read_input_tokens` -> cached input; `cache_creation_input_tokens` -> cache write; do not invent reasoning from thinking.

- [ ] **Step 1: RED fixture test for mapping, filename mismatch, and skip records**

- [ ] **Step 2: Implement adapter with the same symlink/reparse and budget rules as Codex**

- [ ] **Step 3: Merge into freeze/pending. Missing Claude root skips. Corrupt in-project Claude data fails freeze.**

- [ ] **Step 4: GREEN**

Run: `go test ./internal/source/claude ./internal/reviewjob ./internal/prepare ./internal/cli -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/claude testdata/sources/claude internal/platform internal/prepare internal/reviewjob internal/cli
git commit -m "feat: collect Claude Code sessions into the project ledger"
```

---

### Task 7: OpenCode Source Adapter

**Files:**
- Create: `internal/source/opencode/adapter.go`
- Create: `internal/source/opencode/canonical.go`
- Create: `internal/source/opencode/adapter_test.go`
- Modify: `go.mod` / `go.sum` to add CGO-free SQLite
- Modify: platform/CLI/freeze for `--opencode-db` and `SESSION_REVIEWER_OPENCODE_DB`

Default macOS/Linux DB: `~/.local/share/opencode/opencode.db`. Windows: do not guess `%APPDATA%`; require the flag unless that same documented `.local/share/opencode/opencode.db` path exists under the user profile.

**OpenCode mapping:** open the DB read-only; session `directory` must match project identity; ID is `opencode.<session.id>`; messages `ORDER BY time_created, id`, parts `ORDER BY id`; text -> message; completed tool with input/output -> tool_call then tool_result; step-start, step-finish, reasoning -> skip; hash compact canonical JSON, not `part.data`; usage sums assistant message tokens with model `providerID/modelID`; if session-level totals disagree, omit usage and warn; unexpected schema fails closed.

- [ ] **Step 1: RED tests on a tiny SQLite fixture created in t.TempDir. Never copy the live database.**

- [ ] **Step 2: Implement parameterized SQL only. Never interpolate paths or IDs.**

- [ ] **Step 3: Merge into freeze/pending with the same skip/fail policy as Claude.**

- [ ] **Step 4: GREEN**

Run: `go test ./internal/source/opencode ./internal/reviewjob ./internal/prepare ./internal/cli -count=1 && go mod tidy -diff`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/opencode go.mod go.sum internal/platform internal/prepare internal/reviewjob internal/cli
git commit -m "feat: collect OpenCode sessions into the project ledger"
```

---

### Task 8: Claude and OpenCode Skill Packaging

**Files:**
- Modify: `skill/session-reviewer/SKILL.md`
- Create: host entrypoints under `skill/session-reviewer/hosts/claude/` and `skill/session-reviewer/hosts/opencode/`
- Modify: release packager if it currently copies only the Codex skill tree
- Test: `skill/session-reviewer/tests/package_test.go`

Semantic rules stay one document. Host wrappers only differ in how they pass the current native session id. Do not invent undocumented env vars. If none exist, cwd plus time may resolve the current session and must fail closed on ambiguity.

- [ ] **Step 1: RED packager test that the archive contains Codex, Claude, and OpenCode entrypoints sharing scripts/schema**

- [ ] **Step 2: Add host entrypoints that exec the same prepare-workflow and apply-proposal wrappers**

- [ ] **Step 3: GREEN packager test**

- [ ] **Step 4: Commit**

```bash
git add skill scripts
git commit -m "feat: package SessionReviewer skills for Claude and OpenCode"
```

---

### Task 9: Review CLI Agent Kind and Claude Obsidian Worker

**Files:**
- Modify: `internal/cli/review.go`, `internal/cli/review_test.go`
- Create: `internal/agent/claude/verify.go`, `internal/agent/claude/run.go`, tests
- Modify: `internal/reviewjob/service.go` to dispatch by `job.Agent.Kind`

**CLI:**

```text
session-reviewer review agent verify --agent-kind KIND --executable ABS --json
session-reviewer review start --project-id ID --agent-kind KIND --agent-executable ABS --json
session-reviewer review retry --job-id ID --agent-kind KIND --agent-executable ABS ...
```

`KIND` is `codex|claude|opencode`. Verify JSON `kind` must equal the requested kind. Omitting `--agent-kind` is invalid argv. OpenCode verify returns `compatible: false` and `E_AGENT_INCOMPATIBLE` without spawning `opencode run`. Claude verify/run uses a fixed no-tools argv (`-p`, JSON schema, `--bare`, disabled MCP/tools), a private work root, and fails on any tool event. If Claude cannot prove no-tools, `E_AGENT_INCOMPATIBLE`.

- [ ] **Step 1: RED tests for missing kind, OpenCode incompatible without spawn, Claude incompatible path, and persisted `job.Agent.Kind`**

- [ ] **Step 2: Implement `adapterFor(kind, executable)` dispatch. Codex keeps the existing adapter.**

- [ ] **Step 3: GREEN**

Run: `go test ./internal/cli ./internal/agent/... ./internal/reviewjob -count=1`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli internal/agent internal/reviewjob
git commit -m "feat: select Obsidian review workers by agent kind"
```

---

### Task 10: Obsidian Default Worker and Source Labels

**Files:**
- Modify: `obsidian-plugin/src/cli/runner.ts`
- Modify: `obsidian-plugin/src/cli/settings.ts`
- Modify: `obsidian-plugin/src/main.ts`
- Modify: `obsidian-plugin/src/view/project-view.ts`
- Modify: `obsidian-plugin/src/view/presentation.ts`
- Modify: evolution/decision rendering if raw session IDs are shown
- Modify: plugin tests for CLI, settings, and review-job view
- Modify: `internal/ledger/diagram.go` only if it dumps raw session IDs with no source label

**Settings contract:** save `cliPath`, optional `agentKind`, optional `agentPath`. Ignore `codexPath`. Empty worker settings mean sync-only. Runner allowlist includes `--agent-kind` on verify/start/retry. Verification kind must match the requested kind. OpenCode kind refuses to save. Present `codex.` / `claude.` / `opencode.` as Codex / Claude Code / OpenCode next to session titles and citations. Do not add a second usage card grouped by agent. Do not reorganize the evolution layout.

- [ ] **Step 1: RED plugin tests for argv, ignored `codexPath`, OpenCode save rejection, and source labels**

- [ ] **Step 2: Implement settings, runner, and presentation**

- [ ] **Step 3: GREEN plugin lint/test suite and any ledger diagram tests**

- [ ] **Step 4: Commit**

```bash
git add obsidian-plugin internal/ledger
git commit -m "feat: configure multi-agent Obsidian review workers"
```

---

### Task 11: Slice Verification and Docs

**Files:**
- Modify: `README.md`, `README.zh-CN.md`

- [ ] **Step 1: Run full Go and plugin gates**

```bash
gofmt -w .
go test ./...
go vet ./...
go mod tidy -diff
```

Plus the plugin lint/test/build commands already used for 0.2.x. Expected: PASS. No live OpenCode DB opens. No unprefixed session IDs in packets or cursors.

- [ ] **Step 2: Document source-root flags, pending, until flags, and `--agent-kind`. State that OpenCode is Skill-only for Obsidian workers in this version.**

- [ ] **Step 3: Commit**

```bash
git add README.md README.zh-CN.md
git commit -m "docs: describe multi-agent session review"
```

Real acceptance after implementation, not as a substitute for the gates: installed Obsidian bundle; one Claude-started review and one OpenCode-started review against a connected project; both appear in the same project browser.

---

## Spec coverage

- Canonical IDs, no aliases: Tasks 1, 4
- Canonical records / extract: Task 2
- Codex/Claude/OpenCode adapters: Tasks 3, 6, 7
- Project-wide freeze/pending/until: Tasks 4, 5, 6, 7
- In-agent Skill path: Tasks 5, 8
- Obsidian worker kind, Claude proof, OpenCode closed: Tasks 9, 10
- Human source labels, ignored `codexPath`: Task 10
- Fail-closed roots, schema, usage omit-don't-zero: Tasks 6, 7, 11
- Delivery slices 1-4 map to Tasks 1-5, 6+8, 7+8, 9-10

