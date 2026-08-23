---
name: session-reviewer
description: Review or checkpoint bounded SessionReviewer evidence into an accepted Markdown ledger, or resume from accepted ledger state. Use for SessionReviewer project-continuity workflows, not generic code review or raw Codex-log analysis.
---

# SessionReviewer

Use the installed `session-reviewer` binary and the wrappers in `scripts/`. Classify the request:

- **review**: inspect bounded evidence, optionally from the session start.
- **checkpoint**: accept only evidence after the accepted cursor.
- **resume**: first run `session-reviewer resume --ledger-only --project <project>`. This view is accepted state only. If the user also wants pending work incorporated, continue as review; otherwise stop after reporting the ledger view.

## Safety boundaries

- Never edit ledger files directly. Apply a proposal only with `scripts/apply-proposal.sh` or `scripts/apply-proposal.ps1`.
- Never read raw JSONL. Read one bounded packet and only the accepted ledger entities needed from `docs/session-review/`.
- Never interpret hidden reasoning, system or developer instructions, or opaque/encrypted compaction as evidence.
- Never run Git mutation commands. Do not add, commit, push, reset, checkout, switch, restore, branch, tag, stash, merge, or rebase.
- Never call an API client. The semantic proposal is produced locally from the bounded packet.
- A ledger-only view does not process pending sessions; never claim that it does.

## Accept one packet

1. Create a private temporary directory outside the project and record the exact packet and proposal paths. Do not use a broad or inferred cleanup target.
2. For review or checkpoint, run `scripts/prepare-workflow.sh` on POSIX or `scripts/prepare-workflow.ps1` in PowerShell once. This produces one bounded packet. Use `--from-start` only for review when the user asks to rebuild from the start.
3. Read that packet plus the accepted ledger entities needed to establish current IDs, revisions, and valid transitions. Do not inspect unrelated files or session sources.
4. Before synthesizing, read [references/proposal-v1.schema.json](references/proposal-v1.schema.json). Emit exactly one proposal JSON object conforming to that schema:
   - copy `project_id`, `session_id`, `from_cursor`, and `to_cursor` from the packet;
   - compute `evidence_packet_sha256` as `sha256:` plus the SHA-256 of the packet's compact JSON bytes (the prepared file without its single trailing LF);
   - preserve the packet's `expected_cursor` and `next_cursor` as the acceptance boundary even though they are not proposal fields;
   - cite only exact packet evidence tuples: evidence ID, session ID, JSONL line, source hash, and a faithful summary;
   - include every required top-level field, using empty arrays where appropriate; use accepted entity revisions for patches; never upgrade an inference to verified without verification evidence.
5. Apply only through `scripts/apply-proposal.sh <proposal> <packet> [flags]` on POSIX or `scripts/apply-proposal.ps1 <proposal> <packet> [flags]` in PowerShell. Treat validation, rendering, receipt, write, or compare-and-swap failure as rejection of the whole proposal.
6. Delete only the explicit packet and proposal temporary files, then the known empty temporary directory, after successful acceptance. On failure, stop and report the retained diagnostic paths.

If `has_more` is true, do not prepare another packet until the apply succeeds and the accepted cursor compare-and-swap completes. After `cursor_advanced: true`, repeat prepare, synthesize, and apply for one new bounded packet. If apply reports `already_applied: true`, re-run prepare so its `expected_cursor` proves the accepted boundary before continuing. Stop when `has_more` is false.

Stop on any failure. Do not claim acceptance, changed entities, or cursor advancement unless the apply output confirms it. On success report the accepted or updated entities (IDs or changed ledger paths), the accepted cursor range, whether the cursor advanced, and whether more evidence remains.
