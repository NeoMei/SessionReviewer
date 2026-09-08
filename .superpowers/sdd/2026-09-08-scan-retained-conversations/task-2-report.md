# Task 2 implementation report

## Status checkpoint

Implementation and frozen verification are complete pending the exact Task 2 commit. Work is limited to the retained conversation query, conservative answer classification, optional response extension, strict consumers, actual Session host wiring, tests, and the one required frozen dependency-review edge. Controller-owned documentation commits/files were not staged or edited.

## Implemented

- Authenticated current/historical retained-chain selection through the existing published inspection callback, reusing its store, manifest, SessionView, revisions, project and index authentication.
- Source-full reads through the shared configured-root resolver and exact authenticated prefix; source failure degrades only to an already authenticated retained chain.
- Exact current chain selection and unique availability-equivalent historical selection using provider, Session ID, source identity, restored SourceRecord digest, and exact active revision order; current index view and historical evidence view remain distinct.
- Retained-only messages expose authenticated excerpts with `text: null`, phase metadata absent, explicit body availability and unknown-vs-known coverage diagnostics.
- Selected action/result evidence has bounded shown/total/omitted semantics. Message paging budgets around fixed evidence/cursor overhead so long answers remain accessible under the 1 MiB response bound.
- Shared conservative answer classification for source-backed and retained materialization, including final then commentary, commentary then unphased, final then commentary then unphased, empty final, source gaps, normal final, commentary-only and unanswered cases.
- Conversation cursor topology, response identity, mode, initial origin, adjacency, stable dependency/coverage and selected-turn identity guards in Go and TypeScript.
- Session renderer permits retained queries with a compatible CLI, makes zero private callbacks in no-CLI mode, labels retained excerpts/full-body absence and partial answers honestly, and preserves authenticated `visible_reader_unsupported` as distinct from no answer.
- Optional Go/TypeScript/schema fields preserve legacy omission behavior. Fixed CLI argv and public request shape remain unchanged.

## TDD and debugging evidence

RED (controller/repository evidence established before or during implementation):

- `go test -overlay /tmp/session-reviewer-scan-chain-qa.6VBnCG/new-project-cli-overlay.json ./internal/cli -run '^TestControllerPublishedRetainedConversationLifecycle$' -count=1` initially failed after removal plus successful rescan with `published retained conversation is unavailable or corrupt`.
- `node /tmp/session-reviewer-scan-chain-qa.6VBnCG/published-ui-qa.mjs` initially failed strict Go-to-TypeScript parsing with `$ has an invalid exact shape` before the optional response extension consumer existed.
- Controller adjacency probe initially rendered a skipped page and followed turn 3 (`renderedSkippedPage=true`, `wrongFollowups=1`).
- Repository `TestConversationEvidenceBudgetStillPagesEveryVisibleMessage` initially failed with `conversation page exceeds its byte limit`; root cause was fixed action/result evidence consuming unaccounted fixed response bytes.
- First `npm run check` after the behavior changes failed only two `no-unnecessary-type-assertion` lint errors; both were removed without semantic changes.

GREEN focused evidence:

- `go test ./internal/conversationchain ./internal/inspect -run 'Conversation|Visible|Retained' -count=1` passed.
- `go test ./internal/inspect ./internal/cli -run 'RetainedConversation|ConversationCursor|ConversationReadOnly' -count=1` passed (`internal/inspect` 3.070s; CLI package had no matching tests).
- `go test ./internal/inspect -run 'ConversationEvidenceBudget|RetainedConversationCompatibility|ConversationExposesAuthenticated|ConversationRetainedEvidence' -count=1` passed (1.507s).
- `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts tests/render-scan-records.test.ts` passed 3 files / 103 tests.
- Controller actual publication lifecycle passed with source-full, removed pre-rescan retained, removed+rescan historical evidence-view, and restored source-full states; controller reported 3.266s.
- Controller actual Go publication fixture -> strict TypeScript parser -> real renderer passed at 1200px and 390px, including four source phases, exact user/answer content, retained copy, keyboard Enter, zero no-CLI calls, no overflow/console errors.
- Controller long-conversation publication gate passed: 121 visible messages, six source-full pages and two retained pages, exact ordering, no duplicates/loss, every response <= 1 MiB; controller reported 3.331s.
- Controller adjacency probe passed after the UI response-boundary guard (`renderedSkippedPage=false`, `wrongFollowups=0`).

