# Task 4 Report: Previewable, Recoverable Legacy Migration

## Status

COMPLETE

- Base: `446423a0a5bb757e4643ac55b313f6a32d9182a2`
- Feature commit: `fdbff0a78d987d78fc59365fbc06b1220d1a64e7` (`feat: add atomic review v2 migration`)
- Independent-review fix commit: `691dcc0091ad24c2584eb0607920333ae2549064` (`fix: harden review v2 migration recovery`)
- Branch: `codex/session-reviewer-v2`
- `.superpowers/sdd/progress.md` was not modified.

## Scope and implementation

- Added a read-only `PlanMigration` preview, `ApplyMigration`, `RecoverMigration`, `MigrationPlan`, and `MigrationReport`.
- Added a private, content-free migration journal below the configured data root. It records only paths, hashes, sizes, modes, immutable project identity, manifest identity, time, and stage; it never stores Markdown or ledger content.
- Added a deterministic project-hidden backup manifest at `docs/session-review/.session-reviewer/backups/<manifest-sha256>/manifest.json`, private content-addressed objects, and a lossless `archive/` tree containing the moved legacy namespace.
- Added create-if-absent backup publication, strict manifest/object verification, private-mode checks, journal namespace collision checks, duplicate-field/exact-field JSON checks, and monotonic stage transitions.
- Added atomic v2 target publication with absent preimages, exact desired hashes/modes, recovery of partial v2 writes, and no-clobber stale diagnostics.
- Added rooted legacy inventory and archive validation with exact union convergence, portable Windows/case/NFC path keys, redirect/symlink/junction/reparse rejection, and the inherited 64 MiB/4096-file limits.
- Added fail-closed handling for mixed state without a matching journal, plan-after-edit/addition, v2 target and backup collisions, project/data root replacement, archive collision, corrupt/public backup state, and user edits at every interrupted recovery boundary.
- Added path-aware pruning of `.session-reviewer/backups` even when a scan starts inside `.session-reviewer`; exact rooted reads of `ledger.json` remain available.
- Added `docs/session-review/.session-reviewer/backups/` to `.gitignore`.

## Independent-review remediation

All 2 HIGH, 4 MEDIUM, and 1 LOW findings in `task-4-review.md` were reproduced with focused RED tests and closed in the independent fix commit.

- V2 files now use a rooted, durable hard-link no-replace publication primitive. An exact existing file is skipped; a concurrently-created or redirected target is never replaced. A deterministic pre-publication hook proves user bytes remain byte-for-byte.
- Legacy archive files now move via a source-identity-bound, no-replace hard link, durability sync, authenticated source retirement, and post-move identity/hash/mode verification. Destination collision and post-link source replacement roll back only the migration-owned link. Partial directory creation and a simulated process death after link publication converge on restart.
- The content-free journal now stores canonical project/data paths and restart-stable physical identities. POSIX uses device+inode; Windows uses volume serial+64-bit file ID from `GetFileInformationByHandle`. Both roots are reopened and checked before every stage or cleanup write.
- All three v2 targets are checked for exact hash, size, and mode at every recovery stage and before journal cleanup. `ledger.json` is `0600`; the two visible Markdown files remain `0644`.
- Existing machine-owned journal/backup/archive directories are re-opened, identity-stability checked, and required private without auto-chmod. Mode changes stop recovery before later writes.
- Backup verification now walks the entire rooted namespace with bounded enumeration, portable case/NFC keys, exact stage-appropriate expected entries, exact hashes/sizes/modes, and rejection of every unexpected root/object/archive entry.
- Journal decoding now requires all non-optional root and nested fields and rejects omitted, duplicated, aliased, unknown, truncated, empty, or cross-field-inconsistent state. Legacy/inventory are nonempty, the three writes are exact and distinct, and the manifest is recomputed from inventory.
- Backup pruning now compares a portable component-aware key, including caller-prefix and on-disk casing variants.

## RED evidence

1. Core migration API and crash recovery:
   - `go test ./internal/reviewv2 -run TestMigrationDryRunWritesNothingAndCrashRecoveryConverges -count=1`
   - Failed to compile with undefined `Stage`, `PlanMigration`, `applyMigrationWithHook`, and `RecoverMigration`.
2. Backup pruning:
   - `TestWalkMarkdownPrunesSessionReviewerBackupsButExactLedgerReadRemainsAvailable`
   - Failed because the ordinary walk visited `docs/session-review/.session-reviewer/backups/manifest/legacy.md`.
3. Journal backup collision:
   - `TestMigrationJournalIsContentFreeAndRejectsBackupCollision`
   - Failed because a primary journal was written beside an attacker-provided `.session-reviewer-backup` alias.
4. Recovery budget validation:
   - `TestMigrationJournalRejectsRecoveryInventoryBeyondLedgerBudgets`
   - Failed because a recovery journal with more than 4096 files was accepted.
5. Private backup verification:
   - `TestRecoverMigrationRejectsPublicBackupObjectBeforeV2Writes`
   - Failed because a mode-0644 content-addressed object was accepted.
