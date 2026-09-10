# Scan observation-boundary race-test efficiency

Date: 2026-09-10. Scope: `internal/scan/service.go`, `spool.go`, and their tests. Controller authorized the private lower-only limit after the initial test-only diagnosis. No push, tag, full-suite rerun, or default-size repeat run was performed.

## Diagnosis and historical evidence

`tag-045-arm64.log` lines 581–632 report the scan package timing out at 1,800 seconds, with `TestRunSingleSourceObservationBudgetStillFailsClosed` running for only three seconds. The preceding `TestRunMoreThanGlobalObservationBudgetAcrossSourcesSucceeds` built 32,769 distinct revisions per source and ran the complete two-source pipeline over 65,538 records. This exercises canonical hashing, validation, spool write/replay, retained conversation materialization, object persistence and prepared-generation verification. The single-source refusal test separately built 65,537 revisions even though its purpose was boundary refusal.

The same historical native Apple Silicon log, lines 434–488, shows the normal `go test -timeout 30m ./...` scan package passing in 263.414 seconds. That suite included the original 65,538-record success case. This historical success is retained as default-volume integration evidence; the replacement verifies the same limit composition using small integration fixtures plus the actual default count boundary.

A bounded local Apple Silicon baseline command ran only the old multi-source test with `-race -timeout=90s -cpuprofile=/tmp/scan-budget-baseline.cpu`. It timed out after 90.833 seconds. Its active stack was in `conversationchain.Validate/Render/Materialize` called by `scan.Run`. The 90.15-second profile collected 96.12 CPU seconds: syscall work, race instrumentation, canonical serialization/hashing and validation dominate, with ongoing spool and conversation processing. This supports excessive full-pipeline fixture volume, not a stuck lock, as the bottleneck. It does not prove an exact speedup against a completed baseline.

## Minimal changes and retained coverage

- Add an unexported `Options.sourceRevisionLimit` and per-run spool `maxCount`. The constructor retains the production default of 65,536. `Run` accepts an override only when positive and strictly below the default, before worker goroutines start. Zero, negative and larger values cannot raise or disable the default; no exported configuration was added.
- Replace the global-volume integration test with `TestRunMoreThanPerSourceObservationBudgetAcrossSourcesSucceeds`: limit 8, two sources of 5 distinct observations, real preparation/persistence, and exact two-lineage coverage of 10. It proves the combined total may exceed a per-source budget while each source remains below it.
- Keep `TestRunSingleSourceObservationBudgetStillFailsClosed`: limit 8, 9 distinct observations, typed budget refusal, failed/unprepared result, no prepared generation, no catalog source publication, and spool cleanup.
- Add `TestObservationSpoolDefaultRevisionBoundaryIsPerSourceAndSticky`: seed only the already-accepted count to 65,535, then append real valid observations. It verifies the actual 65,536th append advances the count, the 65,537th is refused without bytes changing, refusal remains sticky even if the counter is reset, and a separate source can independently reach 65,536. The seeded prefix is explicit; this is default-boundary unit evidence, not a full-volume serialized fixture.
- Existing canonical bytes, private storage, redirect rejection, in-place mutation, cancellation/error/panic cleanup, resident-source bound and replay-count tests stay active.

## Red/green verification

Before wiring the new limit, the small single-source refusal test failed behaviorally under `-race`: the 9-record scan completed and prepared a generation with no error. After implementing the lower-only seam, the focused race command below passed all 10 top-level tests and three cleanup subtests in 6.204 seconds. The multi-source and single-source budget tests took 0.83 and 0.33 seconds; the real default boundary test took 0.30 seconds. No skips were added and timeouts were not raised.

```sh
go test -race ./internal/scan -run '^(TestRunMoreThanPerSourceObservationBudgetAcrossSourcesSucceeds|TestRunSingleSourceObservationBudgetStillFailsClosed|TestObservationSpool|TestRunCleansObservationSpoolsOnCancellationErrorAndPanic|TestRunReplaysAtMostOneObservationSourcePayload|TestRunValidatesEachNewSourceWithOnlyPreApplyAndPersistenceSpoolReplays)' -count=1 -v -timeout=60s
```

`gofmt` and `git diff --check -- internal/scan` passed. Controller scoped review and integrated native CI remain the next gates. This work does not claim full-suite or release success.

## Focused race output

