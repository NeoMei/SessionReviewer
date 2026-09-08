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
