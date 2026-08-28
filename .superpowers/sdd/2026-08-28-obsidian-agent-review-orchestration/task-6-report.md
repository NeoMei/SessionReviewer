# Task 6 Report: Verified No-Tools Codex Adapter

Date: 2026-08-28

## Status

Complete. Task 6 and both mandatory Task 5 breaker prerequisites are implemented and committed on `codex/obsidian-agent-review`.

- Base: `d6cd63debab05fb4c3d8d936c203c6e43eda4740`
- Implementation head: `8e0e2759ba45ae71f4215058305f7aceee721bdf`
- Implementation commit: `feat: run Codex as a no-tools proposal worker`

## Mandatory Task 5 prerequisite closure

### A. Raw accepted-context scan before JSON encoding

- `reviewprompt.Build` now constructs the exact allowlisted accepted-context projection once and traverses every projected string before `json.Marshal`.
- The traversal includes current state, risks, decisions, open loops, timeline entries, session reports, every nested phase, and all allowlisted string slices.
- Invalid UTF-8 maps to `ErrInvalidInput`; any redaction finding maps to the exact sentinel `ErrUnsafeInput` and returns a zero `Bundle`.
- The field table uses the short named secret `password:"x"`, including a shared accepted/packet project-ID case that proves the raw accepted scan wins before envelope rejection. This closes the JSON-backslash escape bypass.

### B. Central duplicate-warning validation

- Added `evidence.ValidateWarnings`, which owns both element parsing and the evidence-v2 `uniqueItems` invariant.
- `reviewprompt` packet validation and final `proposal.Validate` both use the collection validator.
- Duplicate canonical redaction and structural warnings return a zero `Bundle` / zero `ledger.ChangeSet`; proposal tests recompute the packet digest first so digest mismatch cannot mask this check.

## Codex adapter implementation

### Verification and executable pinning

- Requires an absolute path, resolves symlinks to one absolute physical executable, and rejects non-regular or non-executable files.
- Records physical file identity plus SHA-256 content identity. It reopens and rechecks the path/inode/content before every probe, after all probes, and immediately before generation.
- Detects both rename replacement and same-inode content drift.
- Serializes `Verify`; a concurrent verification or active run returns `E_AGENT_BUSY`, and a failed re-verification leaves no stale capability configured.
- Accepts only canonical `codex-cli 0.147.<patch>` versions, equivalent to `>=0.147.0,<0.148.0`. Older, newer, prerelease, and noncanonical patch forms fail closed.
- Runs bounded `--version`, `exec --help`, `features list`, and `debug prompt-input` probes in a private temporary directory.
- The prompt-input probe must contain the exact harmless marker as a user `input_text`; merely mentioning it elsewhere does not satisfy the capability check.
- Capability reports proposal-only, no-tools, read-only, structured output, native cancellation, and `model_provenance=unavailable`.

### Fixed generation boundary

- Uses the Task 6 invocation byte-for-byte and in the specified order:

```text
exec --ephemeral --ignore-user-config --ignore-rules
--sandbox read-only --json --color never --skip-git-repo-check
--output-schema <private-schema>
--disable shell_tool --disable apps
--disable browser_use --disable browser_use_external
--disable browser_use_full_cdp_access --disable computer_use
--disable image_generation --disable workspace_dependencies
--disable skill_search --disable remote_plugin -
```

- Does not add model, profile, extra-directory, approval, or bypass flags.
- Supplies prompt bytes only through stdin.
- Creates a private per-run child directory under the caller's private job work directory, uses it as `Cmd.Dir`, writes only the schema at `0600`, and removes the child directory after the run. POSIX tests pin the directory at `0700`.
- Bounds prompt at 4 MiB, output schema at 1 MiB, stdout at 8 MiB, stderr at 256 KiB, capability stdout at 2 MiB, capability stderr at 256 KiB, and JSON nesting at 128 levels.

### Strict output, usage, and safe errors

