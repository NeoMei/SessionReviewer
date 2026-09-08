# Remaining spec implementation and GitHub release execution

User authorization2026-09-08: “做完剩下的任务，让代码发布到 github”. Implement all remaining applicable requirements, then integrate and publish a verified release. Do not stop after one bounded task. No release before the spec matrix is satisfied.

Baseline4565e69, branch `codex/spec-ui-restoration`, worktree `.worktrees/codex-v4-scan-display`. Main5c5cb7a retains two human review edits and an untracked brainstorm directory; preserve them. GitHub latest release read-only verified0.4.2; GitHub authentication available. Release version is selected after implementation against live remote tags; do not replace0.4.2 assets in place.

Authoritative spec is `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`; existing subsystem plans remain binding. `2026-09-07-spec-restoration.md` is the requirement matrix. User removed new migration and cross-platform expansion; existing regression/compatibility safeguards remain.

## Execution queue

User addition2026-09-08: repeat comprehensive task-completion audits until all applicable tasks are done; repeat code review/fixes until no known worthwhile defects remain; repeat backend/frontend/UI functional tests and fixes until no known worthwhile defects remain. This explicitly replaces a single final whole-branch pass with repeated final passes. Passing one layer does not close the others; report tested boundaries and do not claim absence of all possible bugs.

Final convergence protocol: after all functional tasks, run at least two comprehensive requirement/code/functional verification rounds. Any worthwhile finding is assigned, reproduced when applicable, fixed and rechecked; a clean earlier round is not carried across a material later fix. Independent reviewers use immutable code revisions. Requirement audit checks S01–S12 against the spec and actual entry points, not only plan checkboxes. Code passes cover evidence truth/privacy/authentication/concurrency first and source/CLI/UI integration and regressions second. Functional passes exercise real temporary-Vault scan→five tabs→detail/actions→rescan/reopen and degraded modes; browser fixtures supplement, never replace native acceptance. Final release requires no open worthwhile findings and green verification on the actual release revision. Ordinary scoped-review round caps do not authorize parking a worthwhile defect as complete under this latest user instruction.

- [ ] Recover current Codex tool envelopes and conservative exec wrappers; then bind visible Q/A and typed execution/verification into retained chains.
- [ ] Reproduce and correct aggregate-shell/no-execution command verification attribution before qualifying milestones (`2026-09-08-command-verification-attribution.md`); retain generic command outcomes.
- [x] Before mixed-provider chain/search integration, remove obsolete Codex-only restrictions from generic memory contracts with unchanged hash/location/identity safeguards (`2026-09-08-provider-neutral-memory-contracts.md`). Reviewed at01218cb+cf81296; this does not activate an adapter.
- [ ] Current-contract Claude/OpenCode adapter registration, authenticated source reads and provider parity (port reusable legacy Claude parser, not its obsolete architecture).
- [ ] Authenticate current and historical answer snapshots in public source refs and fixed-argv drilldown (`2026-09-08-historical-turn-bindings.md`); no relabeling a historical answer as the current view.
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

## Reviewed decoder completion

Literal-wrapper implementation `ad83c0c` and correction `33d4ccc` passed independent spec/quality review. The review found and verified three corrections: unresolved calls without output now contribute explicit unsupported coverage; body-only patch output cannot prove a terminal result; contradictory structured patch status/body stays unverified. Controller independently reran the three exact regressions at33d4ccc, exit0. Frozen plugin check at33d4ccc passed26files/381tests, lint, TypeScript and production build. Controller full Go suite atad83c0c passed all packages; the fix's affected source/scan/inspect/contextupdate and zero-token regressions passed separately.

Read-only actual first-Session diagnosis remains deliberately incomplete execution evidence:362literal-source wrappers have uniquely attributable two-block outputs, but191commands retained only stdout and171patches retained170emptyobjects plus1unrecognized body. None proves a child terminal outcome. This is genuine missing historical metadata, not grounds to infer success from wrapper completion. Ordinary scanning must retain explicit partial coverage while displaying captured visible answers and supported typed facts.

Provider-neutral contract implementation starts at33d4ccc. Source manager registration remains the capability boundary; removing a generic Codex literal does not constitute Claude/OpenCode support. No code was pushed or released; fresh GitHub check still lists0.4.2 as latest.

Projection integration audit: `RenderMarkdownUpdate` currently authenticates only scan-identity carry and cannot yet accept new machine milestones. Public `ChainDependency` currently permits one view per provider/Session, unlike the planned private current/historical roots. These are explicit remaining integration requirements: generated deltas must prove preservation of pending human fields, and historical references must remain bound to their exact accepted evidence, never silently relabeled or combined under a fabricated digest.

## Provider contract checkpoint

Generic provider correction01218cb passed full Go, scoped vet, full plugin26files/381tests and independent review after correctioncf81296 added previously missing SessionIndexMeasurement provider fixture coverage. Re-review approved spec and quality with no remaining findings. Controller independently ran the entire memory package atcf81296, exit0 (0.716s), and diff check passed. This closes only generic contract neutrality; production Claude/OpenCode readers and registration remain open. Retained pure materializer is the next active task, followed by immutable chain CAS and scan/query wiring. GitHub fresh read still lists0.4.2; no push or release has occurred.

## Retained builder review checkpoint

Implementationfe0ab0c preserves stable turn IDs, joins exact active typed evidence by source ordinal, retains bounded redacted excerpts and explicitly marks interrupted commentary partial. Focused conversation/inspect/Codex/privacy tests and scoped vet passed. Full Go functional packages passed, but Gate A failed on four new production dependency edges: conversationchain -> memory/redact/sort/time. The baseline remains unchanged pending independent task review; this is not a green full-suite gate.

Controller frozen plugin check atfe0ab0c passed26files/381tests, lint, TypeScript and production build; diff check clean. The shared sanitizer extraction did not break the Go-backed plugin wire fixture. Independent reviewer is checking both spec/quality and the concrete new dependency purposes. Persistence, scan wiring and product acceptance remain open.
