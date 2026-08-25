# Human-readable ledger navigation acceptance

This record separates locally observed evidence from public-release evidence. It contains no private session text or absolute Vault path.

## Acceptance contract

- Project and Vault `project-overview.md` bytes converge after a clean explicit sync.
- The homepage starts with the fixed five-node Mermaid recovery path and links to current state, timeline, full evolution diagram, and all three indexes.
- Decision, open-loop, and Session indexes link to the real current filenames, including renamed entity files.
- Generated `项目导航`, `快速理解`, accounting, indexes, and diagrams are restored without changing semantic revision.
- Ordinary semantic edits still use the Base/Project/Vault merge and are followed by canonical derived publication.
- Session and project records show duration, tokens, list-price cost per million tokens, totals, and per-model shares.
- A repeated apply or sync has no writes or operations.

## Repeatable commands

```bash
go test ./internal/ledger ./internal/syncdoc ./internal/sync ./internal/cli -count=2
go test ./... -count=1
go test -race ./... -count=1
go test -shuffle=on ./... -count=2
go vet ./...
go mod tidy -diff
git diff --check
go build ./cmd/session-reviewer
GOOS=darwin GOARCH=amd64 go build -o /tmp/session-reviewer-darwin-amd64 ./cmd/session-reviewer
GOOS=darwin GOARCH=arm64 go build -o /tmp/session-reviewer-darwin-arm64 ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o /tmp/session-reviewer-windows-amd64.exe ./cmd/session-reviewer
```

For a configured project, use read-only checks first:

```bash
session-reviewer sync status --json
session-reviewer sync --dry-run
```

Run `session-reviewer sync` only when the dry-run has no conflict, malformed source, blocked entity, or entity error. Verify byte identity with `shasum -a 256` on the Project and Vault copies of the homepage, diagram, and indexes.

## Evidence table

| Surface | Expected evidence | Current result |
|---|---|---|
| Renderer and merge packages | Focused suites pass twice | PASS on 2026-08-25: `go test ./internal/ledger ./internal/syncdoc ./internal/sync ./internal/cli -count=2` |
| Full Go suite and vet | Fresh pass | PASS on 2026-08-25: `go test ./... -count=1`; `go vet ./...`; `go mod tidy -diff`; `git diff --check` |
| Race and order independence | Full race pass and two shuffled repetitions | PASS on 2026-08-25: `go test -race ./... -count=1`; `go test -shuffle=on ./... -count=2` |
| macOS native | Current host build and isolated E2E | PASS on Darwin arm64 with Go 1.26.5; round-trip, generated-only repair, revision, rename, concurrency, interruption, and repeat-no-op tests pass |
| macOS Intel x64 | Cross-build plus SHA-256 | PASS: `0b15285e31bd8834f7a53e51157886d3226a118c3e16315ab85591c760a3588a` |
| macOS Apple Silicon arm64 | Cross-build plus SHA-256 | PASS: `8d60c71c4f877a9725e128f34246a07d0caedc0cc6a392b82a65444ba4cc3e0b` |
| Windows x64 | Cross-build plus SHA-256; native UI remains CI evidence | Cross-build PASS: `423740481356fdc98577db738d15cfa375754d8680ddc8775ea13ba2e21f60e0`; CLI and Skill test executables also cross-compiled; native Windows UI was not available in this local acceptance |
| Real configured Obsidian Vault | Safe dry-run, converged hashes, visible Mermaid and links | PASS: relative path `Projects/SessionReviewer--269b8cab/Session Review`; 18 Markdown files byte-identical; `derived=current files=16`; repeat dry-run has zero operations/conflicts/issues/errors; homepage `当前状态` link opened the real detail document |
| GitHub Mermaid | Public repository page visibly renders Mermaid and relative links | Not run; requires separately authorized push and public inspection |
| GitHub Release v0.1.0 | Public tag, downloadable three-platform archives, checksum file, successful release workflow | PASS: tag `v0.1.0` targets `9c2397f`; Release workflow `32800187430` succeeded; all four assets were downloaded again and verified |

## Multi-pass review findings

Seven worthwhile defects were reproduced with regression tests and fixed during the final review, integration, and release gates:

- malformed or blocked Base entities are no longer counted as `in_sync` by `sync status`;
- a successful `sync resolve` releases its lock, runs a full reconciliation, and refreshes generated navigation in the same command;
- a partial post-resolution reconciliation now emits `E_SYNC_PARTIAL` and returns a nonzero CLI exit code without leaking sensitive content;
- generated Markdown navigation text now escapes raw HTML and Markdown control characters before Obsidian or GitHub renders it.
- evidence summaries ending in a source newline no longer create trailing whitespace in generated Markdown;
- a dry-run containing an unaccepted decision, open-loop, or Session semantic edit now defers derived planning instead of failing against the previous merge Base.
- the release gate no longer relies on a machine-load-sensitive wall-clock assertion for post-overflow rendering; it uses a deterministic output-equivalence assertion instead.

