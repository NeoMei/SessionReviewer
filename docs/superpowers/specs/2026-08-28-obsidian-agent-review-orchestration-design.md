# Obsidian-triggered Agent review orchestration

Date: 2026-08-28
Status: approved design, pending implementation plan

## Problem

SessionReviewer currently separates two operations:

1. a semantic SessionReviewer Skill reviews bounded Codex evidence and produces a proposal; and
2. the deterministic CLI validates, applies, and synchronizes accepted state between the Project and Obsidian Vault.

The Obsidian plugin can inspect and repair synchronization state, but it cannot start a semantic review. A user must leave Obsidian, invoke an Agent manually, wait for proposal/apply, and then synchronize again. The desired behavior is one primary Obsidian action that finds all pending Codex sessions for the selected project, summarizes them, accepts valid results, and synchronizes the updated project context.

## Goals

- Add one primary Obsidian action named `总结并同步`.
- Process every Codex session associated with the selected project that has evidence after its accepted cursor.
- Include a bounded checkpoint of an active session up to the click-time boundary.
- Reuse the locally authenticated Codex CLI and its default model. Obsidian stores no API key.
- Apply valid proposals and synchronize them automatically without a routine confirmation step.
- Preserve explicit, resumable cursor, proposal, apply, and sync boundaries.
- Recover job status after an Obsidian reload or restart.
- Preserve the current project-evolution UI structure and data behavior.
- Leave an adapter boundary for Claude Code and OpenCode without implementing those adapters in v1.

## Non-goals

- Supporting Agent implementations other than Codex in v1.
- Resuming, messaging, or interrupting the original work session.
- Running an always-on local daemon or installing a filesystem watcher.
- Allowing an arbitrary user-defined Agent command.
- Giving the Codex review worker write access to Project, Vault, ledger, cursor, or SessionReviewer data roots.
- Automatically skipping a failed session and continuing later sessions.
- Changing existing project-review Markdown, review schema v2, or the existing human-facing layout.
- Publishing a release or updating the Obsidian community listing as part of implementation.

## Confirmed product decisions

- V1 supports Codex only, behind a future-facing `AgentAdapter` interface.
- A job automatically applies and synchronizes accepted results.
- A click freezes and processes all pending project sessions in chronological order.
- The locally authenticated Codex CLI selects its default model; there is no per-project model picker.
- `总结并同步` is the only permanent primary action.
- When no evidence is pending, the primary action performs deterministic synchronization without starting Codex.
- `仅同步已有修改` appears only as a contextual recovery action after an Agent/review failure.
- A job stops at the first failed session. Earlier fully accepted and synchronized work remains.
- The Codex worker is a proposal generator only. The trusted CLI owns prepare, apply, cursor advancement, and sync.
- No existing evolution, decision, usage, risk, resume, editing, or responsive layout may be reorganized.

## Architecture

The first version uses a durable one-shot background worker rather than plugin-owned orchestration or an always-on service.

```text
Obsidian ProjectEvolutionView
        |
        | fixed allow-listed review command
        v
SessionReviewer CLI control plane
  - validate mapping and executable
  - acquire project/global Agent leases
  - create durable job
  - spawn one-shot worker
        |
        v
Review worker
  - freeze pending session queue
  - prepare bounded packet
  - build bounded prompt bundle
  - call CodexAdapter for structured proposal only
  - validate/apply through existing CLI packages
  - sync through existing sync engine
        |
        v
Accepted Project ledger <-> Obsidian Vault
```

### Component boundaries

#### Obsidian plugin

The plugin is a thin client. It starts, polls, cancels, retries, and presents a safe status projection. It never reads Codex session JSONL, evidence packets, proposals, Codex stdout/stderr, private job files, or Project filesystem paths.

The plugin continues to invoke processes with an absolute executable path, `execFile`, and `shell: false`. New review argv is constructed by typed methods and an exact allowlist rather than accepting strings from Markdown.

#### CLI review control plane

The control plane validates the project mapping, the configured Codex executable, compatible Codex capabilities, job identity, and leases. `start` creates a durable job and returns quickly. A separately spawned copy of the SessionReviewer executable runs the job. `status`, `cancel`, and `retry` use only the private job store.

No existing sync queue record is reused for Agent work. The current sync queue represents entity-copy retry state; semantic review jobs have different identity, lifecycle, security, and accounting requirements.

