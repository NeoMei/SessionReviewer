# Codex Tool Evidence Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Continue through task review before dependent closure work.

**Goal:** Recover supported current-host command/patch evidence without treating wrapper completion or stdout as process verification.

**Architecture:** Adapt current Codex source envelopes into the existing typed decoder, preserving authenticated source refs, affinity, duplicate-call invalidation and redaction. A conservative static adapter recognizes bounded literal exec wrappers; it never executes their JavaScript and never guesses associations for ambiguous wrappers.

**Tech Stack:** Existing Go decoder and test fixtures; no model, network or new dependency.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§2,4,5.1,12,13,17.3 and `2026-09-04-conversation-chain-evolution-closure.md` Task1.

## Global Constraints

- Ordinary scans and deterministic projection start zero Agent processes.
- Only visible user/assistant content and typed tool facts participate; no system/developer instructions, hidden reasoning or raw tool output persistence.
- `execution_verified` never means `workflow_state=resolved`.
- Preserve existing affinity, duplicate-call invalidation, source authentication and bounded excerpts. Unknown/missing/conflicting outcomes remain unverified and have explicit coverage diagnostics.
- Do not execute source JavaScript or shell during decoding. Do not infer child process success from `Script completed`, a tool wrapper exit or stdout prose.
- No migration, cross-platform expansion, dependencies, daily-Vault changes or release within these task commits. The overall user-authorized GitHub release follows all remaining spec gates.

### Task 1: Current direct tool envelopes and trustworthy terminal metadata

**Files:** Modify `internal/source/codex/decode.go`, create `internal/source/codex/tool_envelope.go` if needed to isolate parsing; tests in `internal/source/codex/tool_envelope_test.go` and `decode_test.go`. Modify `internal/contextupdate/service.go` and tests only to select a versioned decoder after behavior changes; preserve predecessor supersession.

**Interfaces:** Existing `addToolCall`, `addToolOutput`, `pendingCall` and `DecodeReport` remain the consumers. Normalize `function_call.arguments` and `custom_tool_call.input` only for exact supported tool names; `function_call_output` shares exact call identity. Keep structured terminal metadata distinct from text body. Existing `parseGitOutput` receives only authenticated terminal output body after stripping its envelope, not arbitrary whole records.

- [ ] RED actual Decode fixtures: modern `function_call` exec_command followed by structured `{exit_code:0,output:"PASS"}` produces command/verification evidence; nonzero produces failure; null/missing exit with live session ID does not produce a finished/success fact. Existing custom call still works. Unknown tools/reasoning remain outside evidence. Duplicate IDs across legacy/modern forms invalidate previous results.
- [ ] RED metadata-spoof tests: stdout contains `exit code: 0` under an authoritative exit_code1 => failure; contradictory/malformed top-level terminal fields => no verified result; wrapper `Script completed` alone => unknown. Recognize the direct tool format `Process exited with code N` only in the metadata header before the `Output:` delimiter, not in body lines. Legacy fixture `exit code: N` remains supported but ambiguous conflicting markers are rejected.
- [ ] RED raw apply_patch input and supported native patch-success envelope produce only bounded target facts; malformed/failed patch and duplicate call IDs never produce a successful change. Structured metadata must not be concatenated with arbitrary text to create evidence.

```go
// Literal expectations, through real Decode rather than parser-only mocks:
// modern exec go test ./... + exit_code:0 -> command_finished/success and verification/passed
// same command + exit_code:1, output:"exit code: 0" -> verification/failed
// same command + exit_code:null, session_id:42 -> no command_finished
```

- [ ] Run focused RED `go test ./internal/source/codex -run 'ToolEnvelope|ModernTool|TerminalMetadata|NativePatch' -count=1`, implement only supported forms and reuse typed fact emission. Keep the legacy unknown-tool policy and privacy exclusions.
- [ ] Version changed decoder at production registration and test stable re-scan/supersession; new evidence cannot silently reuse an old decoder identity. No schema change is needed.
- [ ] GREEN `go test ./internal/source/codex ./internal/contextupdate ./internal/inspect -count=1`, scoped `go vet`, `git diff --check`; one full `go test ./...` before commit. Commit exact task files and record RED/GREEN plus unresolved wrapper coverage.

### Task 2: Conservative literal exec-wrapper evidence

**Files:** Create `internal/source/codex/exec_wrapper.go`, `exec_wrapper_test.go`; modify decoder/envelope integration and its tests. Production decoder version increments only if Task1 already committed a distinct version.

**Interfaces:** Consume original wrapper input and original output blocks before concatenation. Produce a normalized pending supported operation only when source code establishes exactly one literal call to `tools.exec_command` or `tools.apply_patch` and the emitted result has uniquely attributable tool metadata. Support the real forms `text(await tools.exec_command({...}));` and `const r = await tools.exec_command({...}); text(r);`, whitespace and the optional leading `// @exec:` directive. `text(r.output)` may expose body but cannot prove a terminal status. JSON literals only; never interpret variables, interpolation, computed calls, dynamic code or loops.

- [ ] RED real Decode tests for singleton JSON-literal wrapper exec with structured result block, singleton patch with attributable success, wrapper without result metadata, multi-call outputs, commented-out fake calls, string-contained fake calls, conditional/dead code, dynamic argument and mismatched output count. Wrapper success is never child success; unrecognized execution-bearing wrappers increment bounded unsupported coverage rather than disappearing as complete.
- [ ] Implement a bounded whole-input grammar, not unanchored search for a tools call. Preserve strings/escapes and refuse trailing executable statements beyond the supported result emission. Arbitrary wrapper source is data only. Unsupported patterns remain visible partial coverage, not invented commands.

```go
// Whole-input acceptance examples:
// text(await tools.exec_command({"cmd":"go test ./..."}));
// const r = await tools.exec_command({"cmd":"go test ./..."}); text(r);
// Rejection: if(false){text(await tools.exec_command({...}));}
// Rejection: text("tools.exec_command(...)");
```

- [ ] Use source call ID for exact wrapper identity and preserve source record hash; do not fabricate individual child IDs for multi-call wrappers. Known unsupported patterns get a typed diagnostic, no sensitive source code in messages.
- [ ] GREEN focused wrapper tests and full source/scan/inspect/zero-token regressions. Read-only actual Session check reports counts by captured/unsupported category; no automatic rescan or public-file write merely to test the parser. Commit exact files; independent review precedes closure projection.