## Frozen gates so far

- Controller fresh frozen `npm run check` session `85026` exited 0: lint; 27 test files / 466 tests; TypeScript typecheck; production esbuild bundle.
- First `go test ./...` run passed all Task 2 packages but failed two independent gates:
  - `internal/project/TestProjectLockSerializesProcessesAndSurvivesOwnerCrash`: `held lock error=<nil>`. Controller later proved this is a separate test-helper liveness defect, not a transient production-lock result: after printing `READY`, `TestProjectLockSubprocessHelper` blocks in `select{}`, the Go runtime exits with `fatal error: all goroutines are asleep - deadlock!`, and the process releases the OS lock before the parent assertion. The queued controller plan is `2026-09-08-project-lock-test-liveness.md`; no Task 2 or lock changes were made here. An immediate isolated rerun happened to pass in 0.490s but does not erase the defect.
  - `test/zerotoken/TestGateAZeroTokenCore`: required human-reviewed production import edge `internal/source/codex -> internal/platform` was absent. This edge is the task-mandated common source-root resolver dependency; the exact frozen edge was added to `testdata/zero-token/gate-a-production-import-edges.txt`.
- The earlier focused zero-token rerun and second full-Go process exit were not preserved in the report/tool context before compaction, so no result is claimed from either.
- Controller fresh frozen `go test ./... -count=1` session `69660` exited 0 with every package passing. Recorded package timings include `internal/inspect` 99.346s, `internal/scan` 373.141s, `internal/project` 52.487s, and `test/zerotoken` 220.914s. This pass verifies the Task 2 tree but does not erase the separately reproduced project-lock helper defect above.
- Recovery scoped static checks passed: `go vet ./internal/conversationchain ./internal/inspect ./internal/source/codex` exited 0; `./node_modules/.bin/tsc --noEmit` exited 0; `npm run lint -- --quiet` exited 0. A direct bare ESLint attempt was discarded as a setup error because the repository requires its `--flag unstable_native_nodejs_ts_config` wrapper; no dependency was installed or changed.
- Final `gofmt -d` over every changed Go file and `git diff --check` both exited 0 with no output.

## Files changed (checkpoint)

- `internal/conversationchain/materialize.go`, `materialize_test.go`, `retained.go`
- `internal/inspect/conversation.go`, `conversation_test.go`, `conversation_retained.go`, `conversation_retained_test.go`, `service.go`
- `internal/source/codex/visible.go`
- `obsidian-plugin/src/cli/runner.ts`, `contracts/conversation-page.ts`, `data/conversation-page.ts`, `view/render-conversation.ts`, `view/render-scan-records.ts`
- `obsidian-plugin/tests/conversation-page.test.ts`, `render-conversation.test.ts`, `render-scan-records.test.ts`
- `schemas/conversation-page-v1.schema.json`
- `testdata/zero-token/gate-a-production-import-edges.txt`

## Self-review / concerns