#### Review worker

The worker owns orchestration order but does not implement semantic judgment. It freezes the click-time session queue before starting Codex, which prevents the worker's own ephemeral Codex session from entering the job. New sessions or records written after that frozen boundary remain pending for the next click.

For each pending session, the worker repeats prepare, propose, apply, and sync until the packet reports no more bounded evidence. It then proceeds to the next frozen session. The first failure stops the job.

#### CodexAdapter

`CodexAdapter` implements a provider-neutral contract:

```text
Verify(executable) -> capability and version result
GenerateProposal(context, output schema, cancellation) -> proposal plus review-run usage
Cancel() -> bounded process termination result
```

The adapter launches a new ephemeral Codex execution. It never resumes the target session. It uses the existing local authentication and does not pass a model override, so Codex selects its default model.

#### Trusted data plane

Existing prepare, proposal validation, apply, receipt recovery, cursor compare-and-swap, and sync packages remain authoritative. The worker calls package/service boundaries or the same typed CLI operations; it does not reproduce their validation logic.

## Codex proposal-only boundary

The coordinator builds a bounded prompt bundle in memory from:

- the evidence packet produced by `prepare`;
- only the accepted review/history context required by the proposal contract;
- a versioned SessionReviewer worker prompt derived from the current Skill rules;
- proposal schema and apply invariants; and
- exact output requirements.

The complete bounded bundle is passed to Codex through stdin. The model does not need filesystem or external tools to complete the task. The adapter must:

- run an ephemeral, non-interactive Codex turn;
- disable the shell tool and all optional external tools for the supported Codex version;
- use a read-only sandbox as defense in depth;
- ignore project rules and user tool/plugin instructions for this isolated worker;
- require structured output with the proposal JSON schema;
- parse the JSON event stream and reject any tool-call event;
- never use a dangerous sandbox/approval bypass; and
- fail closed with `E_AGENT_INCOMPATIBLE` if the installed Codex version cannot prove this no-tools contract.

The Agent executable path is a user-configured absolute path in the existing plugin settings area. It is never read from Markdown. The CLI verifies the executable version and capabilities before saving or starting a job.

This boundary enforces that the Codex worker can produce bytes for a candidate proposal but cannot apply those bytes or write to Project, Vault, cursor, Base, machine ledger, or private SessionReviewer state.

## CLI command contract

The public command family is:

```text
session-reviewer review agent verify --executable ABSOLUTE_PATH --json
session-reviewer review start --project-id PROJECT_ID --agent-executable ABSOLUTE_PATH --json
session-reviewer review status --project-id PROJECT_ID --json
session-reviewer review cancel --job-id JOB_ID --json
session-reviewer review retry --job-id JOB_ID --agent-executable ABSOLUTE_PATH --json
```

The existing deterministic sync command remains the recovery path. The plugin adds one exact `sync --project-id PROJECT_ID` action for the contextual `仅同步已有修改` control.

`start` returns a safe projection containing a job ID and state after the private job record and worker launch are durable. `status --project-id` returns the selected project's active job, otherwise its most recent visible terminal result. It never scans all Codex sessions merely to render an idle page.

The plugin command runner accepts only valid project/job IDs, a configured absolute executable path, fixed flags, and the exact action shapes above. Each short control command retains a bounded timeout. The detached worker has its own phase deadlines.

## Durable job model

Private job state lives under the platform SessionReviewer data root, outside Project and Vault. Directories are private and records use the repository's rooted atomic writer, authenticated preimages, and lock conventions.

The internal record contains:

- schema version and stable job ID;
- project ID and authenticated canonical project identity;
- Agent kind, verified executable identity, and compatible version;
- state, phase, timestamps, attempt, and cancellation request;
- the frozen ordered internal session IDs and per-session progress;
- accepted cursor boundaries and safe result counts;
- review-run usage and list-price accounting;
- stable safe error code; and
- worker ownership/lease information required for restart recovery.

The public status projection excludes session paths, canonical filesystem paths, raw session text, packet content, proposal content, prompts, stdout/stderr, source hashes, and internal errors.

Job states are:

```text
queued -> running -> completed
                  -> failed
                  -> cancel_requested -> cancelled
failed -> retrying -> running
```

Running phases are:

```text
preflight -> scanning -> preparing -> reviewing -> applying -> syncing
```

