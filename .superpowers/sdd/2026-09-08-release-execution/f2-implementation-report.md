# F2 implementation report — usable problem operations

## Result

F2 now provides the complete human-confirmed problem workflow from the Obsidian view through fixed CLI contracts, authenticated Markdown mutation, atomic publication, and strict readback. Ordinary scans generate private deterministic candidates without starting an Agent. An optional Agent placement request has a separate explicit-consent lifecycle and can only replace a private candidate proposal; it cannot publish the formal graph.

## Commits

- `b7d2ecf` — initial root creation, candidate store, authenticated publication, presentation readback.
- `d5a22af` — child/sibling/root/merge/keep/dismiss/restore, move/reorder, human field edit, resolve/reopen, and the Obsidian problem workflow.
- `6e9fbbc` — scan-time deterministic candidate discovery/reconciliation and exact source-answer viewer lifecycle.
- `232ef4c` — exclude exact host wrappers and pure control/choice replies from deterministic problem discovery.
- `0300c0c` — bounded proposal-only Agent placement runtime, durable jobs, cancellation, dependency caching, candidate CAS, crash recovery, and empty-source merge fix.
- `ce7b9a1` — absolute `--data-dir` placement contracts and active-job lookup by candidate after UI reload.
- `c0f0cb8` — strict placement response codec and explicit request/status/cancel UI with disposal.

## Runnable lifecycle

The plugin reads the trusted presentation and private candidate list, preserving the exact `provider/session_id/turn_unit_id/session_view_digest` references. A candidate action sends fixed arguments and the current candidate revision, problem-map revision, and accepted review SHA. Create/edit/reorder text is sent through a bounded versioned stdin payload. The CLI authenticates the current Project/Vault mapping and accepted review, validates the full resulting graph, merges human Markdown, publishes through the existing journal/lock path, and returns the new revision and accepted SHA. The host refreshes from disk before showing success.

Available formal actions are:

- create an exact-text human candidate and confirm it as a root;
- confirm a pending candidate as a root, child, or sibling;
- merge all unique source-turn references into an existing node;
- keep, dismiss, or restore a private candidate;
- preview and confirm a subtree move with old/new paths;
- publish a complete sibling order;
- edit exact question, current conclusion, and completion criterion text;
- resolve or reopen workflow state without changing answer/execution verification state.

Ordinary scan reconciliation uses `deterministic-problem-rules-v2`, remains zero-token, preserves human and confirmed graph state, stales disappeared rule candidates, and retains an Agent result only while its exact dependency-bound candidate remains current.

Optional Agent placement commands are:

```text
problems placement request --project-id ID --candidate-id ID --expected-candidate-revision N --expected-problem-map-revision N --expected-generation-id ID [--data-dir ABS] --json
problems placement status --project-id ID (--job-id ID | --candidate-id ID) [--data-dir ABS] --json
problems placement cancel --project-id ID --job-id ID --expected-revision N [--data-dir ABS] --json
```

The UI first warns that the request consumes model usage and requires a second explicit click. The trusted host verifies the saved proposal-only Codex capability before reserving a durable job, launches a detached restricted/read-only worker, passes only the candidate question, exact source-turn identities, and current graph identity fields, and validates strict generic JSON output. The output updates the private candidate with `analysis_mode=agent_requested` and an exact job ID. The formal graph still requires a separate human confirmation. Active jobs are recovered by candidate after reload, status polling rejects older revisions, and cancellation uses the current job revision. No configured Agent produces an actionable configuration error before any job or model use.

## Verification

Targeted Go verification passed:

```text
go test ./internal/problemmap ./internal/cli -run 'Problem|Placement|MergeWithoutSources' -count=1
```

This covers the full graph lifecycle, stale CAS/no-write behavior, exact-text human fields, deterministic reconciliation, proposal schema and graph-bound IDs, durable reserve/claim/cache/cancel/recovery, identical-request reuse after candidate revision advancement, reload lookup, null active lookup, and the real empty-source merge regression.

Targeted plugin verification passed:

```text
npx vitest run tests/problem-placement.test.ts tests/v4-shell.test.ts
npx tsc --noEmit --skipLibCheck
```

