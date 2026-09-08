# Project Lock Test Liveness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after the active query implementation is frozen, before treating a full-suite lock-test retry as clean evidence.

**Goal:** The subprocess lock fixture remains alive and holds the lock until the parent deliberately kills it, so the existing crash-recovery test is deterministic.

**Architecture:** Replace the helper's bare `select {}` with a parent-owned stdin control pipe and a tiny readiness/liveness handshake. Continue to use the real advisory lock and real child termination; change no production lock behavior.

**Tech Stack:** Existing Go testing/os/exec/bufio and standard pipes; no dependencies.

**Spec:** `docs/verification/2026-09-08-release-execution.md` repeated full-suite regression gate and user requirement to fix worthwhile test/system bugs.

## Global Constraints

- Modify only `internal/project/lock_test.go`. Production locking, zero-token baselines and release artifacts remain unchanged.
- Temporary directory only; no source/Vault/main/remote writes. No Agent invocation.
- Preserve positive proof that a live owner prevents acquisition, killing it permits acquisition, and repeated release is harmless.
- No fixed sleep, longer lock timeout, swallowed assertion, skipped test or success-by-retry workaround.

### Task 1: Keep the lock owner alive with a real pipe and verify its lifecycle

**Files:** Modify `internal/project/lock_test.go` only.

**Interfaces:** Existing `TestProjectLockSerializesProcessesAndSurvivesOwnerCrash` and `TestProjectLockSubprocessHelper`; existing `AcquireProjectLock` is not changed.

- [ ] RED: before changing the helper, extend the parent with `command.StdinPipe()` and a persistent bounded stdout reader. After `READY`, send `PING\n` and require `ALIVE` or fail on EOF/error/bounded timeout. The existing bare-select helper cannot acknowledge and exits with Go deadlock. Ensure the existing parent checks still run only after the liveness acknowledgement.

```go
control, err := command.StdinPipe()
if err != nil { t.Fatal(err) }
defer control.Close()
// After READY, retain the same stdout scanner/reader for the acknowledgement.
if _, err := fmt.Fprintln(control, "PING"); err != nil { t.Fatal(err) }
// Require exactly ALIVE with the existing bounded readiness deadline.
```

- [ ] Run `go test ./internal/project -run '^TestProjectLockSerializesProcessesAndSurvivesOwnerCrash$' -count=1` and record the target RED, not a compilation/setup error. Controller direct old-helper proof already prints `READY` then `fatal error: all goroutines are asleep - deadlock!` and exits2 at lock_test.go107; it is not proof of broken production flock.
- [ ] Implement a real blocked read in the child after it acquires the real lock and prints `READY`:

```go
control := bufio.NewScanner(os.Stdin)
for control.Scan() {
    if control.Text() != "PING" { t.Fatal("invalid lock helper control message") }
    fmt.Println("ALIVE")
}
if err := control.Err(); err != nil { t.Fatal(err) }
t.Fatal("lock helper control pipe closed before termination")
```

- [ ] Keep the write end open in the parent for the complete held-lock assertion. Parent intentionally kills and waits for the child, then proves reacquisition. Cleanup must always close the pipe and kill/reap the exact child on failures; avoid a readiness goroutine leaking on failed startup or EOF. Capture bounded child stderr for meaningful helper-death diagnostics without mixing it into readiness messages.
- [ ] Extend the parent test to send another PING after verifying `ErrProjectLocked`; require ALIVE before deliberate kill. This proves the child did not die merely after its first readiness line. If an unexpected acquisition returns a nonnil lock, release it before failing so fixture cleanup is exact.
- [ ] GREEN focused test once, then `go test ./internal/project -run 'ProjectLock' -count=5`, `go test -race ./internal/project -run 'ProjectLock' -count=1`, and full `go test ./internal/project -count=1`. Run `go vet ./internal/project`, diff check. Do not run another whole-repository suite concurrently with the active query worker; controller schedules the final combined suite.
- [ ] Commit only the test file and write a report with RED/GREEN commands/results, cleanup/liveness evidence and unchanged production code. Obtain independent scoped spec/quality review.

## Self-review

The contract is a single test-process lifecycle correction: READY is not a guarantee of ongoing liveness, and a bare select can cause runtime deadlock in a test binary launched without a timeout timer. The control pipe supplies a real wait condition and independently observable ALIVE acknowledgements. Production locking and its expected error remain intact; successful reacquisition still requires an actual killed owner.
