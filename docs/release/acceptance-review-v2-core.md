# Review v2 core acceptance

Date: 2026-08-27

Scope: Task 8 core CLI, migration, machine-ledger publication/repair, Project ↔ Vault semantic merge, conflict resolution, and release packaging. This record intentionally uses logical target labels and counts; it contains no private absolute path, raw session text, credential, or high-entropy candidate.

## Gate A zero-token core acceptance — 2026-09-01

Status: **locally accepted as a private prepared-generation core**. This is not a public command, Project/Vault publication, Obsidian integration, GitHub release, or marketplace release. Gates B and C remain required before public release.

The sanitized replay at `test/zerotoken` composes the production Codex discovery/freeze/decode adapter, SourceCatalog, immutable memory store, SessionView materializer, production ProjectProbe, ProjectView reducer, and scan orchestrator. Codex production decoding derives unsupported, malformed/unreadable, and valid cross-project ambiguous terminal classes from the frozen JSONL evidence; the gate does not decorate or mutate decode reports. A real duplicate first-seen Session issue and the separate foreign-project source are discovered through production discovery and correctly excluded from the target count.

The initial result is exactly 154/154 terminal Sessions: 151 indexed, one unsupported, one malformed/unreadable, and one shared cross-project ambiguous. The same replay then verifies a real append preserving old chunks and writing only one successor chunk/view, an unchanged no-write replay, adapter-v1-to-v2 supersession, source truncation/replacement with withdrawal, missing/unavailable replay retaining prior facts, and an unchanged missing replay. Every run reports `review_run_tokens=0`.

The replay initializes a temporary real Git repository, authenticates its root and common directory, and uses the production ProjectProbe to read branch, HEAD, dirty count, remote identity, version-file hash, and required-file hash. Cancellation and complete Project/Vault tree snapshots prove the probe and scan do not change paths, bytes, modes, sizes, or file/directory modification times. Captured project commands are decoded but never executed. A strict allowlist fixes the complete local production dependency closure; process execution is confined to ProjectProbe's approved read-only Git argv, and network/model/Agent entry points are absent. Complete user text/tool output canaries do not enter the private store, and the only safe verbose test log is:

```text
Gate A: 154/154 terminal, 151 indexed, zero model tokens
```

Retention acceptance accounts exact unique reachable objects/physical bytes from only the current prepared generation plus validated opaque external generation pins. Other fully validated immutable generations and objects are reported separately as unreachable-but-retained and are never cleanup targets. Reporting is read-only and preserves namespace, content, and file/directory modification times; atime is outside the portable contract. Automatic cleanup is restricted to canonical content-addressed `staging` and `cache` entries that are unreachable and at least seven days old. Hard-linked candidates count each authorized namespace unlink but physical bytes only once. Tests cover the exact seven-day boundary, corrupt graph/pin/namespace fail-closed behavior, permissions and redirect rejection, entry/mtime/size/namespace TOCTOU, concurrent initial prepare and prepared advance, caller pin mutation, and a real subprocess exit after one deletion followed by successful restart cleanup. Current/pinned graphs, all immutable lineage, diagnostics, journals, and locks are never cleanup targets.

Local verification completed successfully:

```sh
go test ./internal/memory ./internal/memorystore ./internal/projectidentity ./internal/sourcecatalog ./internal/source/... ./internal/sessionview ./internal/projectprobe ./internal/projectview ./internal/scan ./internal/cli ./test/zerotoken -count=1
go test -race ./internal/memorystore ./internal/sourcecatalog ./internal/scan -count=1
go vet ./...
task9_tmp_dir="$(mktemp -d)"
GOOS=windows GOARCH=amd64 go test -c -o "$task9_tmp_dir/memorystore.test.exe" ./internal/memorystore
GOOS=windows GOARCH=amd64 go test -c -o "$task9_tmp_dir/zerotoken.test.exe" ./test/zerotoken
git diff --check
```

## Repository replay gate

The sanitized eight-step fixture contract is committed at `testdata/acceptance/review-v2-core-manifest.json`. Replay the full generated-fixture chain, including all three isolated conflict actions, with:

```sh
go test ./internal/cli -run '^TestReviewV2CoreAcceptanceReplay$' -count=1 -v
```

The test creates all Project, Vault, data, manual-merge, and backup roots under Go test temporary directories. Before migration it captures an independent, exact inventory of the authoritative legacy files from the Project backup. The generated migration manifest must contain that exact safe, portable, unique path set and match every original byte sequence, SHA-256, size, and mode before its object/archive copies are checked. Adversarial replay tests reject omission, duplicate identity, unsafe backup paths, and snapshot-hash mismatch. Each conflict-action backup is byte-verified before use, restored after resolution, and byte-verified again. The replay's only durable output is the content-free result `8/8 steps passed; 3/3 isolated conflict actions passed`; it emits no generated absolute path or fixture content.

## External mapping gate

The configured real mapping was resolved by stable project ID and completed after fixing three migration-compatibility defects found by its accepted v1 state: proposal-v1 stable IDs containing dots or underscores, sparse legacy timeline events, and verbose session-wide verification copied into every human event. A fourth defect appeared during the first real write: Project migration continued against the legacy Vault and replayed v1 documents into the compact Project. That attempt was isolated intact, the independently archived Project/Vault/data state was restored, and regression tests were added before retrying.

The corrected path verifies that every legacy Vault Markdown file is either byte-identical to Project or still matches its authenticated merge Base while Project contains the newer accepted value. It retires those Vault copies before Project migration, resets v1 merge Bases before the stable compact IDs are reused, and fails without writes when Vault has an unmerged edit. Project, Vault, and machine-data backups from both the initial attempt and the final retry remain retained in the private acceptance workspace.

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

## Configured mapping completion

The final real dry-run exited 0 with three migration creates, 21 archived legacy Markdown files, zero entity operations, zero conflicts/issues/errors, and pending machine publication. The real sync then published `项目回顾.md` and `项目历史.md` to Vault, reported `migration=current`, `machine=current`, two entity operations, and zero conflicts/issues/errors.

The normal Project and Vault review directories now expose only `项目回顾.md` and `项目历史.md`; the machine ledger remains hidden. Project contains one content-addressed migration manifest with 21 unique safe paths. All 21 object copies and all 21 archive copies were independently rehashed against the manifest with zero mismatch. Vault contains no migration backup.

Final JSON status reports `in_sync=2`, `conflicted=0`, `malformed=0`, `queued=0`, `blocked=0`, `migration=current`, `machine_state=current`, no pending operations, and no hidden conflicts. A repeated dry-run reports zero operations/conflicts/issues/errors. Project and Vault bytes match for both Markdown documents and `ledger.json`.

The human view was also checked as a product boundary, not only as valid Markdown. The project title is the directory name rather than an internal ID, resolved legacy loops are omitted from the current review but retained in history and the hidden compatibility ledger, and each event carries only its own concise result instead of repeating the complete session verification list. The configured result is 150 lines for `项目回顾.md` and 164 lines for `项目历史.md`.

Native PowerShell wrapper execution remains an external CI gate because `pwsh` is unavailable on this macOS host. The Windows workflow runs the wrapper twice and compares all archives, checksums, and the packaged schema; the local macOS shell wrapper gate is independently executable on this host.
