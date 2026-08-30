# Multi-agent SessionReviewer

- Status: approved design, implementation plan revised after architecture review
- Date: 2026-08-30
- Target: Codex, Claude Code, and OpenCode on macOS and Windows
- Decision: one project ledger, namespaced session IDs, source adapters, and two invocation paths

## 1. Problem

SessionReviewer currently gives Codex a complete continuity loop: local JSONL becomes a bounded evidence packet, a Skill or Obsidian job turns that packet into a proposal, and the trusted CLI applies it to one project ledger that Obsidian can read and edit.

Claude Code and OpenCode already hold real work for the same projects, but they are invisible to that loop. Their session stores are not Codex JSONL. Their in-agent Skills are not installed. Obsidian's primary review action is hardcoded to a Codex executable. The 2026-08-28 orchestration spec left an adapter boundary for those hosts and explicitly deferred them.

The product goal is equal experience, not a Codex-shaped export of other logs. A user who starts a review in Claude, OpenCode, or Obsidian should land in the same project context browser.

## 2. Goals

- Collect pending work from Codex, Claude Code, and OpenCode into one project ledger.
- Let the user start a review from Claude, OpenCode, Codex, or Obsidian.
- Keep one primary Obsidian action, `总结并同步`, and one human-facing project-evolution view.
- Preserve the cursor, proposal-only worker, apply, and sync safety properties while replacing the source-accounting JSON contracts cleanly.
- Keep raw session stores local, redacted, and unread by Skills.
- Fail closed on identity collisions, source drift, and unproven worker capabilities.

## 3. Non-goals

- A sidecar copy of Claude or OpenCode sessions rewritten as Codex JSONL.
- Direct reads of OpenCode's private SQLite schema.
- An OpenCode Obsidian worker before it can prove the proposal-only contract.
- A per-source worker split inside one click.
- Changing the evolution, decision, usage, risk, or resume layout.
- Git mutation, a watcher, a daemon, cloud collection, or a new desktop GUI.
- Publishing a release or community-plugin update as part of this design.

## 4. Confirmed product decisions

- One click reviews every pending session for the selected project, regardless of source.
- The starter is the semantic worker: the current Claude, OpenCode, or Codex conversation writes the proposal. Obsidian uses one configured default worker.
- Session IDs are always `provider.nativeId`. Unprefixed IDs are invalid.
- Source adapters emit a canonical ordered record stream. `jsonl_line` remains the 1-based sequence number.
- Skills never read raw source stores or OpenCode export JSON. The only evidence is a prepared packet.
- Obsidian workers must prove proposal-only, no-write, structured-output behavior. OpenCode's isolated worker stays closed until that proof exists.
- Missing source roots are skipped. Corrupt data that may belong to the current project fails the whole freeze.
- In-agent review owns one durable review run from freeze through the final sync. Kernel leases protect each command; the durable owner record prevents an Obsidian job from starting between commands.
- Source usage keeps exact token counts even when public pricing is unavailable. Unknown prices are represented as incomplete pricing, never as zero cost and never as a failed semantic review.
- OpenCode collection uses its documented local CLI JSON interfaces. SessionReviewer does not depend on OpenCode database tables.

## 5. Selected approach

Three approaches were compared.

A. Rewrite Claude and OpenCode into Codex-shaped sidecar JSONL, then reuse the current locator and extractor. This is the smallest code change and the weakest provenance story: it keeps a second private copy and makes exporter behavior part of cursor provenance.

B. Provider adapters plus namespaced session IDs plus a canonical record stream. Selected. The ledger, Obsidian view, and CAS cursor stay project-shaped. Each source owns discovery and streaming behind an opaque reader. The accounting contracts advance once so partial pricing is represented honestly.

C. Opaque per-source cursor tokens. Cleaner for a fourth and fifth agent, but it would also replace the proven sequence-plus-hash CAS. Stable finalized prefixes solve the current three-source problem without that cursor migration.

## 6. Architecture