- Provider-specific visible-reader implementations remain intentionally Codex-only. Same native IDs cannot cross provider at the public query boundary, but Claude/OpenCode source-full parity is not claimed or implemented.
- Fresh-project v2 initializer incompatibility is a separately recorded controller task and was not changed here.
- No real user source, Vault, network, paid call, main branch, remote branch, publication, provider adapter, milestones, problem graph or pricing work was performed.
- Provider-specific visible source readers remain intentionally Codex-only; this task proves generic retained query identity at the public boundary, not Claude/OpenCode source-full parity.
- The dependency edge `internal/source/codex -> internal/platform` is deliberate and narrow: `codex.SessionsRoot` now delegates to the existing common `platform.ResolveSessionsRoot(platform.CurrentEnv())`, matching scan/query configured-root precedence without adding an Agent/process boundary or changing the fixed public request/CLI argv.
- Re-read the Task 2 brief after recovery and reviewed the complete diff for scope, authentication, historical/current binding, retained phase omission, diagnostic unknown semantics, payload budgets, cursor adjacency/identity, fixed request shape, source-root precedence, and no-CLI behavior. No additional implementation edit was necessary after the frozen gate.
- Exact staging/commit and independent task review remain; native candidate acceptance, provider adapters, initializer repair, and the separate lock-test liveness repair are intentionally outside this implementation.

## Fix round 1 (review findings 1-6)

All six review findings are addressed in the bounded query/wire/renderer surface. This round keeps provider-specific source readers Codex-only while making authenticated retained selection provider-neutral; preserves empty assistant final attempts; treats legacy diagnostic counters as unknown; makes body truncation conservatively incomplete; adds persisted graph tamper/ambiguity controls; and prevents the retained-only renderer from claiming a full body was read.

### RED evidence

- Controller actual CLI empty-final publication: `go test -overlay /tmp/session-reviewer-scan-chain-qa.6VBnCG/new-project-cli-overlay.json ./internal/cli -run '^TestControllerPublishedEmptyFinalAnswer$' -count=1` exited 1 because `empty final answer state=no_answer, want partial` (1.636s). The controller later extended this unchanged probe through source removal and rescan.
- Controller actual CLI redaction expansion: `TestControllerPublishedRedactionExpansionCoverage` exited 1 because `truncatedBodies>0` was emitted with `complete=true` (2.320s).
- Controller actual Go publication -> strict TypeScript -> renderer probe `/tmp/session-reviewer-scan-chain-qa.6VBnCG/controller-retained-copy.mjs` exited 1 with `{claimsFull:true,retainedMessages:19}` for retained messages whose `text` was null.
- New persisted successor tests initially reached their intended authentication boundary but exposed fixture defects: legacy returned `retained Session facts binding mismatch`; ambiguity returned `invalid generation manifest: retained SessionView duplicates current dependency`. The successor helper was corrected to pass the authenticated previous index and keep historical views reachable through chain proof rather than falsely listing them as current/retained Session identities.
- New mixed-provider fixture first failed before publication with `invalid source usage: invalid session usage duration`, then with `SessionView terminal state and source availability disagree`; fixture usage duration and availability were made internally consistent without changing production behavior.

### GREEN evidence

- Controller independently reported the combined real CLI `EmptyFinalAnswer` (including removal/rescan retained attempt), `RedactionExpansionCoverage`, and `RetainedConversationLifecycle` probes passing in 7.141s.
- Controller rebuilt the actual retained renderer fixture and reported `claimsFull:false` with 19 retained messages; actual four-state published Chrome checks also passed at 1200px and 390px, including source/removal/rescan/restore, no-CLI zero calls, Enter behavior, no overflow, and no console errors.
- `go test ./internal/inspect -run 'TestRetainedConversation(LegacyCoverageRemainsQueryableAsUnknown|TwoCompatiblePublishedRootsAreAmbiguous)$' -count=1 -v` passed both authenticated successor fixtures (5.787s). These use a private-store `CommitPublished` test proof, not real four-file publication.
- `TestRetainedConversationPublishedGraphTamperingFailsClosed` passed its positive control plus independent chain-document, evidence-view identity, selected active-revision object, and manifest-dependency tampering cases. The encompassing run took 13.798s; only the two then-unfixed successor fixtures failed in that run, and both later passed as recorded above.
- `go test ./internal/inspect -run 'TestRetainedConversationPublishedSameNativeIDAcrossProviders$' -count=1 -v` passed Codex and Claude with the same native Session ID through actual private-store publication/query, with the configured Codex source root pinned to an empty temporary directory (1.448s). This is a generic retained boundary test, not Claude source-full support.
- `go test ./internal/inspect -count=1` passed (49.385s).
- `go test ./internal/inspect ./internal/cli -run 'RetainedConversation|ConversationCursor|ConversationReadOnly' -count=1` passed (`internal/inspect` 99.453s; CLI 0.785s with no matching named tests).
- `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts tests/render-scan-records.test.ts` passed 3 files / 109 tests (7.05s).
- `npm run lint && npm run build` passed ESLint, TypeScript typecheck, and production esbuild.
- `git diff --check` exited 0.

