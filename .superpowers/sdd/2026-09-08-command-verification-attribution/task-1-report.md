# Task 1 Report: Command verification attribution

## Implemented

- Replaced leading-token `strings.Fields` attribution with a bounded literal command lexer. It accepts conservative quoted literal arguments but rejects shell sequencing, pipelines, background execution, redirection, substitutions, expansions, unmatched quotes, and oversized input before specialized classification.
- Kept generic `command_started` / `command_finished` evidence and authoritative exit codes unchanged. Unsupported shell grammar normalizes to `command_signature=other` with no verification or Git classification.
- Added narrow Go verification eligibility for supported literal flags. Help, list, compile-only, dry-run, unknown flags, `-count<=0` or invalid counts, and execution wrappers (`-exec`, `-toolexec`) do not produce specialized verification.
- Added narrow npm eligibility. Help, dry-run, ignore-scripts, unknown npm flags, and unknown forwarded script flags do not produce verification. The explicitly supported forwarded form is `--runInBand` after `--`.
- Tightened supported Git classification to exact normalized argument sets, preventing compound/extra-argument stdout from becoming fabricated HEAD, branch, tag, or status facts.
- Corrected only `assertGateMalformedContinuation` for the existing malicious `go test ... && touch ...` fixture: it now requires exact generic start and successful finish evidence and explicitly rejects specialized verification. The malicious fixture, process canary, capability counts, malformed continuation, usage, diagnostics, and baselines were not changed.

## TDD evidence

### RED

Command:

```text
go test ./internal/source/codex -run 'CommandAttribution|CommandClassification|VerificationComponent' -count=1
```

Initial relevant failure before production edits:

```text
--- FAIL: TestCommandAttributionRejectsAggregateShellExit (0.04s)
    command_classification_test.go:21: shell aggregate exit was attributed to child verification
FAIL
FAIL github.com/neomei/SessionReviewer/internal/source/codex 0.734s
```

Additional RED cycles caught the unsupported shell operators/substitutions, help/list/compile-only/dry-run/unknown modes, Go execution wrappers and zero/invalid counts, quoted-argument component normalization, and npm forwarded `--listTests` / `--list` / unknown flags before each scoped implementation amendment.

### GREEN

Final focused command:

```text
go test ./internal/source/codex -run 'CommandAttribution|CommandClassification|VerificationComponent' -count=1
```

Output:

```text
ok github.com/neomei/SessionReviewer/internal/source/codex 0.630s
```

The decoder fixture proves `go test ./... || true` exit 0 retains generic successful completion but emits no verification, while direct `go test ./...` exit 0/1 still emits passed/failed verification.

## Verification

Affected packages:

```text
go test ./internal/source/codex ./internal/conversationchain ./internal/inspect -count=1
ok github.com/neomei/SessionReviewer/internal/source/codex 6.615s
ok github.com/neomei/SessionReviewer/internal/conversationchain 0.448s
ok github.com/neomei/SessionReviewer/internal/inspect 20.503s
```

Amended Gate A coverage:

```text
go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1
ok github.com/neomei/SessionReviewer/test/zerotoken 40.659s
```

Static checks:

```text
go vet ./internal/source/codex ./internal/conversationchain ./internal/inspect ./test/zerotoken
git diff --check
```

Both exited 0 with no output.

One full suite was run before commit:

```text
go test ./... -count=1
```

All printed packages passed except `github.com/neomei/SessionReviewer/test/zerotoken`. Its sole failure was the old Gate A assertion requiring specialized verification for the compound malicious fixture:

```text
--- FAIL: TestGateAZeroTokenCore (79.61s)
    gate_a_test.go:142: post-malformed evidence request=true tool=false diagnostic=true diagnostics=2 ...
FAIL github.com/neomei/SessionReviewer/test/zerotoken 186.289s
```

This was reported before baseline changes. The controller then extended the reviewed task contract to correct only that contradictory assertion. The covering focused Gate A test above is green after the correction; per controller direction, unrelated full packages were not rerun.

## Files changed

- `internal/source/codex/decode.go`
- `internal/source/codex/command_classification_test.go`
- `test/zerotoken/gate_a_test.go`
- `.superpowers/sdd/2026-09-08-command-verification-attribution/task-1-report.md`

## Self-review

