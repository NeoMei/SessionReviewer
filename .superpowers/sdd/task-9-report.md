# Task 9 report: packaged semantic review workflows

## Scope and range

- Review range: `27d1ac1..HEAD`.
- Initial package commit: `099db5b` (`feat: package semantic review workflows`).
- Scope is limited to `skill/session-reviewer/` plus this Task 9 report. Task 10 was not implemented.

The package adds an automatically discoverable SessionReviewer skill, a byte-identical proposal schema reference, a progressive semantic-invariant reference, argument-preserving POSIX/PowerShell wrappers, and Go packaging/security/runtime tests.

## RED evidence

The initial Task 9 RED run was:

```text
go test ./skill/session-reviewer/tests -count=1
```

It failed because the skill package, schema copy, wrappers, and instructions did not exist. After the first implementation passed, fresh review produced five Important findings. New tests then failed on:

- missing first-packet-only `--from-start` and later-packet omission rules;
- missing pinned canonical project-root flow through resume, prepare, apply, and retries;
- missing progressive semantic-invariant reference;
- the stale report that described foundation `prepare.Run` instead of Task 9;
- PowerShell missing/non-Application-shadow/stale-exit behavior (runtime tests are conditional on PowerShell availability).

The forward-flow regression executes first prepare with `--from-start`, apply with the pinned root, then packet-two prepare without `--from-start` and apply with the same root. It also requires `already_applied: true` recovery to re-prepare once without `--from-start`, prove the accepted boundary, and stop if the boundary does not advance or the same packet repeats.

## GREEN implementation

- `SKILL.md` classifies review/checkpoint/resume, pins one absolute physical project root before any command, uses it as every prepare `--cwd` and resume/apply `--project`, and keeps it through resume-with-pending and `has_more` flows.
- `--from-start` is allowed only on the first explicitly requested review packet. All later packets and `already_applied` retries omit it, preventing packet-one replay loops.
- `references/apply-invariants.md` documents semantic rules not expressible in JSON Schema: exact packet evidence summaries, evidence on every changed entity, complete evidence-link coverage, revisions/status transitions, supersedes/reference constraints, current-state evidence/source sessions, inference verification, and exact sorted session-report effects.
- POSIX wrappers preserve all arguments and use `exec` so native exit codes propagate.
- PowerShell wrappers resolve only `CommandType Application`, invoke the resolved absolute path, capture `$LASTEXITCODE` immediately, return deterministic 127 for missing/non-Application commands and 126 for start failure, and avoid aliases/functions/external-script shadows.
- Tests cover schema byte equality, discovery/frontmatter, progressive references, forbidden wrapper capabilities, forward packet flow, arbitrary argument forwarding, exit status, wrapper arity/mode validation, PowerShell parsing/runtime when available, missing/shadow/stale status, and Task 9 report scope.

## Final verification

Commands:

```text
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -o <private-temp>/session-reviewer ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go build -o <private-temp>/session-reviewer.exe ./cmd/session-reviewer
GOOS=windows GOARCH=amd64 go test -c -o <private-temp>/package-tests.exe ./skill/session-reviewer/tests
go test ./skill/session-reviewer/tests -count=1
sh -n skill/session-reviewer/scripts/*.sh
cmp schemas/proposal-v1.schema.json skill/session-reviewer/references/proposal-v1.schema.json
PYTHONPATH=<private-temp>/pyyaml python3 /Users/neomei/.codex/skills/.system/skill-creator/scripts/quick_validate.py skill/session-reviewer
git diff --check
```

Result on 2026-08-23: every applicable command exited 0. `quick_validate.py` printed `Skill is valid!`; native output was Mach-O arm64; both Windows outputs were PE32+ x86-64. The focused verbose package run also passed all POSIX and static PowerShell checks and skipped the six PowerShell parser/runtime tests because no PowerShell executable is installed.

## Platform limits

- Native execution host: macOS arm64.
- POSIX wrapper syntax and runtime are exercised on macOS.
- PowerShell is not installed on this host, so PowerShell parser/runtime tests skip here; they are implemented and run automatically when `pwsh` or Windows PowerShell is available.
- Windows amd64 cross-build and packaging-test cross-compilation run on macOS. Neither executable is run on a Windows host in this task.
