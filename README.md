# SessionReviewer

SessionReviewer turns local Codex session history into a concise, editable project review. It helps you recover a project's goal, current stage, next action, risks, key decisions, evolution timeline, model usage, and cost without copying raw session logs into your notes.

[Read the Chinese documentation](./README.zh-CN.md)

## What it does

- Scans every Codex session associated with the project, then deterministically reduces the per-session results into one project view without an Agent call or token usage.
- Keeps the SessionReviewer skill as an optional semantic refinement path; it is not required for the normal whole-project scan.
- Stores human-readable project context in `docs/session-review/项目回顾.md` and `docs/session-review/项目历史.md`.
- Synchronizes editable project notes with a configured vault using a deterministic three-way merge.
- Provides a desktop project browser with evolution, decisions, risks, usage, pricing sources, and safe editing controls.
- Keeps machine-owned evidence, accounting, revisions, and synchronization metadata out of the human recovery view.

Raw session files stay on your computer. SessionReviewer does not modify the original JSONL files and does not require a separate OpenAI API key.

## Requirements

- macOS 13 or later on Intel or Apple Silicon
- Windows 10 22H2 or Windows 11 on x64
- Obsidian 1.8.7 or later for the desktop plugin
- Go 1.26 only when building the CLI from source

The desktop plugin is marked desktop-only because it invokes the local SessionReviewer CLI for validated status, synchronization, migration preview, and recovery actions.

## Install the desktop plugin

Download `main.js`, `manifest.json`, and `styles.css` from the [latest GitHub Release](https://github.com/NeoMei/SessionReviewer/releases/latest). Place the three files in:

```text
<Vault>/.obsidian/plugins/session-reviewer/
```

Enable **SessionReviewer** under **Settings → Community plugins**, then run **SessionReviewer: Open project evolution** from the command palette or select the history icon in the left ribbon.

No executable settings are required. On startup, the plugin discovers and verifies SessionReviewer from normal user installation locations and `PATH`; legacy saved paths are accepted once for migration and then removed. If discovery fails, install or run SessionReviewer once and reload the plugin. The plugin only executes fixed, allow-listed CLI actions; it does not read executable paths or arbitrary arguments from Markdown.

## Initialize and synchronize a project

Create a stable project mapping and review files:

```bash
session-reviewer init --project /path/to/project --vault /path/to/vault
session-reviewer init --project /path/to/project --vault /path/to/vault --write
```

Preview synchronization without writing:

```bash
cd /path/to/project
session-reviewer sync --dry-run
session-reviewer sync status --json
```

Apply a real project-to-vault synchronization:

```bash
session-reviewer sync
```

The first command previews initialization. Adding `--write` creates the stable mapping and review files. Synchronization uses per-entity base snapshots so independent edits can merge while same-field conflicts remain explicit.

## Update the whole project with zero tokens

Run a complete foreground scan from the initialized project:

```bash
session-reviewer scan --json
```

The command discovers all Codex sessions associated with the project, updates deterministic per-session memory, reduces it into a project-wide view, preserves accepted human edits and unknown Markdown sections, and synchronizes the concise projection to Obsidian. It never sends session content to an Agent and reports `review_run_tokens: 0`.

The Obsidian action **更新项目脉络** starts the same work as a durable background job. The equivalent CLI controls are:

```bash
session-reviewer scan start --json
session-reviewer scan status --json
```

Terminal states distinguish a complete scan (`completed`), a complete scan with isolated source issues (`completed_with_issues`), and a failed scan (`failed`). Starting the same project again while its worker is queued or running returns the existing job instead of launching a duplicate.

## Optional Agent-assisted review

Prepare a bounded evidence packet:

```bash
session-reviewer prepare checkpoint \
  --sessions-root "$HOME/.codex/sessions" \
  --output ./evidence.json
```

The SessionReviewer skill converts the packet into a semantic proposal. Apply the validated proposal locally:

```bash
session-reviewer apply \
  --proposal /path/to/proposal.json \
  --evidence /path/to/evidence.json \
  --project /path/to/project
```

An accepted apply updates the machine ledger and the two human-readable project files before advancing the accepted cursor. Reapplying the same accepted proposal is idempotent.

The manual Skill workflow prepares bounded evidence and accepts a validated semantic proposal. It remains available when deterministic project context needs human-requested interpretation or enrichment, but the Obsidian plugin does not invoke an Agent for its normal update action.

The legacy Agent-orchestrated job remains covered by its compatibility suite:

```bash
go test ./test/reviewjob -count=1
```

The suite covers its multi-session happy path, failure and retry, cancellation, and kill-based restart recovery, and skips automatically on unsupported platforms.

## Build and verify

```bash
go test ./...
go vet ./...

cd obsidian-plugin
npm ci
npm run check
```

Release assets are reproducible and accompanied by `SHA256SUMS`. The GitHub Actions release workflow publishes the three standalone plugin files required by the Community directory, the plugin ZIP, and CLI archives for macOS and Windows.

## Privacy and safety

SessionReviewer keeps raw session logs local, stores bounded redacted observations instead of duplicating the raw transcripts, binds optional proposals to evidence digests and accepted revisions, and fails closed on modified machine ledgers or unresolved synchronization conflicts. Human-editable fields and unknown custom Markdown remain the highest presentation authority; machine-owned accounting, evidence, and generated sections remain separate.

## License

Apache License 2.0. Copyright 2026 NeoMei and QUUKK.
