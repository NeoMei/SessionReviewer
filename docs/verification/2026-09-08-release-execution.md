# Remaining spec implementation and GitHub release execution

User authorization2026-09-08: “做完剩下的任务，让代码发布到 github”. Implement all remaining applicable requirements, then integrate and publish a verified release. Do not stop after one bounded task. No release before the spec matrix is satisfied.

Baseline4565e69, branch `codex/spec-ui-restoration`, worktree `.worktrees/codex-v4-scan-display`. Main5c5cb7a retains two human review edits and an untracked brainstorm directory; preserve them. GitHub latest release read-only verified0.4.2; GitHub authentication available. Release version is selected after implementation against live remote tags; do not replace0.4.2 assets in place.

Authoritative spec is `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`; existing subsystem plans remain binding. `2026-09-07-spec-restoration.md` is the requirement matrix. User removed new migration and cross-platform expansion; existing regression/compatibility safeguards remain.

## Execution queue

- [ ] Recover current Codex tool envelopes and conservative exec wrappers; then bind visible Q/A and typed execution/verification into retained chains.
- [ ] Before mixed-provider chain/search integration, remove obsolete Codex-only restrictions from generic memory contracts with unchanged hash/location/identity safeguards (`2026-09-08-provider-neutral-memory-contracts.md`). This does not activate an adapter.
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

Direct-tool task closed at8b156bd+b76dd12 after independent review found and verified correction of generic zero-exit patch-success fallback. Frozen plugincheck at8b156bd passed26files/381tests, lint/typecheck/build; controller repeated NativePatch/TerminalMetadata regression atb76dd12 exit0. Wrapper Task2 running fromb76dd12. Retained builder and immutable CAS plans are ready; generic provider validation is a newly identified prerequisite before their mixed-provider fixtures can execute.

## Read-only pricing preflight

On2026-09-08 the web reader could not open the API landing page; direct fixed HTTPS endpoint requests succeeded. HEAD models returned200/application-json and ETag; bounded models/history GETs both returned `{count,updated,data}`, count248 and actual248, updated2026-09-06. This verifies endpoint shape, not a production cache implementation.

Models listing keys include all previously known fields plus category, context_window, modality, tags, released, status, open_source, parameters, blended_cost_per_mtok, evidence. History values remain `{model,provider,history}`; history entry shape includes date, nullable input/output/cached_input rates, context_window,event,changes. Do not implement strict decoding against the old minimal fixture alone: it would reject the actual catalog. Use synthetic fixtures with this full recognized shape. No project/session metadata sent to the price service and no remote catalog persisted into the repo.

## Read-only provider preflight

Installed OpenCode executable resolves to `/Users/neomei/.npm-global/bin/opencode` (native package binary) and reports1.18.29. Its installed help supports `session list --format json --pure`. Exact bounded invocation with both isolated-worktree and real-project cwd returned status0, stdout0bytes and stderr0bytes. This is an incompatible/empty response, not a valid empty JSON list and not proof of zero Sessions. Preserve provider-local diagnostics and implement synthetic command-contract tests before revisiting native acceptance; no user configuration changes or database reads were made.

## Resumed controller verification

On2026-09-08, GitHub still lists0.4.2 as latest. No new release or push has occurred. Main's two review documents and brainstorm directory remain untouched; the resumed implementation continues on the existing linked worktree, not main.

Native Obsidian1.13.7 temporary Vault now contains the current plugin bundle: local and installed main.js both SHA256 `fd1897d5e90827a4bac26e5feeb04153b7980a57a5874179faa1ac69324ca1b4`. Controller observed actual search `session-001` selects the corresponding detail; clearing search preserves it on the last page; 首页 displays1–25/154 and selects session-154; 末页 displays151–154/154; selecting session-001 then visiting 问题脉络 and returning retains that selection and page. No bundle install or daily-Vault modification was performed during these checks. These are synthetic-index native navigation checks; authenticated real-source summary/Q&A remain unavailable in this fixture and are not accepted by this evidence.

New native finding: zero filtered rows correctly dispose the old detail but the right pane says “公开索引中没有 Session。” despite total154. This is inaccurate empty-state copy, not lost indexed data. Correct as a bounded follow-up during Session search/UI integration, with tests distinguishing genuinely empty index from no filter matches. Do not discard it as completed navigation acceptance.