The last run completed 34 tests across the two files. Coverage includes explicit Agent consent, exact-revision cancel, strict response identity, active-job reload, poll disposal, candidate-only mounting, source-view disposal, and the existing problem action UI.

Controller native acceptance on the real `f1-native/green-5` project confirmed root creation/readback, edit/close/reopen/rescan preservation, deterministic candidates without Agent use, historical noisy candidates becoming stale, and the formerly failing empty-source merge succeeding without adding a duplicate formal node. Controller owns the final integrated plugin build and optional Agent-free native disabled-state check.

## Integration boundary

The F2 modules expose `ProblemPlacementActions` with `start(candidate)`, `status(jobId)`, `cancel(jobId, revision)`, and optional `latest(candidate)`. `renderV4Problems` accepts this as `actions.agentPlacement` plus `agentPlacementCompleted` for host refresh. The controller owns the final `CliRunner`, presentation, project-view, and shell wiring because those files also contain concurrent F3/F4/F5 work.

## F5 supplement: open the original Session

Commit `b07fdda` implements the approved native Session launch boundary. The independent modules are `internal/sessionlaunch`, `internal/cli/session_contracts.go`, `internal/cli/sessions.go`, `internal/inspect/session_identity.go`, `obsidian-plugin/src/cli/session-launch.ts`, and `obsidian-plugin/src/view/session-launch.ts`. The controller owns the runner and project-view integration.

The public lifecycle is:

```text
sessions launcher verify --provider codex|claude|opencode --executable ABS [--data-dir ABS] --json
sessions open --project-id ID --provider ID --session-id ID --expected-generation-id ID --expected-session-view-digest DIGEST [--data-dir ABS] --json
```

Verification runs only the selected executable's fixed `--version` route, records its physical identity and SHA-256 in a private 0600 configuration, and detects replacement. Open authenticates the exact current published generation and Session-view digest, pins the physical configured Project root, and writes a two-minute one-use envelope under the macOS user cache. The Terminal bootstrap contains only the absolute SessionReviewer executable and random token. It contains no Project ID, Session ID, source identity, Project root, or provider executable. The worker atomically claims the token, rechecks SessionReviewer and provider executable identity/SHA, reauthenticates the published Session and Project mapping, requires an interactive TTY, changes directory through the authenticated Project directory descriptor, removes inherited Session source variables, and executes one fixed provider route:

```text
codex resume SESSION_ID
claude --resume SESSION_ID
opencode PROJECT_ROOT --session SESSION_ID
```

The UI requires a prepare click followed by a separate confirm click and states that opening does not send a message. The response state is `launch_requested`: it means macOS accepted the Terminal request, not that the provider resumed successfully. Windows returns explicit unsupported behavior and has a compile gate; no portable Windows terminal integration is claimed.

Focused verification passed:

```text
go test ./internal/sessionlaunch ./internal/inspect ./internal/syncproject ./internal/cli -run 'Session|Launcher|MappingPin|RunSessions|RunSessionWorker' -count=1
go vet ./internal/sessionlaunch ./internal/inspect ./internal/syncproject ./internal/cli
GOOS=windows GOARCH=amd64 go test -c ./internal/sessionlaunch
GOOS=windows GOARCH=amd64 go test -c ./internal/cli
npx vitest run tests/session-launch.test.ts tests/session-launch-integration.test.ts tests/evolution-answer.test.ts
npx tsc --noEmit --skipLibCheck
```

The related TypeScript run completed 19/19 tests. It verifies strict reply identity, fixed shell-free argv, disposal of late UI results, all-Sessions integration, and historical-answer behavior: the Q&A reader keeps its historical view digest while native launch authenticates the same Session against its unique current index digest.

Controller native acceptance used a dedicated content-free Session fixture and fake provider through the production Obsidian UI and production CLI build. `f5-launch-native/terminal-proof.json` records the exact `resume` argument, exact controlled Project working directory, `stdin_is_tty=true`, and absent `CODEX_THREAD_ID`/source-root values. This proves the UI-to-real-Terminal transport without opening any historical user Session or invoking a model. A real Codex/Claude/OpenCode client resume remains a separate release acceptance gate.