- Confirmed specialized fields remain empty for every ambiguous/no-execution case while generic start/finish facts remain present.
- Confirmed command/stdout/path canaries do not enter persisted command classifications; only normalized signatures/components are emitted.
- Confirmed supported direct Go/npm/Git forms and quoted literal arguments retain normalized classifications.
- Confirmed the Gate A change is limited to the malformed-continuation evidence assertion and does not weaken its no-process canary or capability baselines.
- No wire schema, dependency, real data, network, Vault, UI, release, or captured-command execution changes were made.

## Concerns and conservative exclusions

- Classification intentionally withholds specialized evidence for shell expansions/operators, unknown Go/npm flags, and npm forwarded script arguments other than `--runInBand`. Some safe commands may therefore remain generic; this is the accepted conservative cost of preventing false milestone qualification.
- A post-correction full `go test ./...` was not rerun by explicit controller direction; the corrected Gate A test and every affected source/package/static check are green.

## Round 1 review fixes

### Changes

- Expanded the supported direct `go test` grammar to retain ordinary executing flags used by repository acceptance commands: boolean `-json` and `-failfast`; value flags `-skip` and `-bench`. Existing `-race`, `-timeout`, `-run`, and other supported flags remain accepted with correct value arity.
- Preserved `go test -race -timeout 30m ./... -skip 'SlowAcceptance|NetworkOnly'`, `go test -json -failfast ./...`, and mixed test-plus-benchmark selection as typed test verification.
- Kept explicit benchmark-only `go test -run '^$' -bench ...` generic, so a benchmark run does not claim tests completed. Unknown flags, wrappers, and zero/invalid counts remain rejected.

### TDD evidence

RED command:

```text
go test ./internal/source/codex -run 'CommandAttribution|CommandClassification|VerificationComponent' -count=1
```

Relevant failure before the amendment:

```text
--- FAIL: TestCommandClassificationPreservesSupportedLiteralCommands (0.00s)
    --- FAIL: TestCommandClassificationPreservesSupportedLiteralCommands/repository_acceptance_go_test (0.00s)
        command_classification_test.go:98: classification={signature:go:test verification: verificationOperation: gitOperation:} want signature="go:test" component="package" operation="test" git=""
    --- FAIL: TestCommandClassificationPreservesSupportedLiteralCommands/json_failfast_go_test (0.00s)
        command_classification_test.go:98: classification={signature:go:test verification: verificationOperation: gitOperation:} want signature="go:test" component="package" operation="test" git=""
    --- FAIL: TestCommandClassificationPreservesSupportedLiteralCommands/test_and_benchmark_selection (0.00s)
        command_classification_test.go:98: classification={signature:go:test verification: verificationOperation: gitOperation:} want signature="go:test" component="package" operation="test" git=""
FAIL
FAIL github.com/neomei/SessionReviewer/internal/source/codex 0.719s
```

GREEN focused output:

```text
ok github.com/neomei/SessionReviewer/internal/source/codex 0.717s
```

### Final verification on frozen code

```text
go test ./internal/source/codex ./internal/conversationchain ./internal/inspect -count=1
ok github.com/neomei/SessionReviewer/internal/source/codex 5.972s
ok github.com/neomei/SessionReviewer/internal/conversationchain 0.661s
ok github.com/neomei/SessionReviewer/internal/inspect 20.305s

go vet ./internal/source/codex ./internal/conversationchain ./internal/inspect ./test/zerotoken
git diff --check
```

Vet and diff check exited 0 with no output.

Required final whole suite:

```text
go test ./... -count=1
```

Exit 0. Every package passed; the final lines were:

```text
ok github.com/neomei/SessionReviewer/internal/source/codex 21.910s
ok github.com/neomei/SessionReviewer/internal/sync 161.364s
ok github.com/neomei/SessionReviewer/internal/syncproject 140.265s
ok github.com/neomei/SessionReviewer/test/reviewjob 46.571s
ok github.com/neomei/SessionReviewer/test/zerotoken 208.603s
```

### Round 1 self-review

- Verified value-bearing test flags are consumed as flag values and cannot become the reported package component.
- Verified benchmark selection qualifies only when the test pattern is not the known empty `^$` / `$^` form; broader unknown semantics remain conservatively unsupported rather than inferred.
- No controller documentation, capability baselines, wire schemas, or unrelated files were changed.
