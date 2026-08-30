# Multi-agent SessionReviewer

- Status: approved design, pending implementation plan
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
- Preserve the current evidence, cursor, proposal, apply, and sync contracts.
- Keep raw session stores local, redacted, and unread by Skills.
- Fail closed on identity collisions, source drift, and unproven worker capabilities.

## 3. Non-goals

- Evidence, proposal, or ledger schema v3.
- A sidecar copy of Claude or OpenCode sessions rewritten as Codex JSONL.
- An OpenCode Obsidian worker before it can prove the proposal-only contract.
- A per-source worker split inside one click.
- Changing the evolution, decision, usage, risk, or resume layout.
- Git mutation, a watcher, a daemon, cloud collection, or a new desktop GUI.
- Publishing a release or community-plugin update as part of this design.

## 4. Confirmed product decisions

- One click reviews every pending session for the selected project, regardless of source.
- The starter is the semantic worker: the current Claude, OpenCode, or Codex conversation writes the proposal. Obsidian uses one configured default worker.
- Session IDs are always `provider.nativeId`. Unprefixed historical IDs are a Codex alias only.
- Source adapters emit a canonical ordered record stream. `jsonl_line` remains the 1-based sequence number.
- Skills never read raw JSONL or SQLite. The only evidence is a prepared packet.
- Obsidian workers must prove proposal-only, no-write, structured-output behavior. OpenCode's isolated worker stays closed until that proof exists.
- Missing source roots are skipped. Corrupt data that may belong to the current project fails the whole freeze.

## 5. Selected approach

Three approaches were compared.

A. Export Claude JSONL and OpenCode SQLite into Codex-shaped sidecar JSONL, then reuse the current locator and extractor. This is the smallest code change and the weakest provenance story: OpenCode would keep a private copy, export instability would look like cursor drift, and the engine would no longer hash the real source.

B. Provider adapters plus namespaced session IDs plus a canonical record stream. Selected. The ledger, Obsidian view, and CAS cursor stay project-shaped. Each source is responsible for Discover and Stream. Evidence packets keep their current fields.

C. Opaque per-source cursor tokens and evidence v3. Cleaner for a fourth and fifth agent, but it would rewrite proposal, ledger, Skill, plugin, and tests now. Two additional sources do not justify that migration.

## 6. Architecture