```text
Codex JSONL -+
Claude JSONL-+- Source adapters - canonical records - prepare - evidence packet
OpenCode CLI-+                                              |
                                                            v
In-agent Skill (current conversation) -- proposal -- apply -- project ledger
Obsidian default worker (isolated)    -- proposal -- apply --      |
                                                                   v
                                                         Obsidian project view
```

The trusted CLI still owns discovery, redaction, validation, cursor compare-and-swap, apply, and sync. Semantic judgment stays in a Skill or an isolated proposal worker. The worker never writes Project, Vault, ledger, cursor, or SessionReviewer data roots.

## 7. Session identity

Canonical IDs use a single known prefix and treat everything after the first dot as the native ID:

- `codex.<nativeId>`
- `claude.<uuid>`
- `opencode.<ses_...>`

The native ID may itself contain dots. Unknown prefixes are invalid. Job and project IDs keep the current lowercase `validID` rule. Session IDs use the cursor character set `[A-Za-z0-9._-]`, which allows mixed case.

`--session`, cursor files, packets, proposals, and ledger session IDs store only the canonical form. Unprefixed values fail closed. There is no alias, dual lookup, or rewrite path.

`--session` accepts only canonical IDs. Skill wrappers receive canonical session IDs from `interactive begin`; they do not inspect host session environment variables or guess by cwd and time.

Human display maps prefixes to short labels: Codex, Claude Code, OpenCode. The same rule applies to timeline nodes, session titles, and evidence citations. Usage cards remain model-aggregated. The machine schema does not gain a `source` field.

## 8. Source adapters

Each source resolves its availability, discovers sessions for the authenticated project identity, and opens an opaque session reader. The orchestration layer sees provider-neutral candidate metadata and a `Reader` with `Stream` and `Close`; it never receives `[]*os.File`, a database path, or a provider-specific handle. A canonical record contains 1-based sequence, timestamp, type, payload, and SHA-256. Cursor CAS remains sequence plus hash. Adapters assign the sequence; extract requires it to increase strictly. Codex emits one record per JSONL line, so sequence equals the source line number. Claude and OpenCode assign sequence in stream order. One Claude JSONL line may become several sequence numbers when it contains multiple evidence-bearing parts. `source_hash` authenticates the underlying source atom: the JSONL line for Codex and Claude, or one compact canonical record payload for OpenCode.

### 8.1 Codex

Keep the current sessions-root discovery. The adapter translates Codex JSONL into canonical record types, preserves one-record-per-JSONL-line numbering and source-line hashes, and always emits `codex.<nativeId>`.

### 8.2 Claude Code

Default root is `~/.claude/projects` on macOS and `%USERPROFILE%\.claude\projects` on Windows. Override with `--claude-sessions-root` or `SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT`. Files are `<uuid>.jsonl`. The encoded project directory is an untrusted discovery prefilter so an unrelated malformed project cannot block every Claude project. Every selected record must still prove project membership with its physical `cwd`. Hashes cover the source JSONL line. Filename UUID and record `sessionId` must match.

Included evidence: `user` / `assistant` text as messages, `tool_use` as tool calls, `tool_result` as tool results. `thinking`, queue, and attachment records advance the cursor only. Evidence-bearing parts on one JSONL line receive consecutive sequence numbers and share that line's source hash. A final EOF fragment without a newline is an active tail and is deferred; an invalid newline-terminated record is corrupt.

### 8.3 OpenCode

The source is the installed OpenCode CLI. SessionReviewer resolves and authenticates an absolute executable from `--opencode-executable`, `SESSION_REVIEWER_OPENCODE_EXECUTABLE`, or `PATH`, then invokes only `opencode session list --format json --pure` and `opencode export SESSION_ID --pure` with the project root as the child working directory. Stdout and stderr have fixed byte budgets and timeouts. A successful empty `session list` means no sessions; empty `export`, non-UTF-8, truncated, or schema-invalid output fails the source. Session `directory` must match the current physical project identity. The JSON is consumed in memory; no sidecar export is written.