6. Existing human directory permissions:
   - `TestMigrationDoesNotTightenExistingHumanDirectoryModes`
   - Failed because the first directory creator changed `docs/` from 0755 to 0700.
7. V2 preimage collision:
   - `TestMigrationRejectsChangesAfterPlanningBeforeAnyMigrationWrite/v2_target_collision`
   - Failed because backup/journal state was created before the collision was rejected.
8. Complete crash matrix:
   - Extending the core test to `StagePlanned` failed because the planned-stage hook was initially ignored.
9. Portable collision key:
   - `TestMigrationPortableInventoryKeyCollapsesCaseAndNFC`
   - Failed to compile because the migration-owned portable-key helper did not yet exist.
10. Atomic v2 publication race:
   - `TestMigrationV2PublicationNeverReplacesConcurrentUserFile`
   - Failed to compile before `migrationHooks`; the original check-then-replace writer could overwrite a publication injected in the final window.
11. Archive destination/source races:
   - `TestMigrationArchiveNeverReplacesConcurrentDestination` and `TestMigrationArchiveRollsBackWhenSourceReplacedAfterPublish`
   - Both initially completed with `nil`, proving the original rename ignored the deterministic collision/replacement windows.
12. Restart root replacement:
   - `TestRecoveryRejectsPhysicalRootReplacementAtEveryStage/project/planned`
   - Initially recovered successfully from a byte-identical replacement root because the journal had no physical identity.
13. V2 chmod-only mutation:
   - `TestMigrationAuthenticatesV2ModesAtEveryRecoveryStage/v2_written/ledger.json`
   - Initially found a `0644` hidden ledger; later stages also lacked mode authentication.
14. Machine directory privacy:
   - `TestRecoveryRejectsNonPrivateMachineDirectoriesWithoutRepair/data/migrations`
   - Initially recovered and wrote after the journal directory changed to `0755`.
15. Exact backup namespace:
   - `TestRecoveryRejectsUnexpectedBackupNamespaceEntries/case-0`
   - Initially committed with an unexpected backup-root file still present.
16. Exact required journal fields:
   - `TestMigrationJournalRequiresEveryRootAndNestedField/visible_inventory/size`
   - Initially accepted a nested required field omitted from JSON.
17. Portable backup pruning:
   - `TestBackupPruningUsesPortableComponentAwarePathKey`
   - Initially failed for uppercase caller prefix and on-disk `BACKUPS` spelling.
18. Intra-stage archive crash recovery:
   - `TestMigrationRecoveryConvergesAfterPartialArchiveDirectoryCreation` first failed to compile without its deterministic hook.
   - `TestMigrationRecoveryConvergesAfterCrashFollowingArchiveLink` initially returned `stale_migration` instead of retiring the authenticated same-inode alias and converging.

## GREEN evidence

- Core focused: `go test ./internal/reviewv2 -run TestMigrationDryRunWritesNothingAndCrashRecoveryConverges -count=1` passed.
- Task packages: `go test ./internal/atomicfile ./internal/pathguard ./internal/reviewv2 -count=1` passed after the review fix.
- Focused final race: `go test -race ./internal/atomicfile ./internal/pathguard ./internal/reviewv2 -count=1` passed after the final archive crash-recovery changes.
- Full repository: `go test ./... -count=1` passed across every package after the review fix.
- Full repository race: `go test -race ./... -count=1` passed across every package during review remediation; the final archive follow-up was then revalidated by the focused final race above.
- Static analysis: `go vet ./...` passed with zero output.
- Native build: `go build -o <private-temp>/session-reviewer ./cmd/session-reviewer` passed; output is Mach-O arm64.
- Windows main build: `GOOS=windows GOARCH=amd64 go build ... ./cmd/session-reviewer` passed; output is PE32+ x86-64.
- Windows Task 4 compilation: `GOOS=windows GOARCH=amd64 go test ... -exec=/usr/bin/true` passed for `internal/atomicfile`, `internal/pathguard`, and `internal/reviewv2`; `GOOS=windows GOARCH=amd64 go build ./cmd/session-reviewer` also passed.
- `git diff --check`, cached diff check, and post-commit `git status --short --branch` passed; the worktree is clean.

The local macOS filesystem collapses case-only and NFC-equivalent filenames, so the two physical collision subtests skip there. The migration-owned pure key regression passes for both pairs, and the scanner uses that exact helper; symlink and 64 MiB physical hostile cases pass locally.

## Crash-stage matrix