After these fixes, the repeated full race, shuffled-order, cross-build, package reproducibility, real Vault, and Obsidian UI gates found no additional worthwhile defect.

## Independent repeat audit

The complete audit was repeated independently on 2026-08-25. The final staged-diff gate exposed the two additional defects listed above; both were fixed with red-green regression tests before integration:

- the full suite, full race suite, two shuffled repetitions, `vet`, module-tidiness check, and patch check passed again;
- the sync, CLI, apply, atomic-write, and pathguard packages passed three additional race-detector repetitions;
- Darwin arm64, Darwin amd64, Windows amd64, Windows CLI tests, and Windows sync tests cross-compiled successfully;
- two fresh release-package runs were byte-identical and all archive checksums, wrappers, proposal-schema copies, and Skill validation passed;
- the real Project/Vault trees remained byte-identical with `in_sync=14`, zero pending/conflict/malformed/blocked entities, and `derived=current files=16`;
- Obsidian 1.13.7 again rendered the five-node homepage and full evolution diagram, and the homepage link opened the real diagram document.

No further defect was found in an enabled product path after those fixes. The repository still contains queue and event-gate foundations for the explicitly deferred watcher/history milestone; `Engine.DrainQueue` is not used by the current CLI and is not counted as implemented watcher functionality.

## Isolated Project/Vault UI checks

The isolated E2E must record the following without private content:

- relative Vault review path;
- before/after SHA-256 hashes and derived-file count;
- five Mermaid labels and four directed links;
- link resolution for the three indexes and at least one renamed detail document;
- a semantic Vault edit with exactly one revision increment;
- a generated-only Vault edit restored with no revision increment;
- repeat sync: `conflicted=0`, `malformed=0`, `blocked=0`, no pending semantic operation, and `derived=current`.

## Visual evidence locations

Project homepage: inspected through the byte-identical Obsidian copy and direct Markdown/link checks; no durable screenshot committed.

Obsidian homepage and detail view: PASS in Obsidian 1.13.7 on 2026-08-25. The fixed five-node Mermaid graph, Chinese navigation, accounting totals, six quick links, and entity `快速理解` were visible. The homepage `当前状态` link navigated to the real synced detail page. Mermaid trust was enabled with prior user authorization. Computer Use captures were temporary and were not committed because they can expose local UI context.

GitHub Mermaid screenshot: not run because no push is authorized by this acceptance task.

## Artifact hashes

Public archive values below are the authoritative GitHub Release asset digests downloaded after publication. Standalone binary values are retained as local pre-release cross-build evidence; standalone binaries are not separate Release assets.

| Artifact | SHA-256 |
|---|---|
| Native/Apple Silicon macOS binary | `8d60c71c4f877a9725e128f34246a07d0caedc0cc6a392b82a65444ba4cc3e0b` |
| Intel macOS binary | `0b15285e31bd8834f7a53e51157886d3226a118c3e16315ab85591c760a3588a` |
| Windows x64 binary | `423740481356fdc98577db738d15cfa375754d8680ddc8775ea13ba2e21f60e0` |
| Public v0.1.0 macOS Intel archive | `764dc57ccda13592a508c86a2e6c12a1cbb14b126af848af1cec33b61d66adc9` |
| Public v0.1.0 macOS Apple Silicon archive | `b7c5cc4efc4458f4c41dce36df6ef25c3e986f582285f28d77e60030c8707bfb` |
| Public v0.1.0 Windows x64 archive | `f2525a729d28ec770db8d8ca10225a96a7679629737de34f05f9c26cebc02d6c` |
| Public v0.1.0 SHA256SUMS | `4c3e481dd24846192cadcb81dc5968aa09f33afe748ffe4f409233d11e2c797c` |
| Project and Vault `project-overview.md` | `09b4c12b0262b0892eb5dcf7defb281256ba88ce51a12cdbbcd0fb2aaac47287` |
| Project and Vault `diagrams/project-evolution.md` | `42f6a3b49040da2ed9d3aabe9af28411aebe01271e4fa2fb38e0c2ea6ebb9811` |

The real configured status result was `in_sync=14`, `conflicted=0`, `malformed=0`, `blocked=0`, no pending entities, `derived_state=current`, and `derived_files=16`. A full recursive comparison of the Project and Vault review trees produced no differences. Public `v0.1.0` targets `9c2397f`; main CI run `32799794297`, Release run `32800187430`, and the rerun-completed tag CI run `32800187442` are green. The published Release is neither a draft nor a prerelease and exposes the three platform archives plus `SHA256SUMS`.