```text
Codex JSONL -+
Claude JSONL-+- Source adapters - canonical records - prepare - evidence packet
OpenCode DB -+                                              |
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

Read compatibility: an unprefixed historical ID is exactly `codex.<old-value>`. Cursor lookup tries `codex.<id>` first, then the legacy filename. Write compatibility: the next successful apply or cursor commit stores the canonical ID. There is no bulk rewrite.

`--session` and Skill wrappers accept canonical IDs. Unprefixed input is accepted only as a Codex alias and is normalized immediately.

Human display maps prefixes to short labels: Codex, Claude Code, OpenCode. The same rule applies to timeline nodes, session titles, and evidence citations. Usage cards remain model-aggregated. The machine schema does not gain a `source` field.

## 8. Source adapters

Each source implements three operations: resolve its root, discover sessions for the authenticated project identity, and stream canonical records. A canonical record matches the current `session.Record`: 1-based sequence, timestamp, type, payload, and SHA-256. Cursor CAS remains sequence plus hash.

### 8.1 Codex

Keep the current sessions-root discovery, JSONL line numbers, and source-line hashes. New writes use `codex.<nativeId>`. Existing unprefixed IDs remain aliases.

### 8.2 Claude Code

Default root is `~/.claude/projects` on macOS and `%USERPROFILE%\.claude\projects` on Windows. Override with `--claude-sessions-root` or `SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT`. Files are `<uuid>.jsonl`. Project membership uses the physical `cwd` inside records, not the encoded directory name. Sequence numbers are file line numbers; hashes cover the source line. Filename UUID and record `sessionId` must match.

Included evidence: `user` / `assistant` text as messages, `tool_use` as tool calls, `tool_result` as tool results. `thinking`, queue, and attachment records advance the cursor only.

### 8.3 OpenCode

Default source is the official data-directory `opencode.db`, opened read-only. Override with `--opencode-db` or `SESSION_REVIEWER_OPENCODE_DB`. If the Windows path is not the documented location, the flag is required; the engine does not guess `%APPDATA%`. Session `directory` must match the current project identity.

Canonical order is messages by `(time_created, id)`, then parts of one message by `id`, matching existing SQLite indexes. Hashes cover a SessionReviewer-defined canonical payload, not SQLite's stored JSON byte layout. `text` parts become messages; `tool` parts become tool calls and results. `step-start`, `step-finish`, and `reasoning` advance the sequence only.

If OpenCode rewrites history or inserts an earlier message, the sequence drifts. Report source drift; do not reorder or guess. An unexpected schema fails closed.

### 8.4 Freeze and prepare

`Discover(sessionsRoot)` becomes a project-identity merge across the three sources, sorted by `StartedAt`. One review is still one job, one worker, and one ledger. A missing or uninstalled source is skipped. A present root with corrupt data that may belong to the current project fails the whole freeze.

Tests use trimmed Claude JSONL fixtures and a small OpenCode SQLite fixture. They must not scan the live local database.

## 9. Invocation paths

### 9.1 In-agent Skill

The current conversation is the worker. It runs prepare, writes one proposal, apply, and sync, using the same Skill rules as Codex today. It does not spawn a second Claude, OpenCode, or Codex process.

A Skill invocation freezes the pending set at start time. The current conversation only sees evidence up to that boundary; the review turn itself remains pending for the next review. After each successful packet apply, sync immediately so Obsidian can refresh without a live job.

The Skill still must not read raw session stores, mutate Git, or edit `ledger.json`. "Do not read raw JSONL" becomes "do not read any source store."

### 9.2 Obsidian worker

Obsidian keeps the durable one-shot job: freeze, isolated proposal worker, trusted apply/sync. The primary action remains `总结并同步`. Settings store a default worker kind plus an absolute executable path. CLI review commands take `--agent-kind` and `--agent-executable`. The global Agent lease still prevents an in-agent review and an Obsidian job from running together.

When no default worker is configured, Obsidian can sync but cannot start a review.

### 9.3 Worker contract

An Obsidian worker must prove: ephemeral non-interactive run, structured output, empty tool registry or fail on any tool event, private read-only work root, no Project/Vault access, and no raw session reads. `AgentAdapter.Verify` remains the capability gate. Failure is `E_AGENT_INCOMPATIBLE`. Skills do not need this gate.

- Codex: the existing adapter remains the Obsidian worker.
- Claude: ship the Skill first. An Obsidian adapter may use `claude -p --json-schema --bare` with MCP and tools disabled, and only after verification proves no tool events.
- OpenCode: ship the Skill or command first. `opencode run --format json --pure` does not currently prove a no-tools or JSON-schema contract, so it cannot be saved as the Obsidian default worker in this version.

The same Skill semantics are packaged per host: Codex Skill, Claude Skill, OpenCode command or Skill. Wrappers differ; prepare/apply scripts and schemas do not.

## 10. Current session, pending freeze, and usage

### 10.1 Current session

`--session` wins and must be a canonical ID, with unprefixed Codex aliases normalized.

Otherwise:

1. `--current-session-id`
2. Host environment IDs: Codex keeps `CODEX_THREAD_ID` / `CODEX_SESSION_ID`; Claude and OpenCode use only documented session-id variables
3. cwd plus time window across all three sources

Skill wrappers must pass the current canonical ID. Ambiguous cwd/time matches fail closed. `--sessions-root` still overrides only Codex.

### 10.2 Skill freeze

Obsidian jobs freeze inside the worker. In-agent reviews freeze at Skill start through a read-only command:

```text
session-reviewer pending --cwd "$PROJECT_ROOT" --json
```

The result lists pending canonical `session_id`, `started_at`, and the start-time `upper` line/hash. It writes no job and starts no worker. Later prepare calls must reuse that upper bound:

```text
session-reviewer prepare checkpoint --session ID --until-line N --until-hash HASH ...
```

A mismatched bound is source drift. The Skill may not rescan for a new bound. `pending` runs under the global Agent lease; failure to acquire it is `E_AGENT_BUSY`.

If nothing is pending, the Skill performs deterministic sync only.

### 10.3 Usage

`session_usage` is unchanged. Each source maps reviewed counters; if the mapping is not exact, omit usage and warn instead of writing zeros.

- Codex: existing `token_count` events
- Claude: assistant `message.usage`; `cache_read_input_tokens` maps to cached input, `cache_creation_input_tokens` to cache write; model is `message.model`; thinking is not evidence and is not invented as reasoning
- OpenCode: sum assistant message tokens (`input`, `cache.read`, `cache.write`, `output`, `reasoning`); model is `providerID/modelID`. If session-level totals disagree with the sum, omit usage and warn

Costs remain public USD list prices. Subscriptions are not discounts. Models missing an HTTPS catalog price fail apply rather than recording zero cost. Review-run usage stays separate from source-session totals.

## 11. CLI and Obsidian contract

`--sessions-root` / `SESSION_REVIEWER_SESSIONS_ROOT` override only Codex.

Claude: `--claude-sessions-root` / `SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT`.

OpenCode: `--opencode-db` / `SESSION_REVIEWER_OPENCODE_DB`.

`pending` is a Skill command, not an Obsidian command. Its JSON contains no source paths and no record bodies.

Review commands require an explicit kind:

```text
session-reviewer review agent verify --agent-kind KIND --executable ABS --json
session-reviewer review start --project-id ID --agent-kind KIND --agent-executable ABS --json
session-reviewer review retry --job-id ID --agent-kind KIND --agent-executable ABS ...
```

`KIND` is `codex`, `claude`, or `opencode`. Verify echoes the requested kind. OpenCode returns `compatible: false` until the no-tools contract is proven. Plugin argv remains an allowlist of absolute paths and fixed flags.

Saved Codex executable settings migrate to `{kind: "codex", executable: previousPath}`. Selecting OpenCode as the Obsidian worker is rejected with a compatibility error; the in-agent Skill still works.

## 12. Failure boundaries

- Missing or uninstalled source root: skip that source.
- Present root with corrupt data that may belong to the current project: fail freeze/pending.
- Claude filename UUID disagrees with record `sessionId`: fail.
- OpenCode schema or unreproducible order: fail.
- `--until-line` / `--until-hash` disagree with the source: fail as source drift.
- Ambiguous cwd/time current session: fail.
- Missing HTTPS list price for a used model: fail apply.
- Unproven Obsidian worker: `E_AGENT_INCOMPATIBLE` and refuse to save it.
- Concurrent in-agent review and Obsidian job: `E_AGENT_BUSY`.

Never skip a failed session to continue later sessions. Earlier fully accepted and synchronized work remains.

## 13. Compatibility

Existing Codex ledgers, cursors, Obsidian mappings, and unprefixed session IDs remain valid Codex aliases. Evidence, proposal, and ledger schemas do not change. `jsonl_line` remains the monotonic sequence number. New projects and old projects share the same loader.

The 2026-08-28 orchestration design remains the Obsidian job model. This spec replaces only its Codex-only v1 product limit and its leave-Claude/OpenCode-unimplemented non-goal.

## 14. Delivery slices

1. Fold Codex into the source-adapter interface. Land canonical `codex.` IDs, aliases, `pending`, and `--until-*` on Codex with equivalent current behavior.
2. Claude collection plus Claude Skill. A Claude-started review accepts pending Claude and Codex sessions into one ledger visible in Obsidian.
3. OpenCode collection plus OpenCode Skill or command. All three sources can enter the same ledger.
4. Obsidian settings become default worker kind plus path. Claude may be saved only after verification. OpenCode's isolated worker stays closed.

## 15. Testing

- Prefix parse/format, unprefixed Codex alias round-trip, and mixed-case OpenCode IDs
- Claude filename/`sessionId` mismatch and OpenCode schema mismatch
- Source drift, including `--until-*` reuse versus rescan
- Missing source skipped; corrupt in-project source fails closed
- Pending lease busy path
- Plugin argv allowlist for `--agent-kind`
- Settings migration from a bare Codex path
- OpenCode Obsidian worker rejected as incompatible
- Fixture-only Claude JSONL and small OpenCode SQLite; no live 800MB+ database scans
- Existing Codex prepare/apply/review tests remain green without weakened assertions

Repository gates stay: focused TDD, `go test ./...`, targeted race, `go vet ./...`, `go mod tidy -diff`, plugin lint/test/build, macOS arm64/amd64 and Windows amd64 builds, and credential scanning.

Real acceptance still requires the installed Obsidian bundle plus one authorized review started from Claude and one from OpenCode against a connected project, confirming the same ledger and browser update. A passing unit suite is not that proof.
