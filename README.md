# SessionReviewer

SessionReviewer turns local Codex session history into a concise, editable project review. It helps you recover a project's goal, current stage, next action, risks, key decisions, evolution timeline, model usage, and cost without copying raw session logs into your notes.

[Read the Chinese documentation](./README.zh-CN.md)

## What it does

- Streams local Codex session JSONL through a bounded, allow-listed, and redacted evidence pipeline.
- Uses the SessionReviewer skill for semantic review while keeping deterministic parsing, validation, cursors, and writes in the local CLI.
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

Download `main.js`, `manifest.json`, and `styles.css` from [GitHub Release 0.2.5](https://github.com/NeoMei/SessionReviewer/releases/tag/0.2.5). Place the three files in:

```text
<Vault>/.obsidian/plugins/session-reviewer/
```

Enable **SessionReviewer** under **Settings → Community plugins**, then run **SessionReviewer: Open project evolution** from the command palette or select the history icon in the left ribbon.

In **Settings → SessionReviewer CLI**, enter the absolute path to a compatible `session-reviewer` executable and select **Verify and save**. The plugin only executes fixed, allow-listed CLI actions; it does not read executable paths or arbitrary arguments from Markdown.

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

## Review new session evidence

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

SessionReviewer keeps raw session logs local, limits persisted evidence by role and size, applies post-truncation redaction, binds proposals to evidence digests and accepted revisions, and fails closed on modified machine ledgers or unresolved synchronization conflicts. Human-editable fields and machine-owned accounting or evidence fields remain separate.

## License

Apache License 2.0. Copyright 2026 NeoMei and QUUKK.
