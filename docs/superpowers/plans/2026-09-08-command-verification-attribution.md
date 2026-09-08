# Command Verification Attribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after the active retained materializer task is reviewed, before milestone projection.

**Goal:** Prevent shell-level success and explicit no-execution command modes from becoming false typed verification evidence.

**Architecture:** Reproduce the decoder boundary error with synthetic source records, then narrow semantic classification while preserving the real generic command start/finish and exit code. Do not execute or interpret arbitrary shell text.

**Tech Stack:** Existing Go Codex decoder and fixture helpers; no dependency or model call.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§4.1,5.1,17.3: execution and validation must be supported by captured evidence, not inferred from an Agent claim.

## Global Constraints

- Routine scanning starts zero Agent processes and never executes captured commands.
- A shell command's exit code authenticates that command invocation, not every child command in it.
- Preserve generic command observations and real exit codes; withhold unsupported verification/git qualification rather than inventing failure or deleting source evidence.
- Do not persist raw commands, shell operands, stdout or private paths. Existing normalized command signature and component contracts remain.
- No source scans, publication, Vault writes, network, migration or release in this task.

### Task 1: Reject ambiguous and non-executing semantic command attribution

**Files:** Modify `internal/source/codex/decode.go`; create focused `internal/source/codex/command_classification_test.go`. A small same-package classification helper file is allowed if needed to keep responsibilities clear. Existing wire schemas remain unchanged.

Reviewed integration scope extension: modify only `assertGateMalformedContinuation` in `test/zerotoken/gate_a_test.go` to follow the corrected conservative semantics. Its actual fixture records `go test ./internal/example && touch PROJECT-SCRIPT-MUST-NOT-RUN`; assert exact matching generic command start/finish and authoritative successful outcome, and explicitly reject specialized verification for that compound invocation. Keep the malicious-command canary, zero-process checks, malformed-source continuation, usage, diagnostic and permission checks intact. Do not regenerate import/capability baselines or weaken any no-execution guard.

**Interfaces:** Existing `classifyCommand(command string) commandClass` feeds `pending.verificationComponent`, `verificationOperation` and `gitOperation`; `addToolOutput` generates typed observations only when those classifications are present. Preserve this interface and generic completion path.

- [ ] Diagnose and RED: build an actual decoder fixture for `go test ./... || true` with authoritative shell exit0. Assert a generic successful `command_finished` remains but no `verification` observation is emitted. Contrast direct `go test ./...` exit0/1, which remains passed/failed. Confirm the current failure before editing production code.

```go
for _, fact := range revisions {
    if fact.Key.Kind == "verification" {
        t.Fatal("shell aggregate exit was attributed to child verification")
    }
}
```

- [ ] Add focused real-value classification cases covering `;`, newline, `&&`, `||`, pipelines, background execution, redirection, command/process substitution and backticks. Conservative unsupported classification is valid; do not build a shell interpreter or infer success from output prose. Ordinary direct Go/npm/git supported commands must retain their normalized classification.
- [ ] Add no-execution cases: Go test help/list/compile-only, Go build/vet dry-run or help, npm help/dry-run/ignore-scripts. Use exact flag spelling and forms represented by fixtures; handle `=value` and separated forms where applicable. Withhold specialized verification rather than reporting a test/build/lint completed. Treat unknown flags conservatively where their execution semantics cannot be established from the supported command grammar.
- [ ] Preserve privacy and bounded parsing; tests include arbitrary executable/operand canaries and normalized components. Compound git output must not create a fabricated HEAD/branch/tag observation even when its final stdout resembles a valid value.
- [ ] Run RED `go test ./internal/source/codex -run 'CommandAttribution|CommandClassification|VerificationComponent' -count=1` and retain the exact failing assertion.
- [ ] Implement the smallest eligibility guard before specialized classification. Parse only a bounded supported literal argument grammar; malformed/ambiguous syntax returns a normalized generic signature with empty verification/git fields. Keep typed generic start/finish unchanged.
- [ ] GREEN focused tests, `go test ./internal/source/codex ./internal/conversationchain ./internal/inspect -count=1`, scoped vet and `git diff --check`. One full Go suite before exact-file commit. Report commands/output and any conservative coverage exclusions; independent task review precedes downstream milestone qualification.

## Preflight

Static hypothesis: `strings.Fields` currently matches the leading executable/subcommand without validating shell structure; `addToolOutput` then attributes the entire exec invocation's exit code to that subcommand. The hypothesis is not accepted as reproduced until the decoder fixture fails.

Ruling: Unsupported shell grammar retains generic command evidence but receives no specialized verification — captured aggregate exit status cannot prove a child outcome — cost if wrong is conservative missing qualification, not false completed milestones.

Ruling: Correct the existing Gate A malformed-continuation expectation in this task — the old assertion requires a specialized verification for a compound command and contradicts the accepted conservative decoder boundary — cost if wrong is reduced semantic qualification; explicit generic evidence and zero-execution assertions remain required. This is a focused contract correction, not approval to regenerate capability baselines.