```text
=== RUN   TestRunMoreThanPerSourceObservationBudgetAcrossSourcesSucceeds
--- PASS: TestRunMoreThanPerSourceObservationBudgetAcrossSourcesSucceeds (0.83s)
=== RUN   TestRunSingleSourceObservationBudgetStillFailsClosed
--- PASS: TestRunSingleSourceObservationBudgetStillFailsClosed (0.33s)
=== RUN   TestObservationSpoolDefaultRevisionBoundaryIsPerSourceAndSticky
--- PASS: TestObservationSpoolDefaultRevisionBoundaryIsPerSourceAndSticky (0.30s)
=== RUN   TestObservationSpoolIsPrivateCanonicalBoundedAndCleaned
--- PASS: TestObservationSpoolIsPrivateCanonicalBoundedAndCleaned (0.31s)
=== RUN   TestObservationSpoolStartupCleansOnlyPrivateStaleRun
--- PASS: TestObservationSpoolStartupCleansOnlyPrivateStaleRun (0.01s)
=== RUN   TestObservationSpoolStaleCleanupFailsClosedOnRedirect
--- PASS: TestObservationSpoolStaleCleanupFailsClosedOnRedirect (0.01s)
=== RUN   TestObservationSpoolRejectsSameSizeInPlaceMutationDuringReplay
--- PASS: TestObservationSpoolRejectsSameSizeInPlaceMutationDuringReplay (0.32s)
=== RUN   TestRunCleansObservationSpoolsOnCancellationErrorAndPanic
=== RUN   TestRunCleansObservationSpoolsOnCancellationErrorAndPanic/cancellation
=== RUN   TestRunCleansObservationSpoolsOnCancellationErrorAndPanic/error
=== RUN   TestRunCleansObservationSpoolsOnCancellationErrorAndPanic/panic
--- PASS: TestRunCleansObservationSpoolsOnCancellationErrorAndPanic (1.08s)
    --- PASS: TestRunCleansObservationSpoolsOnCancellationErrorAndPanic/cancellation (0.37s)
    --- PASS: TestRunCleansObservationSpoolsOnCancellationErrorAndPanic/error (0.36s)
    --- PASS: TestRunCleansObservationSpoolsOnCancellationErrorAndPanic/panic (0.34s)
=== RUN   TestRunReplaysAtMostOneObservationSourcePayload
--- PASS: TestRunReplaysAtMostOneObservationSourcePayload (0.88s)
=== RUN   TestRunValidatesEachNewSourceWithOnlyPreApplyAndPersistenceSpoolReplays
--- PASS: TestRunValidatesEachNewSourceWithOnlyPreApplyAndPersistenceSpoolReplays (0.50s)
PASS
ok  	github.com/neomei/SessionReviewer/internal/scan	6.204s
```

## Bounded original-test timeout evidence