Only one review job may be active for a project. V1 also takes one global Codex worker lease to prevent several automatic review jobs from unexpectedly consuming the account concurrently. A second start returns a safe busy result rather than creating an unbounded queue.

## Processing semantics

1. Authenticate the project mapping and acquire project/global leases.
2. Validate the Codex executable and no-tools capability contract.
3. Discover pending Codex sessions associated with the project.
4. Freeze their chronological order and click-time source boundaries.
5. If none are pending, run deterministic sync and complete without Codex.
6. Prepare one bounded packet for the first session.
7. Build the bounded prompt bundle and request one structured proposal.
8. Validate and apply the proposal through the existing all-or-fail apply path.
9. Synchronize the newly accepted state immediately.
10. If the packet has more evidence, repeat for the successor packet only after apply and sync succeed.
11. Continue with the next frozen session.
12. Refresh safe result counts and mark the job complete.

An active source session can append records while review is running. The frozen packet boundary remains stable; later records are not silently consumed and remain pending for the next click.

Repeated start/retry is idempotent at accepted cursor and apply receipt boundaries. The system never infers completion from an Agent process exit alone.

## Failure, recovery, and cancellation

### Preflight or discovery failure

The job fails without any proposal, ledger, cursor, Base, Project, or Vault mutation.

### Agent/authentication/structured-output failure

The current packet is not applied and its cursor does not advance. The job stops at that session. Safe error codes distinguish unconfigured, incompatible, authentication, timeout, forbidden tool call, cancellation, and malformed output.

### Proposal rejection

Existing schema, evidence, revision, accounting, and invariant validation rejects the entire proposal. No partial ledger update or cursor advancement occurs.

### Apply interruption

Existing receipt recovery determines whether the apply was accepted. Retry must recover or confirm that boundary before preparing new evidence. It may not regenerate against an uncertain cursor.

### Sync conflict or partial sync

Accepted ledger content is not destructively rolled back. The job stops and exposes the existing conflict/recovery UI. After conflict resolution or deterministic sync recovery, retry continues from accepted state.

### Earlier successful sessions

Every successful apply is followed immediately by sync. If a later session fails, earlier accepted and synchronized sessions remain trusted and visible. The system does not present the whole multi-session job as an atomic batch.

### Cancellation

Before apply, cancellation terminates the proposal-only Codex process and discards the unaccepted result. During apply or sync, the worker records `cancel_requested`, lets the current atomic operation finish, and stops before the next packet/session. It never kills a trusted write in its commit window and never rolls back accepted history.

### Retry

Retry reauthenticates Project and Agent identities, reacquires leases, recovers any apply receipt, and continues from accepted cursor. It does not blindly reuse untrusted proposal bytes.

## Safe errors

The first-version public error vocabulary includes:

- `E_AGENT_UNCONFIGURED`
- `E_AGENT_INCOMPATIBLE`
- `E_AGENT_AUTH`
- `E_AGENT_BUSY`
- `E_AGENT_TIMEOUT`
- `E_AGENT_TOOL_FORBIDDEN`
- `E_AGENT_CANCELLED`
- `E_PROPOSAL_REJECTED`
- `E_APPLY_RECOVERY`
- `E_SYNC_CONFLICT`
- `E_SYNC_PARTIAL`

Human-facing messages are fixed, concise Chinese explanations. Internal errors, command output, paths, prompt content, and evidence are never concatenated into plugin messages.

## Accounting

Existing accepted session accounting continues to describe the source work sessions and remains the input to the current project usage view.

The Codex summarizer's own usage is separate review-run accounting. The adapter captures the actual Codex model and usage events, applies the same public list-price convention, and stores the result in the private job record. Completion UI may show this run's summarization cost, but v1 does not add it to source-session totals or double-count it in existing usage cards.

Temporary prompt, packet, and proposal material is removed after each success or terminal failure once the safe diagnostic code and required digests are recorded. Compact job status/accounting records may remain for recovery and recent-result display; they contain no raw evidence.

## Obsidian UI integration

The current `renderReadyView` order remains:

```text
header -> resume -> risks -> tabs -> selected panel
```

No evolution, decision, usage, risk, editing, project picker, or virtual-list renderer is restructured.

The only steady-state main-view change is a `总结并同步` control inside the existing right-side header metadata area. Job state uses a compact banner prepended through `ProjectEvolutionView`, following the current diagnostic-banner pattern rather than inserting content into existing panels.