### Frozen-gate concern

- One fresh `npm run check` was not clean even though all 27 files / 472 tests passed: Vitest reported an unhandled rejection after `tests/session-event-recovery.test.ts`, `TypeError: Cannot read properties of undefined (reading 'kind')` at `src/view/project-view.ts:138` in `ProjectEvolutionView.refresh`. This file and failure are outside Task 2; no unrelated edit or silent retry was made. The Task 2 focused tests, lint, typecheck, and production build are green. Controller owns diagnosis of that asynchronous test race and the final full-Go frozen gate.

### Fix-round files

- Production: `internal/inspect/conversation.go`, `internal/source/codex/visible.go`, conversation-page Go/TypeScript/schema validators, and `obsidian-plugin/src/view/render-conversation.ts`.
- Tests: `internal/inspect/conversation_test.go`, `conversation_retained_test.go`, `service_test.go`, and the conversation parser/renderer tests. The generalized inspect fixture preserves all former Codex-only callers and adds only provider identities plus an optional retained-chain hook for the persisted mixed-provider case.

## Fix round 2

The round-2 review found one remaining classification gap from finding 1 plus its still-missing cross-provider cursor regression. An available non-Codex source with neither a reader nor retained chain swallowed the typed unsupported capability and returned `source_unavailable`.

### RED

- Added an authenticated private-store publication with an available Claude source, no retained chain, and no persisted `visible_reader_unsupported` diagnostic.
- `go test ./internal/inspect -run 'TestConversationAvailableProviderWithoutReaderOrRetainedChainIsUnsupported|TestRetainedConversationPublishedSameNativeIDAcrossProviders' -count=1 -v` exited 1: `TestConversationAvailableProviderWithoutReaderOrRetainedChainIsUnsupported` received `authenticated source prefix is unavailable` instead of `visible_reader_unsupported`.
- The same run proved the new same-native-ID cursor coverage already worked: `TestRetainedConversationPublishedSameNativeIDAcrossProviders` passed after separating its index limit from selected-message pagination.

### Fix

- `LoadConversationPage` now remembers `ErrVisibleReaderUnsupported`, checks cancellation immediately after the optional reader call, and returns the typed public `visible_reader_unsupported` result only after no authenticated retained evidence was selected.
- Authenticated retained evidence remains preferred; corrupt retained graphs still fail closed; no non-Codex source reader or Codex-path fallback was added.
- The persisted mixed-provider fixture now creates two turns per provider. Each provider's real cursor succeeds in its own namespace, while submitting either cursor to the other provider with the same native Session ID returns `stale_cursor`.

### GREEN

- Focused provider tests: both passed (`ok internal/inspect 1.947s`).
- `go test ./internal/inspect -count=1`: passed (47.422s).
- `go vet ./internal/inspect ./internal/source/codex`: exited 0.
- `git diff --check`: exited 0.
- Controller-owned full Go suite is intentionally pending on the new frozen commit; the prior full suite on `d76c9e9` passed but predates this round-2 correction.

### Final scope and concerns

- Round 2 changes only the Go conversation classification, persisted inspect regressions, and this accumulated report.
- Provider-specific Claude/OpenCode source-full readers remain outside scope; only provider-neutral authenticated retained reads and typed absence of a reader are claimed.