Canonical order is exported messages by `(info.time.created, info.id)`, then exported parts in their array order. Hashes cover a SessionReviewer-defined compact canonical payload. `text` parts become messages; a finalized `tool` part becomes a tool call followed by a tool result. `step-start`, `step-finish`, and `reasoning` advance the sequence only.

OpenCode exposes a mutable active tail. The adapter streams only a stable prefix ending at a finished assistant message. Every tool part through that boundary must be `completed` or `error`; `pending` or `running` stops the prefix before its owning user/assistant turn. A user turn without a finished assistant response is deferred. Every finalized part has fixed expansion cardinality, so normal tool completion cannot renumber an accepted prefix. A later export that changes an already accepted canonical payload is genuine source drift.

If OpenCode rewrites history or inserts an earlier finalized message, the sequence drifts. Report source drift; do not reorder or guess. An unexpected export schema fails closed. A missing or incompatible executable skips OpenCode only when no explicit OpenCode executable was configured; an explicitly configured but invalid executable fails preflight.

### 8.4 Freeze and prepare

Freeze becomes a project-identity merge across the three adapters, sorted by `StartedAt`. One review is still one run/job, one worker, and one ledger. A missing or uninstalled source is skipped. A configured source with corrupt data narrowed to the current project fails the whole freeze.

Tests use trimmed Claude JSONL fixtures and captured synthetic OpenCode `session list` / `export` JSON fixtures behind a fake executable. Tests never scan the live local database or depend on a user's session history.

## 9. Invocation paths

### 9.1 In-agent Skill

The current conversation is the worker. It runs prepare, writes one proposal, apply, and sync, using the same Skill rules as Codex today. It does not spawn a second Claude, OpenCode, or Codex process.

A Skill invocation freezes the pending set at start time. The current conversation only sees evidence up to that boundary; the review turn itself remains pending for the next review. After each successful packet apply, sync immediately so Obsidian can refresh without a live job.

The Skill still must not read raw session stores, mutate Git, or edit `ledger.json`. "Do not read raw JSONL" becomes "do not read any source store."

### 9.2 Obsidian worker

Obsidian keeps the durable one-shot job: freeze, isolated proposal worker, trusted apply/sync. The primary action remains `总结并同步`. Settings store a default worker kind plus an absolute executable path. CLI review commands take `--agent-kind` and `--agent-executable`. Obsidian and in-agent review share one durable owner registry. Job start and every interactive command acquire the current project and global kernel leases before checking or changing that registry.

When no default worker is configured, Obsidian can sync but cannot start a review.

### 9.3 Worker contract

An Obsidian worker must prove: ephemeral non-interactive run, structured output, empty tool registry or fail on any tool event, private read-only work root, no Project/Vault access, and no raw session reads. `AgentAdapter.Verify` remains the capability gate. Failure is `E_AGENT_INCOMPATIBLE`. Skills do not need this gate.

- Codex: the existing adapter remains the Obsidian worker.
- Claude: ship the Skill first. The Obsidian adapter uses the authenticated local CLI without `--bare`: `claude -p --output-format stream-json --verbose --json-schema ... --safe-mode --tools "" --strict-mcp-config --mcp-config {"mcpServers":{}} --no-session-persistence` from the private work root. Verification must prove the installed version accepts that fixed argv, returns a schema-valid final result, and emits no tool event. OAuth, keychain, API-key, and supported third-party authentication remain Claude's responsibility.
- OpenCode: ship the Skill or command first. `opencode run --format json --pure` does not currently prove a no-tools or JSON-schema contract, so it cannot be saved as the Obsidian default worker in this version.

The same Skill semantics are packaged per host: Codex Skill, Claude Skill, OpenCode command or Skill. Wrappers differ; prepare/apply scripts and schemas do not.

## 10. Durable in-agent run, pending freeze, and usage

### 10.1 Durable in-agent run

Project-wide review does not need to guess the host's current session ID. The current conversation is the semantic worker, not a source selector. `interactive begin` returns every pending canonical session for the project, and later `prepare` calls use those IDs exactly.