```text
=== RUN   TestRunMoreThanGlobalObservationBudgetAcrossSourcesSucceeds
panic: test timed out after 1m30s
	running tests:
		TestRunMoreThanGlobalObservationBudgetAcrossSourcesSucceeds (1m30s)

goroutine 65 [running]:
testing.(*M).startAlarm.func1()
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2802 +0x498
created by time.goFunc
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/time/sleep.go:215 +0x44

goroutine 1 [chan receive, 1 minutes]:
testing.(*T).Run(0xc000206908, {0x102cd624b, 0x3b}, 0x103008788)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2109 +0x7dc
testing.runTests.func1(0xc000206908)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2585 +0x78
testing.tRunner(0xc000206908, 0xc00071fab8)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2036 +0x168
testing.runTests({0x102cc7308, 0x21}, {0x102cd0eb6, 0x2f}, 0xc0000b0648, {0x1030605e0, 0x35, 0x35}, {0xc00009be80?, 0xc00044a4e0?, ...})
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2583 +0x7a4
testing.(*M).Run(0xc00041d360)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2443 +0xb3c
main.main()
	_testmain.go:150 +0x104

goroutine 33 [runnable]:
regexp.(*inputString).step(0xc00a8e0918, 0x4)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/regexp/regexp.go:385 +0xe4
regexp.(*Regexp).doOnePass(0xc0000a1680, {0x0, 0x0}, {0x0, 0x0, 0x0}, {0xc00afde5d7, 0x9}, 0x0, 0x0, ...)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/regexp/exec.go:497 +0x8e0
regexp.(*Regexp).doExecute(0xc0000a1680, {0x0, 0x0}, {0x0, 0x0, 0x0}, {0xc00afde5d7, 0x9}, 0x0, 0x0, ...)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/regexp/exec.go:532 +0x318
regexp.(*Regexp).doMatch(...)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/regexp/exec.go:514
regexp.(*Regexp).MatchString(...)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/regexp/regexp.go:507
github.com/neomei/SessionReviewer/internal/conversationchain.validID({0xc00afde5d7, 0x9})
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/conversationchain/validate.go:17 +0x98
github.com/neomei/SessionReviewer/internal/conversationchain.validateSourceRef({0x1, {0x102cb5d9f, 0x5}, {0xc007f4da40, 0x47}, {0x102cb929c, 0x9}, {0x102cb5bfb, 0x5}, {0xc002524667, ...}, ...}, ...)
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/conversationchain/validate.go:211 +0xb4
github.com/neomei/SessionReviewer/internal/conversationchain.Validate({0x1, {0x102cb5d9f, 0x5}, {0xc007f4da40, 0x47}, {0x102cb929c, 0x9}, {0x102cb5bfb, 0x5}, {0xc002524667, ...}, ...})
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/conversationchain/validate.go:92 +0x11b0
github.com/neomei/SessionReviewer/internal/conversationchain.Render({0x1, {0x102cb5d9f, 0x5}, {0xc007f4da40, 0x47}, {0x102cb929c, 0x9}, {0x102cb5bfb, 0x5}, {0xc002524667, ...}, ...})
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/conversationchain/codec.go:31 +0xa0
github.com/neomei/SessionReviewer/internal/conversationchain.Materialize({{0x1, {0xc00fdda5f0, 0x47}, {0x102cb929c, 0x9}, {0x102cb5bfb, 0x5}, {0xc002524667, 0x9}, {0xc002524678, ...}, ...}, ...})
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/conversationchain/retained.go:143 +0x1884
github.com/neomei/SessionReviewer/internal/scan.materializeConversation({_, _}, {_, _}, {_, _}, {{0x1, {0x102cb5bfb, 0x5}, {0xc002524667, ...}, ...}, ...}, ...)
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/scan/conversation.go:44 +0x414
github.com/neomei/SessionReviewer/internal/scan.Run({_, _}, {{0x102cb929c, 0x9}, {{0x102cb929c, 0x9}, {0xc0000ec360, 0x82}, {{0x102cbc8a9, 0xf}, ...}, ...}, ...})
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/scan/service.go:312 +0x25f0
github.com/neomei/SessionReviewer/internal/scan.TestRunMoreThanGlobalObservationBudgetAcrossSourcesSucceeds(0xc000206008)
	/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-v4-scan-display/internal/scan/service_test.go:1245 +0x4fc
testing.tRunner(0xc000206008, 0x103008788)
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2036 +0x168
created by testing.(*T).Run in goroutine 1
	/opt/homebrew/Cellar/go/1.26.5/libexec/src/testing/testing.go:2101 +0x7c0
FAIL	github.com/neomei/SessionReviewer/internal/scan	90.833s
FAIL
```

## Bounded original-test profile summary

```text
File: scan.test
Type: cpu
Time: 2026-09-10 23:01:33 CST
Duration: 90.15s, Total samples = 96.12s (106.62%)
Showing nodes accounting for 65.36s, 68.00% of 96.12s total
Dropped 417 nodes (cum <= 0.48s)
Showing top 12 nodes out of 147
      flat  flat%   sum%        cum   cum%
    15.12s 15.73% 15.73%     15.12s 15.73%  syscall.rawsyscalln
    11.95s 12.43% 28.16%     11.95s 12.43%  __tsan::MemoryAccessRangeT
    10.05s 10.46% 38.62%     10.05s 10.46%  __tsan_read
     7.28s  7.57% 46.19%      7.28s  7.57%  __tsan::MemoryRangeSet
     6.34s  6.60% 52.79%      6.34s  6.60%  racecall
     3.93s  4.09% 56.88%      3.93s  4.09%  runtime.madvise
     2.71s  2.82% 59.70%      2.71s  2.82%  racecalladdr
     2.47s  2.57% 62.27%      2.47s  2.57%  __tsan_func_enter
     1.64s  1.71% 63.97%      3.26s  3.39%  github.com/neomei/SessionReviewer/internal/memory.canonicalCheckpoint
     1.63s  1.70% 65.67%      1.63s  1.70%  __tsan_write
     1.14s  1.19% 66.85%      1.14s  1.19%  runtime.pthread_cond_signal
     1.10s  1.14% 68.00%      1.10s  1.14%  racefuncenter
```
