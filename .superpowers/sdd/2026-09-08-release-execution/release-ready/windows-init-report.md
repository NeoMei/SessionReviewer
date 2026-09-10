# Windows current Markdown initialization repair

Date: 2026-09-10. Scope: `internal/project/init.go` and `internal/project/init_test.go`; no push or tag performed.

## Root cause

The Windows release log `tag-045-windows.log`, lines 526–553, shows current Markdown initialization rejecting ordinary `docs/session-review/项目回顾.md` before reaching validation/reuse. `readCurrentMarkdownProjection` converted the canonical slash-relative projection path with `filepath.FromSlash` before calling `pathguard.ReadStableRegularRootFile`. On Windows that produces backslashes. The reader deliberately rejects backslashes in `cleanTreeRelative`, so the call fails with `regular file is redirected, invalid, or exceeds read limit` even for a valid regular file. This also prevents callers such as preparation and CLI initialization from reusing accepted or pending-draft projection state.

## Change

Pass the existing canonical slash-relative path directly to the stable reader, matching the other initialization journal/file reader call sites. A short comment documents the cross-platform contract. The added `TestReadCurrentMarkdownProjectionPreservesNestedSlashPaths` reads all four projection files, including nested Unicode Markdown filenames and a pending human draft, verifies project identity and exact bytes, and confirms that the read leaves public files unchanged.

No pathguard, atomicfile, ACL, hardlink, symlink, reparse-point, pinned-root identity, byte-limit, or double-read protection was changed. No user Markdown or historical evidence was edited.

## Verification

- Existing native Windows RED evidence: preserved `tag-045-windows.log` current initialization failures quoted above.
- New regression RED: before editing production code, a temporary Go `-overlay` replaced only `filepath.FromSlash(relative)` at the faulty call with the equivalent Windows slash-to-backslash transformation. Running `go test -overlay <temporary-overlay.json> ./internal/project -run '^TestReadCurrentMarkdownProjectionPreservesNestedSlashPaths$' -count=1` failed with the exact logged error. This is a targeted platform-format emulation on macOS, not native Windows execution.
- GREEN: `go test ./internal/project -run '^(TestReadCurrentMarkdownProjectionPreservesNestedSlashPaths|TestCurrentMarkdownInitialize|TestInitializeRecoversInterruptionAfterEachReviewV2File|TestInitializePartialReviewV2NeverOverwritesForeignBytes)' -count=1` passed, 1.887 seconds. This includes accepted/pending reuse without mutation, read-only reuse, redirected projection/private state refusal, concurrent state change detection, malformed state rejection, and journal interruption/foreign-byte recovery checks.
- Security regression: `go test ./internal/pathguard -run 'Test(TreeReadRegular|ReadRegular|ReadStableRegular|TreeReject|TreeRelative)' -count=1` passed, 0.477 seconds, including stable-file in-place mutation, redirected parents/files, read limit, and namespace identity checks.
- Build: `GOOS=windows GOARCH=amd64 go test -c -o /tmp/sessionreviewer-windows-init-project.test.exe ./internal/project` passed. This establishes Windows compilation only.

## Remaining gate

Run the focused regression and full release tests on native Windows CI after controller integration. Local macOS test success and cross-compilation do not establish native Windows runtime acceptance, complete release CI, or publication. Other agents own unrelated Windows fixture and CI-timeout changes in the same worktree.
