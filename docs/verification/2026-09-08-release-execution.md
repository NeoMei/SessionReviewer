# Remaining spec implementation and GitHub release execution

User authorization2026-09-08: “做完剩下的任务，让代码发布到 github”. Implement all remaining applicable requirements, then integrate and publish a verified release. Do not stop after one bounded task. No release before the spec matrix is satisfied.

Baseline4565e69, branch `codex/spec-ui-restoration`, worktree `.worktrees/codex-v4-scan-display`. Main5c5cb7a retains two human review edits and an untracked brainstorm directory; preserve them. GitHub latest release read-only verified0.4.2; GitHub authentication available. Release version is selected after implementation against live remote tags; do not replace0.4.2 assets in place.

Authoritative spec is `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`; existing subsystem plans remain binding. `2026-09-07-spec-restoration.md` is the requirement matrix. User removed new migration and cross-platform expansion; existing regression/compatibility safeguards remain.

## Execution queue

- [ ] Recover current Codex tool envelopes and conservative exec wrappers; then bind visible Q/A and typed execution/verification into retained chains.
- [ ] Current-contract Claude/OpenCode adapter registration, authenticated source reads and provider parity (port reusable legacy Claude parser, not its obsolete architecture).
- [ ] Qualified milestone projection, five-part closure and authenticated answer/source/problem drilldown; preserve human changes.
- [ ] Problem candidate rules/private CAS store, child/sibling/merge/move/reorder commands, confirmed tree and pending drawer.
- [ ] Decisions/agreements create/edit, private optional candidates, explicit extract/status/cancel/confirm/ignore/restore; shared conclusion-candidate pipeline. Tests use controlled proposal adapters; no paid model call for routine checks.
- [ ] Authenticated branch/file/error Session search runtime, bounded paging and actual plugin integration.
- [ ] ModelPriceWatch validated24h cache/exact routing/historical snapshots/supplement runtime and full usage UI.
- [ ] Real candidate Obsidian Vault install: actual scanned sources, all five tabs, keyboard/state/project-switch/source-unavailable/no-CLI paths, human edit→rescan preservation and zero-Agent instrumentation.
- [ ] Full Go/plugin/race/reproducible-package gates, independent whole-branch review, no unadjudicated spec gaps.
- [ ] Merge without overwriting human main changes; push source and version tag; wait for GitHub CI and release; download/verify official assets/version/hashes and BRAT-compatible root assets.

## Preflight rulings

Ruling: New approval authorizes GitHub integration/release after all gates, superseding earlier no-publish boundaries only at the final release step — user explicitly requested it — cost if wrong is premature release, prevented by gating.

Ruling: Adapt obsolete plan filenames/interfaces to current v4 contracts; do not recreate already accepted features or downgrade contracts — avoids repeating prior spec drift — cost if wrong is local rework caught by per-task review.

Ruling: Recover direct tool metadata before semantic closure. Arbitrary JavaScript wrappers are not safely interpretable; unsupported dynamic forms retain explicit partial coverage rather than guessed execution evidence — protects verification truth — cost if wrong is incomplete coverage, reported and tested.

## Current checkpoint

Task1 of `2026-09-08-codex-tool-evidence-recovery.md` dispatched to `modern_tool_evidence` from85d0ed7. Focused baseline source/inspect/conversation tests passed. Decoder root cause confirmed: `function_call` is explicitly ignored, `exec` wrapper is unknown, terminal parser only scans `exit code: N`, and production registration still uses `codex-jsonl-v1`. Remaining queue stays open until fresh evidence is recorded.

Controller plugin check was attempted while Task1 production files were being edited. Its Go-backed wire test caught a transient `terminalOutput`/string compile mismatch (380 other tests passed), so this run is not a frozen-build gate or product regression verdict. Worker subsequently reported focused decoder GREEN; rerun the whole plugin check at the committed Task1 revision. Do not present an in-flight workspace test as accepted build evidence.

## Read-only pricing preflight

On2026-09-08 the web reader could not open the API landing page; direct fixed HTTPS endpoint requests succeeded. HEAD models returned200/application-json and ETag; bounded models/history GETs both returned `{count,updated,data}`, count248 and actual248, updated2026-09-06. This verifies endpoint shape, not a production cache implementation.

Models listing keys include all previously known fields plus category, context_window, modality, tags, released, status, open_source, parameters, blended_cost_per_mtok, evidence. History values remain `{model,provider,history}`; history entry shape includes date, nullable input/output/cached_input rates, context_window,event,changes. Do not implement strict decoding against the old minimal fixture alone: it would reject the actual catalog. Use synthetic fixtures with this full recognized shape. No project/session metadata sent to the price service and no remote catalog persisted into the repo.

## Read-only provider preflight

Installed OpenCode executable resolves to `/Users/neomei/.npm-global/bin/opencode` (native package binary) and reports1.18.29. Its installed help supports `session list --format json --pure`. Exact bounded invocation with both isolated-worktree and real-project cwd returned status0, stdout0bytes and stderr0bytes. This is an incompatible/empty response, not a valid empty JSON list and not proof of zero Sessions. Preserve provider-local diagnostics and implement synthetic command-contract tests before revisiting native acceptance; no user configuration changes or database reads were made.