| Persisted stage | Durable state at interruption | Recovery action | Result |
| --- | --- | --- | --- |
| `planned` | Content-free journal only; legacy remains visible | Revalidate legacy/inventory, finish create-if-absent backup | Converges |
| `backup_complete` | Verified manifest/objects; legacy remains visible | Reproject exact legacy bytes, complete missing atomic v2 targets | Converges |
| `v2_written` | Exact v2 targets plus legacy visible state | Revalidate both sides, move remaining top-level legacy entries into archive | Converges |
| `legacy_moved` | V2 visible; complete hidden archive | Reparse v2, verify manifest/object/archive and exact ordinary inventory | Converges |
| `committed` | All invariants verified; committed journal remains | Reverify and remove only the authenticated journal | Converges |
| Partial v2 target set | Journal remains `backup_complete` | Accept exact written targets and atomically publish missing targets | Converges |
| Partial legacy archive | Journal remains `v2_written` | Validate active+archive exact union and move remaining top-level entries | Converges |
| Partial archive directories | Journal remains `v2_written`; archive directory subset exists | Authenticate allowed directory subset, create missing directories, move exact files | Converges |
| Crash after archive hard-link publication | Active and archive names bind the same expected physical file | Authenticate both aliases, durably retire only the active name, continue | Converges |
| Project/data root replaced at any persisted stage | Journal path is unchanged but physical identity differs | Reopen canonical root and compare journal identity before writes | `stale_migration`; replacement untouched |
| V2 or machine-directory chmod at any later stage | Hash/bytes may still match but exact mode differs | Fail before stage/cleanup mutation; never auto-chmod | Rejected; user mode retained |
| User edit after any interruption | Live hash/mode differs from journal | Return stable `stale_migration`; retain journal and user bytes | No overwrite |

## Self-review

- Planning is read-only: the project/data snapshot is byte-identical before and after `PlanMigration`.
- Root trust survives restart: the journal stores canonical path plus stable physical tokens for project and data roots, and recovery reopens both namespaces before every write boundary. Darwin system aliases use the canonical opened path; Windows tokens use the native volume/file ID rather than pathname metadata.
- No broad path mutation is used. Every read, create, atomic no-replace publication, link, sync, and authenticated cleanup stays below an opened root and rejects redirects/special entries.
- Existing `docs/` and `docs/session-review/` modes are preserved. Only machine-owned directories are required to be private.
- Recovery never regenerates from partial legacy state. Before `v2_written`, it requires the complete exact legacy snapshot; afterward, it trusts only journal hashes plus verified v2/backup/archive state.
- Backup roots and files are never automatically pruned or deleted. Only the exact authenticated committed data-root journal is removed.
- Ordinary Markdown inventory contains exactly `项目回顾.md` and `项目历史.md`; hidden `ledger.json` remains exact-readable and backups remain outside ordinary scan/Git/Vault paths.
- The independent fix commit contains only the reviewed Task 4 migration, minimal `atomicfile`/`pathguard` primitives, v2 ledger privacy, and focused tests. `.superpowers/sdd/progress.md` was not modified.

## Final re-review remediation (2026-08-26)

The two remaining findings in the independent re-review are closed by the final fix commit.

- Archive retirement and rollback no longer perform identity-check-then-unlink. A rooted atomic no-replace rename moves the pathname occupying the final window into a deterministic quarantine. If it is the expected inode, the durable retirement alias is retained in the exact backup namespace; if it is a concurrent replacement, migration returns `stale_migration` and the replacement bytes remain recoverable at the reported deterministic quarantine path. Darwin uses `renameatx_np(RENAME_EXCL)`, Linux uses `renameat2(RENAME_NOREPLACE)`, and Windows uses `SetFileInformationByHandle(FileRenameInfoEx)` with replacement disabled. Both source and destination parents are pinned and durably synced.
- Deterministic hooks immediately before source retirement and rollback quarantine reproduce both final TOCTOU windows. A post-retirement crash hook proves restart convergence after the durable rename; the existing post-link crash test continues to prove same-inode alias convergence.
- Windows machine privacy is no longer accepted from `os.FileMode`. Newly created migration directories and unpublished temporary files receive a protected DACL containing only the current user and `SYSTEM`; journal, backup object/manifest/archive, quarantine, and hidden ledger paths are verified again at every existing recovery gate. A broadened or inherited DACL is rejected without repair or later writes. POSIX exact `0600`/`0700` and preserved legacy archive-mode contracts remain unchanged.
- `migration_privacy_windows_test.go` is a Windows-native integration test that starts with permissive inherited ACLs, proves protected creation, broadens the journal directory ACL, and proves recovery rejects it without changing the journal. The macOS host cannot execute this native runtime test; Windows compilation/linking is verified locally and native execution remains an explicit Windows CI/host gate.

Final RED evidence:

1. `TestMigrationArchiveRetirementNeverDeletesFinalWindowSourceReplacement` and `TestMigrationArchiveRollbackNeverDeletesFinalWindowDestinationReplacement` failed to compile before the new immediate hooks existed.
2. `TestMigrationRecoveryConvergesAfterCrashFollowingArchiveRetirement` failed to compile before the post-retirement crash boundary existed.
3. The Windows ACL integration source depends on the new platform privacy contract and is compiled locally for `windows/amd64`; native execution is not claimed on macOS.

Final verification is recorded from fresh commands in the handoff for this commit: focused tests and race, full repository tests and race, `go vet`, Darwin/Windows Task 4 compilation, Darwin/Windows main builds, and `git diff --check`.