The Skill starts with:

```text
session-reviewer interactive begin --cwd "$PROJECT_ROOT" --json
```

`begin` acquires the project and global kernel leases, freezes the pending set, persists one active-owner record bound to the authenticated project identity, and returns an unguessable `run_id` plus the frozen session list. It then releases the kernel leases. Every `prepare`, `apply`, and `sync` in that run reacquires both leases, verifies the owner record, project identity, and exact `run_id`, performs one bounded operation, updates the owner heartbeat, and releases the leases. Obsidian start checks the same record and returns `E_AGENT_BUSY` while it is active. `interactive complete` clears ownership only after every frozen session is accepted and final sync succeeds. `interactive abort` clears a run while preserving already accepted cursor/apply work. An owner expires after six hours without a successful run command and is recovered under both leases before another run starts.

### 10.2 Skill freeze

Obsidian jobs freeze inside the worker. In-agent reviews freeze atomically with durable ownership through `interactive begin`; there is no standalone unlocked `pending` command.

```text
session-reviewer interactive begin --cwd "$PROJECT_ROOT" --json
```

The result lists pending canonical `session_id`, `started_at`, and the start-time `upper` line/hash. It writes an owner record but starts no worker. Later prepare calls must reuse the run ID and that upper bound:

```text
session-reviewer prepare checkpoint --run-id RUN --session ID --until-line N --until-hash HASH ...
```

A mismatched bound is source drift. The Skill may not rescan for a new bound. Failure to create or authenticate ownership is `E_AGENT_BUSY` or `E_RUN_INVALID`.

If nothing is pending, the Skill performs deterministic sync and then completes the owner run.

### 10.3 Usage

The clean multi-agent release increments the evidence, proposal, and ledger contracts. Old packets, proposals, ledgers, cursors, and plugin worker settings are out of contract and are not migrated. Each source maps reviewed counters; if the mapping is not exact, omit usage and emit one canonical `usage_unavailable:<reason>:<count>` warning. Reasons are `inexact_mapping`, `totals_mismatch`, and `missing_model`.

- Codex: existing `token_count` events
- Claude: assistant `message.usage`; `cache_read_input_tokens` maps to cached input, `cache_creation_input_tokens` to cache write; model is `message.model`; thinking is not evidence and is not invented as reasoning
- OpenCode: sum assistant message tokens (`input`, `cache.read`, `cache.write`, `output`, `reasoning`); model is `providerID/modelID`. If session-level totals disagree with the sum, omit usage and warn

Costs remain public USD list prices. Subscriptions are not discounts. Exact source token usage is retained even when a model has no trusted HTTPS catalog price. Source session and project accounting add `pricing_complete`. Each individually priced model keeps its `pricing` and `cost_usd`; an unpriced model omits both. Aggregate `total_cost_usd` and cost-share percentages are omitted unless every included model is priced. Obsidian shows tokens plus `价格未覆盖` for incomplete pricing. Review-run usage stays separate from source-session totals and uses the same incomplete-pricing semantics.

## 11. CLI and Obsidian contract

`--sessions-root` / `SESSION_REVIEWER_SESSIONS_ROOT` override only Codex.

Claude: `--claude-sessions-root` / `SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT`.

OpenCode: `--opencode-executable` / `SESSION_REVIEWER_OPENCODE_EXECUTABLE`; an unconfigured source may resolve `opencode` from `PATH` to an authenticated absolute executable.

`interactive begin|complete|abort` are Skill commands, not Obsidian commands. Their JSON contains the run ID and frozen public boundaries, but no source paths, record bodies, raw owner record, or executable path. The run ID is never written into an evidence packet or proposal.

Review commands require an explicit kind:

```text
session-reviewer review agent verify --agent-kind KIND --executable ABS --json
session-reviewer review start --project-id ID --agent-kind KIND --agent-executable ABS --json
session-reviewer review retry --job-id ID --agent-kind KIND --agent-executable ABS ...
```

