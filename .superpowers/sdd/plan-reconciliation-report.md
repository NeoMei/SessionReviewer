# Remaining Plan Reconciliation Report

Date: 2026-08-23

Scope: documentation-only reconciliation before executing the sync, history/watcher, and release-hardening plans. No implementation or current Task 9 Skill file was changed.

## Executable order

1. Execute `docs/superpowers/plans/2026-08-23-session-reviewer-sync.md` Tasks 1-11.
2. Re-run the sync completion gate, then execute `docs/superpowers/plans/2026-08-23-session-reviewer-history-watcher.md` Tasks 1-8.
3. Re-run the history/watcher completion gate, then execute `docs/superpowers/plans/2026-08-23-session-reviewer-release-hardening.md` Tasks 1-9.

The history plan consumes the sync engine contracts. Release Task 3 establishes the manifest, source installer, and Skill verifier; Task 4 packages those inputs; Task 5 adds archive install/upgrade/rollback.

## Resolved blockers

| Blocker | Reconciled contract |
|---|---|
| Existing ledger document API would be overwritten | Sync uses a separate `internal/syncdoc` lossless compatibility layer; `internal/ledger/document.go` is explicitly unchanged and remains semantic validation. |
| Human/proposal ownership and revisions were ambiguous | Human edits exclude `revision`, `source_sessions`, `evidence`/nested `source_hash`, and `supersedes`; a successful semantic human merge increments Base revision exactly once, no-op retains it, and new/overview documents start at revision 1. |
| Sync/history config declarations diverged | Both plans use one version-1 union containing vault mapping, Git identities, aliases, and session associations, with round-trip preservation tests. |
| Physical deletion rule contradicted rename behavior | The sole exception is old-path cleanup after both target copies and new Base verify, followed by pinned identity/entity/hash revalidation of the old file. |
| Index privacy test was structural only | Tests allow stored SHA-256 hashes while scanning SQLite/WAL/SHM/quarantine bytes for forbidden narrative/title/evidence-summary/content canaries. |
| Index recovery was not cross-process/root safe | A stable per-index advisory lock covers inspection/write/quarantine/rebuild/swap; all moves and swaps revalidate pinned root/file identities. |
| Git allowlists used prefix matching | Both inspectors accept only exact argument vectors; `symbolic-ref` is never prefix-authorized. |
| Pending scanner cursor interface did not match storage | Scanner obtains one identity-pinned per-project cursor store, then calls existing `LoadReadOnly(sessionID)`. |
| Release build/install ordering was cyclic | Manifest/source install/Skill verification precede packaging; archive install/upgrade/rollback follow packaging. |
| Candidate and public release modes were conflated | Private mode requires a clean exact commit but no tag/license and succeeds with publish skipped; public mode requires exact `v0.1.0` tag/commit plus license authorization. |
| Windows section described a removed backup protocol | Plan characterizes the current rooted existing-destination replace rename and absent-destination hard-link publication, with no backup recovery protocol and no untested power-loss claim. |
| `TotalAlloc` was mislabeled as peak heap | The live-heap gate samples `runtime/metrics`; `TotalAlloc` delta is separately named `TotalAllocatedBytes` for churn diagnostics. |

## Execution guard

If an upstream signature differs when a later plan begins, reconcile the affected plan documents in one documentation commit before implementation. Do not silently adapt by overwriting accepted-ledger APIs, dropping union config fields, weakening rooted identity checks, or broadening command/release modes.