- Requires newline-terminated JSONL with the reviewed `thread.started -> turn.started -> items -> terminal` ordering.
- Rejects malformed JSON, duplicate keys, unknown events/fields, missing or duplicate final messages, terminal-following events, invalid usage, schema-invalid proposals, and Agent-authored host accounting.
- Allows only `reasoning` and `agent_message` items. Known, separator-varied, case-varied, and camel-case tool request/call kinds map to `E_AGENT_TOOL_FORBIDDEN` even if a later proposal is valid.
- Parses exactly the five authoritative 0.147.x usage fields and derives/validates total tokens.
- Preserves private causes for diagnostics while every public error renders only the fixed `agent.ErrorCode`; stderr and paths never enter public text.

### Ruling P3: authoritative usage, unavailable model provenance

The reviewed 0.147.x exec event contract contains exact usage but no effective model field. The adapter therefore:

- returns successful proposals and exact usage with `Result.Model == ""`;
- advertises `ModelProvenanceUnavailable` in the verified capability;
- never guesses from user configuration, defaults, stderr, executable metadata, or installation metadata; and
- rejects an invented `model` field in JSONL. A separate success fixture prints a fake configured/default model on stderr and proves it is ignored while usage remains intact.

Task 7 must retain this usage, set `pricing_complete=false`, and omit run cost when model provenance is unavailable. The cost of this ruling is visible as `费用暂不可用`, not fabricated accounting.

### Native cancellation

- Unix starts a dedicated process group, captures a platform start token (Darwin kernel process start time; Linux `/proc/<pid>/stat` start time), sends TERM, waits 200 ms, then sends KILL to the group.
- Windows creates a new process group and assigns the process to a Job Object configured with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`; closing the job kills the complete assigned tree.
- Every signal/termination checks the captured start identity first. A mismatched token is refused and tested without harming the live process.
- `Cancel` is idempotent. A stop-request channel covers cancellation races before/after process start, and post-termination waiting is bounded at two seconds.
- The adapter closes the process-tree boundary after every exit, including success and nonzero exit, so orphan children cannot survive a returned result.

## Reviewed 0.147.x capability evidence

Primary source for the supported contract is the OpenAI Codex `rust-v0.147.0` tag:

- [`exec/src/cli.rs`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/exec/src/cli.rs) defines exec prompt stdin behavior and the reviewed exec flags.
- [`utils/cli/src/lib.rs`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/utils/cli/src/lib.rs) defines shared enable/disable and sandbox options.
- [`features/src/lib.rs`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/features/src/lib.rs) records all ten disabled feature names as `Stage::Stable` in 0.147.0.
- [`exec/src/exec_events.rs`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/exec/src/exec_events.rs) defines the closed JSONL event/item union and the five usage fields. `thread.started` carries only `thread_id`; `turn.completed` carries usage and no model.
- [`cli/src/main.rs`](https://github.com/openai/codex/blob/rust-v0.147.0/codex-rs/cli/src/main.rs) defines the harmless `debug prompt-input` command used by verification.
- [OpenAI Codex issue #39406](https://github.com/openai/codex/issues/39406) independently records that 0.147.0 does not emit model/provider metadata in exec JSONL; the source contract above remains authoritative.

The locally installed executable was inspected read-only as corroboration only:

```text
codex --version
codex-cli 0.150.1
```

It is intentionally outside this adapter's accepted range and was not used as 0.147.x acceptance evidence.

## TDD evidence

### Prerequisite RED

```text
go test ./internal/evidence ./internal/reviewprompt ./internal/proposal \
  -run 'Test(ValidateWarnings|BuildRejectsShortNamedSecret|BuildRejectsDuplicateCanonicalWarnings|ValidateRejectsDuplicateCanonicalWarnings)' -count=1
```

Initial result: FAIL. `evidence.ValidateWarnings` was undefined; raw short-secret cases returned `ErrInvalidInput` instead of exact `ErrUnsafeInput`; duplicate warnings could produce nonzero output.

### Adapter RED

```text
go test ./internal/agent/codex -count=1
```

Initial result: FAIL to compile because the Codex adapter, capability provenance, managed-process primitives, and fake matrix did not exist.

Hardening RED cycles additionally proved the new tests were live:

```text
go test ./internal/agent/codex \
  -run 'Test(VerifyRejectsUnconfiguredAndIncompatibleExecutables|ToolKindNormalizationRejectsSeparatorAndCaseVariants|ExecutableIdentityDetectsInPlaceContentDrift)' -count=1