`KIND` is `codex`, `claude`, or `opencode`. Verify echoes the requested kind. OpenCode returns `compatible: false` until the no-tools contract is proven. Plugin argv remains an allowlist of absolute paths and fixed flags.

Plugin settings store `agentKind` plus an absolute executable. A previous `codexPath`-only setting is ignored; the user re-verifies the default worker. Selecting OpenCode as the Obsidian worker is rejected with a compatibility error; the in-agent Skill still works.

## 12. Failure boundaries

- Missing or uninstalled unconfigured source: skip that source.
- Present source with corrupt data narrowed to the current project: fail freeze/begin.
- Claude filename UUID disagrees with record `sessionId`: fail.
- OpenCode CLI output schema, encoding, bounds, stable-prefix, or reproducible order failure: fail.
- `--until-line` / `--until-hash` disagree with the source: fail as source drift.
- Missing HTTPS list price for a used model: retain tokens, mark pricing incomplete, and omit costs.
- Unproven Obsidian worker: `E_AGENT_INCOMPATIBLE` and refuse to save it.
- Concurrent in-agent review and Obsidian job: durable owner check returns `E_AGENT_BUSY`.
- Missing, expired, mismatched, or completed interactive owner: `E_RUN_INVALID`.

Never skip a failed session to continue later sessions. Earlier fully accepted and synchronized work remains.

## 13. Compatibility

Evidence becomes v3, proposal becomes v2, and ledger becomes v3 so incomplete pricing and canonical usage warnings are represented directly. `jsonl_line` remains the monotonic sequence number. Unprefixed session IDs, old packet/proposal/ledger/cursor files, and old plugin `codexPath` settings are out of contract and are not loaded as aliases. Users start from a fresh canonical review on this version.

The 2026-08-28 orchestration design remains the Obsidian job model. This spec replaces only its Codex-only v1 product limit and its leave-Claude/OpenCode-unimplemented non-goal.

## 14. Delivery slices

1. Land the v3/v2/v3 accounting contracts and the provider-neutral Reader interface as one Codex vertical slice. Canonical `codex.` IDs, prepare, freeze, apply, sync, and all repository tests are green in the same commit.
2. Add durable interactive ownership plus Claude collection and Claude Skill. A Claude-started review accepts pending Claude and Codex sessions into one ledger visible in Obsidian.
3. Add the OpenCode documented-CLI collector with stable-prefix tests plus the OpenCode Skill or command. All three sources can enter the same ledger.
4. Obsidian settings become default worker kind plus path. Claude may be saved only after its fixed-argv conformance gate passes. OpenCode's isolated worker stays closed.

## 15. Testing

- Prefix parse/format, rejection of unprefixed IDs, and mixed-case OpenCode IDs
- Claude filename/`sessionId` mismatch and OpenCode schema mismatch
- Source drift, including `--until-*` reuse versus rescan and OpenCode pending/running-to-completed tail growth
- Missing source skipped; corrupt in-project source fails closed
- Durable interactive owner busy, expiry, abort, completion, and per-command authentication paths
- Plugin argv allowlist for `--agent-kind`
- Ignored legacy `codexPath` settings; user must save `agentKind` plus executable
- OpenCode Obsidian worker rejected as incompatible
- Fixture-only Claude JSONL and fake-CLI OpenCode list/export JSON; no live database scans
- Exact source tokens with unknown price produce `pricing_complete=false`, no zero cost, and no semantic-review failure
- Existing Codex prepare/apply/review tests remain green without weakened assertions

Repository gates stay: focused TDD, `go test ./...`, targeted race, `go vet ./...`, `go mod tidy -diff`, plugin lint/test/build, macOS arm64/amd64 and Windows amd64 builds, and credential scanning.

Real acceptance still requires the installed Obsidian bundle plus one authorized review started from Claude and one from OpenCode against a connected project, confirming the same ledger and browser update. Claude worker acceptance additionally proves existing local authentication works without `--bare`; OpenCode collector acceptance proves UTF-8 and complete bounded JSON on macOS and Windows. A passing unit suite is not that proof.
