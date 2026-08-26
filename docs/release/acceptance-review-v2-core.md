# Review v2 core acceptance

Date: 2026-08-26

Scope: Task 8 core CLI, migration, machine-ledger publication/repair, Project ↔ Vault semantic merge, conflict resolution, and release packaging. This record intentionally uses logical target labels and counts; it contains no private absolute path, raw session text, credential, or high-entropy candidate.

## Repository replay gate

The sanitized eight-step fixture contract is committed at `testdata/acceptance/review-v2-core-manifest.json`. Replay the full generated-fixture chain, including all three isolated conflict actions, with:

```sh
go test ./internal/cli -run '^TestReviewV2CoreAcceptanceReplay$' -count=1 -v
```

The test creates all Project, Vault, data, manual-merge, and backup roots under Go test temporary directories. Before migration it captures an independent, exact inventory of the authoritative legacy files from the Project backup. The generated migration manifest must contain that exact safe, portable, unique path set and match every original byte sequence, SHA-256, size, and mode before its object/archive copies are checked. Adversarial replay tests reject omission, duplicate identity, unsafe backup paths, and snapshot-hash mismatch. Each conflict-action backup is byte-verified before use, restored after resolution, and byte-verified again. The replay's only durable output is the content-free result `8/8 steps passed; 3/3 isolated conflict actions passed`; it emits no generated absolute path or fixture content.

## External mapping gate

The configured real mapping was resolved read-only by stable project ID. The mapped Project was ahead of its remote and contained unrelated uncommitted code and accepted-ledger edits. Its legacy ledger also contains an older entity identity that the v2 stable-ID contract rejects. Read-only `sync status --json` failed closed and created no migration backup. No real Project or Vault write was attempted.

The eight-step write acceptance therefore used a realistic isolated mapping created by the legacy CLI and populated through the legacy ledger renderer. Exact Project, Vault, data, and backup targets were recorded in the private acceptance workspace. Pre-write Project, Vault, and data backups remain retained and were not deleted.

## Eight-step result

1. **Backup before writes:** Project, Vault, and machine data were copied to separate read-only-by-convention backup handles before v2 migration. Each later conflict-action repetition also received its own pre-action Project/Vault/data backup.
2. **Migration dry-run:** `sync --dry-run --project-id <fixture-project-id>` exited 0 and reported `migration=required`, zero operations/conflicts/issues/errors, and `machine=pending`. Complete Project, Vault, and data snapshots before and after were identical.
3. **Real migration and sync:** the real command exited 0, published both human documents to Vault, ended with `migration=current` and `machine=current files=1`, and wrote no stderr.
4. **Visible document boundary:** the normal Project and Vault review directories each contained exactly `项目回顾.md` and `项目历史.md`. Machine data and conflict state remained hidden.
5. **Backup manifest:** Project contained one content-addressed migration manifest covering the exact six-file authoritative legacy inventory captured independently before migration. Every manifest path was safe, portable, and unique; source bytes/hash/size/mode matched that pre-write inventory before every object and archive copy was checked (`6` checked, `0` mismatches). Vault contained no migration backup.
6. **Convergence:** JSON status reported `in_sync=2`, `conflicted=0`, `malformed=0`, `queued=0`, `blocked=0`, `migration=current`, `machine_state=current`, zero pending operations, and zero hidden conflicts. Repeat status, dry-run, and real sync all produced empty stderr; both sync commands reported zero operations. Project/Vault bytes matched for both Markdown documents and `ledger.json` (`0` mismatches), and all six file modification times remained unchanged across the repeated read/sync sequence.
7. **Different semantic units:** a Project-side goal edit and Vault-side next-action edit merged in one sync. Both values appeared on both sides, both human documents aligned at revision `2`, status returned to zero pending/conflict state, and the machine ledger was current.
8. **Same semantic unit and all actions:** three isolated mappings changed the same goal unit to different Project/Vault values. Each produced one hidden `conflict-project-overview-*` record and no visible conflict note. `accept_project`, `accept_obsidian`, and `manual_merge` each exited 0 in its own repetition, produced byte-identical Project/Vault documents with the selected result, and returned to zero hidden conflicts and zero pending operations. Each repetition then restored its independent Project/Vault/data backup and proved the restored bytes/modes and clean status exactly matched the pre-conflict snapshot; the resolved tree was retained separately for inspection.

## Acceptance-found defect and regression

The first isolated conflict repetitions exposed a recoverability defect: when Project and Vault already held identical v2 bytes but a fresh machine-data directory had no merge Base, sync reported current without establishing Base. A later same-unit conflict was persisted but could not be resolved.

The fix makes a real, byte-identical first sync authenticate both preimages and commit their shared bytes as Base without rewriting either human document. In a final fresh-data repetition, status exposed two `establish_base` plus two machine-ledger operations and `machine_state=pending`; dry-run reported the same two Base operations and `machine=pending`. Complete Project/Vault/data size-and-modification-time snapshots remained identical across status and dry-run. The subsequent real sync reported the same semantic operation plan with `machine=current`, and its repeat reported zero operations. Machine-ledger operations intentionally omit the commit-time `after_hash`: status, dry-run, and real sync remain identical as the clock advances, while the transaction still writes and verifies exact ledger bytes and hashes and stamps `last_successful_sync` with the real commit time. Regression test `TestFirstSyncOfIdenticalV2CopiesEstablishesResolvableMergeBases` advances the clock between status, dry-run, real sync, and repeat; compares the complete public plans; verifies the committed timestamp; proves both Bases are committed only by real sync; and proves repeat sync does not churn machine bytes. The three action repetitions above were recreated from fresh backups after rebuilding the CLI with that fix.

## Remaining external gate

Two read-only blockers remain on the configured mapping: the Project branch is ahead of its tracked remote and has unrelated uncommitted code/accepted-ledger edits, and one pre-v2 open-loop identity lacks the stable-ID suffix required by the migration validator. The latter currently makes `sync status --json` fail closed before a migration plan can be trusted.

The safe unblock sequence is owner-directed: first preserve or finish the unrelated worktree changes and verify that `git status --short --branch` reflects the intended state; then update the invalid legacy entity through an explicit, reviewed legacy-ledger migration rather than renaming a live file ad hoc. Re-run read-only legacy loading/status after that change. Only when both checks pass, record the exact configured Project/Vault targets privately, create and retain fresh full backups of both review trees, snapshot Project/Vault/data, and repeat the eight commands and hash comparisons above. Do not reuse the copied-fixture result as proof for those real targets.

This external gate is not release evidence for the real user data; the isolated acceptance is the core implementation gate for the later plugin task. No real Project, Vault, Git state, or invalid legacy entity was changed during this acceptance.

Native PowerShell wrapper execution remains an external CI gate because `pwsh` is unavailable on this macOS host. The Windows workflow runs the wrapper twice and compares all archives, checksums, and the packaged schema; the local macOS shell wrapper gate is independently executable on this host.