```

Initial result: FAIL for noncanonical `0.147.01`, marker outside the user prompt, camel-case tool kinds, and same-inode executable drift.

```text
go test ./internal/agent/codex -run TestVerifyRejectsConcurrentVerificationAsBusy -count=1
```

Initial result: FAIL because the concurrent verification returned a capability instead of `E_AGENT_BUSY`.

```text
go test ./internal/agent/codex \
  -run 'Test(ActiveRunSignalsStopBeforeWaitingForProcessReadiness|WaitForProcessExitIsBounded)' -count=1
```

Initial result: build FAIL with missing stop-signal/bounded-wait primitives.

### Final GREEN

Focused contract and prerequisite run:

```text
go test ./internal/agent ./internal/evidence ./internal/reviewprompt ./internal/proposal ./internal/agent/codex -count=1
ok github.com/neomei/SessionReviewer/internal/agent 0.389s
ok github.com/neomei/SessionReviewer/internal/evidence 0.775s
ok github.com/neomei/SessionReviewer/internal/reviewprompt 1.826s
ok github.com/neomei/SessionReviewer/internal/proposal 1.617s
ok github.com/neomei/SessionReviewer/internal/agent/codex 5.138s
```

Required race repetition:

```text
go test -race ./internal/agent/codex -count=20
ok github.com/neomei/SessionReviewer/internal/agent/codex 69.026s
```

Full suite:

```text
go test ./...
PASS (all packages; internal/agent/codex 3.549s)
```

Static checks:

```text
go vet ./...
PASS (exit 0, no output)

git diff --check
PASS (exit 0, no output)
```

Platform build evidence on Darwin 25.5.0 arm64 with Go 1.26.5:

```text
GOOS=windows GOARCH=amd64 go test -c -o <temp>/codex.test.exe ./internal/agent/codex
PASS

GOOS=linux GOARCH=amd64 go test -c -o <temp>/codex.test ./internal/agent/codex
PASS

GOOS=windows GOARCH=amd64 go vet ./internal/agent/codex
PASS
```

The Windows liveness helper was also made real: a future native Windows run opens the recorded child PID and uses `WaitForSingleObject` instead of treating all Windows PIDs as already dead.

## Fake executable matrix

The fake covers verified success, unsupported/prerelease/noncanonical versions, missing flags/features, unstable/malformed features, malformed prompt probes, oversized probes, probe timeout with an ignored-TERM child, exact invocation/stdin/schema capture, unknown-model success, malformed/duplicate/unknown JSONL, missing/duplicate/schema-invalid final proposals, normalized tools, auth failure, timeout, explicit cancel, context cancel, busy, ignored TERM, huge stdout/stderr, invalid/missing usage, model spoofing, nonzero exit after valid output, and orphan children after both success and failure.

## Risks and follow-up

- Native Darwin exercised real process groups and live ignored-TERM child cleanup. Windows Job Object code and the native Windows test logic compile and vet, but no native Windows runner was available in this task; that remains an integration gate.
- The caller must supply the private review-job work root outside Project/Vault. The adapter enforces a non-symlink private POSIX parent and creates the isolated child, but it intentionally has no Project/Vault paths from which to infer placement. Windows inherits the caller's job-root ACL, consistent with the existing review-job store's Windows contract.
- Portable `exec.Cmd` has a final local rename window between the last path/inode/SHA-256 recheck and the OS image open. The implementation checks before every probe/run and uses native process start identities for cancellation; an adversary already able to rewrite the configured executable at that instant remains outside the trusted-local-executable assumption.
- The installed local Codex is 0.150.1. A real 0.147.x smoke was therefore not run; the exact tagged primary-source contract plus the fake executable matrix is the Task 6 acceptance evidence. Any 0.148+ support requires a new reviewed fixture/range change.
- No UI, plugin, worker, CLI, sync, watcher, pricing, release, publish, or deployment code changed.