UI states are:

- idle: primary action available; no session scan/count is required to render;
- running: phase and session position, with a cancel action and no fake percentage;
- completed: safe counts and review-run cost, while the normal page has already refreshed;
- failed: fixed explanation, retry where allowed, diagnostic code, and contextual `仅同步已有修改` where safe.

The plugin never displays raw Agent logs. Existing selectors and layout rules remain unchanged. New styles use dedicated `sr-review-*` selectors and only add responsive rules for the new controls. At narrow widths, the control wraps within the existing header metadata column instead of changing the page breakpoint or card grids.

## Testing strategy

### Go unit and integration tests

- job schema, permissions, validation, atomic writes, authenticated updates, and restart recovery;
- project and global lease acquisition, stale-owner recovery, and concurrent starts;
- pending-session discovery, chronological freeze, active-session boundary, and worker-session exclusion;
- AgentAdapter version/capability verification and fixed argv;
- no-tools enforcement, forbidden event termination, structured output parsing, cancellation, timeout, and safe error mapping;
- no raw packet, prompt, session text, stdout/stderr, or absolute path in public status;
- proposal rejection with zero mutation and zero cursor advancement;
- apply receipt recovery and idempotent retry;
- immediate sync after each accepted packet and stop-on-first-failure behavior;
- sync conflict/partial failure recovery; and
- review-run usage and list-price accounting without source-session double counting.

Tests use a fake Agent executable for deterministic success, malformed output, tool-call attempts, auth failure, timeout, signal handling, and process-tree termination. A small opt-in local Codex smoke verifies the supported-version no-tools contract without being part of ordinary unit tests.

### Obsidian plugin tests

- extend the typed runner allowlist for review and exact sync actions;
- verify no shell invocation or arbitrary argv/path source;
- start, poll, reload recovery, cancel, retry, completion, and failure UI states;
- verify existing status/conflict/edit interactions still work while a job result is present;
- accessibility names, live-region behavior, keyboard navigation, and disabled/busy controls;
- preserve serialized no-job DOM except for the new header action;
- preserve existing evolution, decision, usage, risk, and responsive fixtures; and
- test 390 px, 860 px, and desktop-width wrapping without horizontal overflow.

All existing plugin tests must pass without weakening assertions.

### Repository gates

- focused Go and plugin suites during TDD;
- `go test ./...`, targeted race suites, `go vet ./...`, and `go mod tidy -diff`;
- plugin lint, tests, build, package-content checks, and deterministic packaging;
- macOS arm64/amd64 and Windows amd64 builds; and
- credential/high-entropy scan and `git diff --check`.

### Real Obsidian acceptance

Before installing a candidate bundle, capture the current real `NeoMei-Docs` view and key element geometry. After installation:

1. confirm project title, goal, resume card, risks, tabs, timeline/detail layout, decision cards, usage cards, editing, and project picker remain behaviorally and visually unchanged;
2. confirm desktop and narrow-width layout do not shift or overflow;
3. configure and verify the real Codex executable without storing an API key;
4. run a safe fixture job through the installed bundle;
5. run one authorized real `总结并同步` action against the connected project;
6. verify Agent usage, accepted entities, cursor advancement, Project/Vault convergence, and repeated no-op behavior; and
7. exercise cancellation and one recoverable failure without leaving an active lease or stale UI state.

A passing build or synthetic DOM test is not a substitute for the installed-bundle acceptance.

## Compatibility and evolution

V1 adds a Codex adapter behind a provider-neutral interface but does not expose generic command configuration. A later Claude Code or OpenCode adapter must provide the same proposal-only, no-write, structured-output, cancellation, usage, and safe-error contract before it can appear in the plugin.

The existing manual Skill workflow remains supported. This feature adds an automatic orchestration path; it does not change the meaning of accepted evidence, cursor, proposal, apply, sync, or human-editable Markdown.

## Documentation changes for implementation

Implementation documentation must explain:

- the new primary action and contextual sync fallback;
- Codex executable verification and compatibility errors;
- what information is sent to the Codex model;
- separation between source-session and review-run accounting;
- cancellation and retry semantics;
- stop-on-first-failure behavior; and
- that no watcher, daemon, other Agent adapter, Git mutation, publish, or deployment is included.
