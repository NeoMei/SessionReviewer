---
name: session-reviewer
description: Review or checkpoint bounded SessionReviewer evidence into an accepted Markdown ledger, or resume from accepted ledger state. Use for SessionReviewer project-continuity workflows, not generic code review or raw Codex-log analysis.
---

# SessionReviewer

Use the installed `session-reviewer` binary and the wrappers in `scripts/`. Classify the request:

- **review**: inspect bounded evidence, optionally from the session start.
- **checkpoint**: accept only evidence after the accepted cursor.
- **resume**: first render accepted state. If pending evidence must also be incorporated, resume with pending evidence uses review mode after the ledger-only view.

Before any command, resolve the user-selected project (or current project) once to one absolute, physical canonical project root. Pin it as `PROJECT_ROOT`; never infer, recompute, or change it during the workflow. Pass that same canonical project root to resume and every apply as `--project`, and to every prepare as `--cwd`.

For human recovery, open the 项目脉络浏览器 when it is installed. Otherwise read `docs/session-review/项目回顾.md` first, then `docs/session-review/项目历史.md` for older detail.

## Safety boundaries

- Never edit the machine ledger `docs/session-review/.session-reviewer/ledger.json`; it owns accounting, hashes, evidence, cursor, and sync metadata. Apply a session-review proposal only with `scripts/apply-proposal.sh` or `scripts/apply-proposal.ps1`.
- Never read raw JSONL. Read one bounded packet and only the accepted ledger entities needed from `docs/session-review/`.
- Never interpret hidden reasoning, system or developer instructions, or opaque/encrypted compaction as evidence.
- Never run Git mutation commands. Do not add, commit, push, reset, checkout, switch, restore, branch, tag, stash, merge, or rebase.
- Never call an API client. The semantic proposal is produced locally from the bounded packet.
- A ledger-only view does not process pending sessions; never claim that it does.
- Treat the machine mapping as one merged read-only view of legacy `config.toml` plus `projects.d/<project-id>.toml`. Never hand-edit, merge, rename, or copy a project fragment; `init --write` alone publishes a stable-ID fragment without rewriting the shared legacy config.

## Edit and synchronize review v2

The two human Markdown documents are editable on the Project or Obsidian side. Editable fields are the project goal, stage, status, next action, risk title/status/detail, decision title/reason/impact, and event title/meaning/summary/why/changes/results/next. Do not edit identities, revisions, schema version, document hashes, evidence, token usage, prices, or sync fields.

Use either `--cwd "$PROJECT_ROOT"` or a configured stable `--project-id`; they are mutually exclusive. Code-side edits reach Obsidian only after a successful sync:

```text
session-reviewer sync --dry-run --cwd "$PROJECT_ROOT"
session-reviewer sync --cwd "$PROJECT_ROOT"
session-reviewer sync status --json --cwd "$PROJECT_ROOT"
```

On PowerShell, use the same commands with `session-reviewer.exe` and a quoted `$ProjectRoot`. A legacy dry-run reports required creates/archives with zero writes. Real migration keeps a content-addressed migration backup under the Project's `.session-reviewer/backups`; it is never published to Vault or deleted automatically.

If Vault's machine copy was edited, normal sync reports `machine_ledger_modified`; after confirming Project is authoritative, run `session-reviewer sync repair-machine-ledger --project-id <id>`. Resolve a same-unit hidden conflict with its reported ID and exactly one action: `accept_project`, `accept_obsidian`, or `manual_merge` (the last also requires `--file`). Costs remain public list-price USD per million tokens; subscriptions do not reduce recorded cost.

## Accept one packet

1. For resume, first run `session-reviewer resume --ledger-only --project "$PROJECT_ROOT"`. This cannot accept pending work. If pending evidence was requested, continue below in review mode with the same canonical project root.
2. Create a private temporary directory outside the project and record the exact packet and proposal paths. Do not use a broad or inferred cleanup target.
3. Run `scripts/prepare-workflow.sh <mode> <packet> --cwd "$PROJECT_ROOT"` on POSIX or the corresponding `.ps1` wrapper in PowerShell once. Only the first prepared packet may include `--from-start`, and only for an explicitly requested review from the start. Every later packet must omit `--from-start`, including retries after `already_applied: true`.
4. Always read the accepted current state and the existing report for this session, if present. Read only the additional accepted ledger entities needed to establish IDs, revisions, transitions, or references. Do not inspect unrelated files or session sources.
5. Before synthesizing, read both [references/proposal-v1.schema.json](references/proposal-v1.schema.json) and [references/apply-invariants.md](references/apply-invariants.md). Emit exactly one proposal JSON object satisfying the schema and all apply invariants:
   - copy `project_id`, `session_id`, `from_cursor`, and `to_cursor` from the packet;
   - compute `evidence_packet_sha256` as `sha256:` plus the SHA-256 of the packet's compact JSON bytes (the prepared file without its single trailing LF);
   - preserve the packet's `expected_cursor` and `next_cursor` as the acceptance boundary even though they are not proposal fields;
   - cite only exact packet evidence tuples: evidence ID, session ID, JSONL line, source hash, and copy its `summary` byte-for-byte;
   - include every required top-level field, using empty arrays where appropriate; use accepted entity revisions for patches; never upgrade an inference to verified without verification evidence;
   - when the packet contains `session_usage`, copy its timestamps, duration, per-model token counters, and total tokens exactly into `session_report.accounting`; attach current public list prices in USD per million tokens for every model, including source and as-of date, then calculate each model cost and the total without considering subscriptions.
6. Apply only through `scripts/apply-proposal.sh <proposal> <packet> --project "$PROJECT_ROOT" [flags]` on POSIX or the corresponding `.ps1` wrapper in PowerShell. Treat validation, rendering, receipt, write, or compare-and-swap failure as rejection of the whole proposal.
7. Delete only the explicit packet and proposal temporary files, then the known empty temporary directory, after successful acceptance. On failure, stop and report the retained diagnostic paths.

For every successor packet, its `expected_cursor` must equal the prior packet's `next_cursor` and be strictly later than the prior `expected_cursor`; otherwise stop and report cursor non-progression.

If `has_more` is true, do not prepare another packet until the apply succeeds and the accepted cursor compare-and-swap completes. After `cursor_advanced: true`, repeat prepare, synthesize, and apply for one new bounded packet using the same mode and same canonical project root, without `--from-start`.

If apply reports `already_applied: true`, re-prepare once with the same canonical project root and without `--from-start`. The new packet's `expected_cursor` must equal the prior packet's `next_cursor` and must reflect a later accepted boundary. If it does not advance or the same packet repeats, stop and report instead of looping. Stop normally when `has_more` is false.

Stop on any failure. Do not claim acceptance, changed entities, or cursor advancement unless the apply output confirms it. On success report the accepted or updated entities (IDs or changed ledger paths), the accepted cursor range, whether the cursor advanced, and whether more evidence remains.
