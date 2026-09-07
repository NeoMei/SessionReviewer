# Spec restoration acceptance ledger

## Authority and current state

User request: “去改好！” after rejecting 0.4.2 as non-compliant with the accepted UI spec.
Authority: `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`, existing five subsystem plans, and approved `problem-context-layout-v3.html` / `evolution-detail-closed-loop.html` prototypes.
The user explicitly removed cross-platform expansion and old-project migration from required new work; preserve existing compatibility, do not implement migrations for this repair. Re-scan remains the accepted path. All other requirements remain binding.

Baseline: `5c5cb7a`; clean isolated worktree `.worktrees/codex-v4-scan-display`, branch `codex/spec-ui-restoration`. Baseline plugin tests: 22 files, 322 tests pass, 2026-09-07. Main's human review files and brainstorm prototypes are untouched.

**Overall status: NOT COMPLETE. No merge, release, or completion claim until every applicable requirement below has evidence.** A completed subtask is not product acceptance. No fabricated milestones, problem parents, verification, or price values may fill an empty page.

## Binding acceptance matrix

| ID | Spec | Required outcome | Current evidence / gate |
|---|---|---|---|
| S01 | §3, §12.3.1 | Five top tabs in approved order; project header compact, risks/todos and coverage visible | Shell restored at 8c8614d; native five-tab navigation observed in authorized temporary Vault; d57b4e3 fixes reproduced narrow-pane clipping with clean review and native retest |
| S02 | §4, §13.3/13 | Qualified milestone list, full history, five-part closure; no raw-event homepage | Accepted timeline now has bounded full history and five-part detail; automatic qualification/closure pipeline missing; blocking |
| S03 | §4.1, §5.1, §13.14 | Visible answers plus execution/verification sources; bounded authenticated drilldown | Visible Codex Q/A implemented; full closure pipeline missing; blocking |
| S04 | §5, §13.15/16 | Actual question tree, focus path/children/related nodes, orthogonal states | Formal nodes render in approved three-pane geometry; full interactive graph/evidence workflow remains incomplete; blocking |
| S05 | §5.3, §18.3/5 | Deterministic pending recommendations, explicit child/sibling/merge/move/reorder CAS | Runtime and UI missing; blocking |
| S06 | §6, §18.2/5 | Decisions/agreements, manual create/edit, candidate extract/confirm/ignore/restore | Legacy view exists; v4 runtime incomplete; blocking |
| S07 | §7, §13.1/2/12 | Full index, filters, retained unavailable sources, summary/search/paged events | Partial scan browser exists; filters/search/integration incomplete; blocking |
| S08 | §8, §18.6 | ModelPriceWatch cache, exact routed price, immutable snapshots, honest full-width cards | Validated accounting/current snapshots render honestly; ModelPriceWatch cache/matching service and full workflow missing; blocking |
| S09 | §2, §13.4/5/14 | Zero Agent starts on scan/read/rule organization; explicit optional candidates only | Must rerun end-to-end instrumentation after integration |
| S10 | §12.3.7/8/11 | Native Obsidian keyboard, project switching, reload, state persistence, no CLI/network/source recovery | Must repeat on completed product, not 0.4.2 |
| S11 | §13.11 | Enabled providers use same index/drilldown/status experience | Multi-provider deep conversation recovery requires check; blocking |
| S12 | §9/10, §17.4 | Human preservation, generation/preimage/CAS integrity, private source content | Existing safeguards to retain and regression-test |

## Delivery rules

- UI-only shell restoration does not close S02–S12.
- Existing subsystem plans remain requirements; any outdated file names/interfaces are adapted to current code, never used to omit behavior.
- Save RED/GREEN evidence and independent review per task. Final whole-branch review plus actual Obsidian screenshots and action outcomes are separate gates.
- Model calls required for optional-candidate acceptance use controlled test adapters unless the user explicitly requests real model execution; do not spend user tokens as part of routine scanning.
- Do not auto-install into the real vault before candidate verification and backup; do not publish based on this repair authorization alone.

## Execution

1. Restore the v4 five-tab host and compact recovery header, retaining safe read boundaries and data-backed panel contracts.
2. Complete conversation evidence, qualified milestone projection and five-part closure.
3. Complete problem rules/store/structural CAS and approved tree/canvas/detail/drawer.
4. Finish full Session filters/summary/search/persistence and provider-neutral drilldown.
5. Complete decision/annotation commands and human confirmation UI.
6. Complete ModelPriceWatch pricing runtime and source-linked usage UI.
7. Run all code, regression, spec-matrix, independent-review and native acceptance gates; report exact remaining blockers before any release request.

## Retained summary checkpoint

- Backend summary runtime implemented at `ae742f4`; independent spec and code-quality review approved without findings. Full inspect/CLI tests and scoped vet passed in its task report; controller also verified a real retained-data read.
- Read-only query against the actual retained Session `codex / 01a06a33-fe42-77c3-b850-c4eeaa4c13fa`, generation `scan-4535e7a655a4f9d09395b30f55293de5`, succeeded on 2026-09-07. It returned 5 key operations and no captured phase/verification/error/unresolved entries, with accepted coverage 88 seen / 88 indexed. This is a real runtime read, not proof of complete conversation/execution capture.
- Root-cause follow-up for S02/S03/S09: the Codex machine-fact decoder currently excludes `function_call`/`function_call_output` and only dispatches `custom_tool_call`/`custom_tool_call_output`; its command exit-code grammar only recognizes `exit code: N`. These boundaries require fixture-backed investigation before milestone closure can claim current-host execution coverage. Do not treat zero summary verification entries as proof that no verification happened.
- S07 remains open: plugin summary wiring, filters, private search and native acceptance are not closed by this backend query.
- Controller full Go regression at code commit `ae742f4` (docs-only head `4faf02f`): `go test ./...` exited 0, including `test/zerotoken` (92.347s); `go vet ./...` and `go mod tidy -diff` exited 0 with no output. This is backend regression evidence, not a replacement for the still-open product matrix.
- Deeper real-data inspection found all5 current key-operation rows have empty text and are typed `artifact/session_started` bookkeeping. The authentication backend task passed its bounded review, but useful summary projection has a newly reproduced gap. `2026-09-07-summary-typed-fact-text.md` tracks the sequential correction; do not call these5 rows substantive operations or summary product acceptance.

## Summary UI implementation checkpoint

- `eabf81e` wires authenticated retained summaries into 全部 Sessions before Q/A and execution facts, with five compact disclosures, exact returned/total/omitted counts, and explicit missing-digest / missing-CLI / empty-excerpt states. Independent task review and test-only fix `c79b100` re-review are clean; this is not product acceptance.
- Controller independently ran `npm --prefix obsidian-plugin run check` against `eabf81e`: 25 files / 364 tests passed, lint, TypeScript and production build passed. Viewed desktop and 390-pixel component screenshots and read the keyboard/retry/stale-response harness; this evidence uses synthetic fixtures, not a real native Vault.
- Read-only metadata counting on the actual first Session segment found 1,893 `exec` wrappers: 1,428 mention `tools.exec_command` (1,300 single / 128 multiple), 449 mention `tools.apply_patch`, and 174 mention `tools.write_stdin`. Categories overlap. These counts are shape probes, not decoded execution evidence; the decoder currently ignores that wrapper name. The root-cause work must handle provenance and unknown results explicitly rather than treating wrapper completion or stdout prose as a child process exit code.
- Review follow-up: unavailable-source Q/A copy promises retained execution facts, but the UI currently blocks their loader. `2026-09-07-retained-event-access.md` tracks the capability correction with private no-write regression; source-backed Q/A remains disabled. This gap is not hidden by summary UI acceptance.

## Typed-fact summary correction checkpoint

- `ca17d16` excludes startup/cwd bookkeeping from key operations and uses closed neutral typed labels with explicit unknown/conflicting outcomes, bounded redacted excerpts and unchanged sources/coverage. Independent spec and quality review approved without findings.
- The real retained query was repeated by implementer and controller: rule `summary-typed-fact-text-v2`, key operations0/blank0, accepted coverage88/88 unchanged. No scan or stored-data change was performed; this does not recover unindexed commands or prove there were no operations/verifications.
- Implementation evidence: focused inspect tests, inspect+CLI tests, scoped vet/diff-check and `go test ./... -count=1` all exit0, including zero-token integration201.861s. Controller separately reran focused typed/bookkeeping tests (0.797s), inspected unchanged block caps/final recheck, and verified actual query output.
- At this checkpoint, main remained5c5cb7a with original dirty human documents/prototypes; installed CLI and NeoMei-Docs plugin hashes remained unchanged. Native activation was pending then; the following checkpoint supersedes that permission state.

## Authorized temporary native Vault checkpoint

- User explicitly allowed exiting Restricted Mode and enabling SessionReviewer only in `.superpowers/sdd/2026-09-07-v4-five-tab-restoration/native-ba19351/Vault`. This is not authorization to install into NeoMei-Docs, change its CLI binding, rescan, merge, push or release.
- Built f74ec14 and copied main.js/manifest.json/styles.css to that temporary Vault. Bundle SHA256 `84ab476aedcbdd121fac8dec31ce31255876a849f1d447b5d0b4234ebd42b2d9`; manifest remains development candidate0.4.2. Native Obsidian1.13.7 registered 打开项目脉络; temporary community-plugins.json confirms SessionReviewer enabled.
- Observed all five tabs and their contents, history last page2/2 with16 seeded milestones, earliest human history conclusion, seeded formal question/decision, Sessions154 with page1/7, search for session-001 and explicit selection of its detail, and usage2310tokens with cost待定. These are synthetic fixture navigation checks, not automatic milestone/closure production or a real scan.
- Native problem view exposed horizontal clipping: window984px, pane approximately584px, three minimum grid tracks remain active because CSS tests viewport width. Right question evidence is cut off. `2026-09-07-v4-pane-responsive.md` tracks a real-renderer narrow-pane regression and scoped repair; browser viewport-only checks had missed this defect.
- Filtering the Session list retains the previously selected detail until another row is clicked. This native evidence belongs to the already planned Session-filter/selection-state task; it is not fixed by the responsive repair.
- Temporary project `project-gate-b-test` is not authenticated in the installed CLI data root; the native UI accurately shows 同步状态验证失败 / 待私有验证·只读. Authenticated summary/Q&A/event drilldown, A-B-A project switching, real scan preservation and complete keyboard flow remain unverified. Do not change daily configuration to make a fixture pass.

## Native pane layout correction

- `d57b4e3` adds a named v4 inline-size container and scopes compact layouts to actual pane width. Legacy viewport rules and runtime data/selection/authentication are unchanged. The browser fixture resides in `obsidian-plugin/tests/fixtures` so existing typed lint applies without rule suppression.
- RED: real renderer, viewport1280/host580, failed with `detail escapes the shell on the right`. GREEN: host580/760 stacks tree→context→detail, host1200 preserves three columns, viewport390/host374 fits, wide→narrow→wide retains selected tab/node; nested Session/conversation/event and usage/header checks pass. Controller independently reran the committed script, exit0, screenshots `/var/folders/0n/49qgdd8x7kgcvh719fw743mh0000gn/T/session-reviewer-v4-pane-joz6qE`.
- Implementer fresh full `npm --prefix obsidian-plugin run check`: exit0, lint clean,25 files/364 tests passed, TypeScript and production build passed. Browser fixture checks page identity, meaningful content, overlay absence and console health; native console was not inspected.
- Temporary Vault's previous styles were backed up in this plan's ignored workspace. Installed candidate CSS SHA256 `f0c9a0d09b4ab27e9067ca6923d92f38f434c431968b395e419e2367ce14ad51`; main.js remains unchanged. Reloaded native Obsidian1.13.7 with only the read-only project pane open; Problems tab remained selected. The same984px window now stacks the three regions. Clicking the selected question then pressing Tab reached the native Markdown button and scrolled the complete detail into view: completion criterion, conclusion, answer state and source copy are readable without right clipping.
- Native coordinate scrolling intermittently returned `noWindowsAvailable`; AX click + keyboard Tab provided the actual successful navigation evidence. No native wheel-scrolling claim is made.
- Daily NeoMei-Docs main.js/styles.css and installed CLI hashes match the pre-repair values. This closes the reproduced clipping defect, not S02–S12 or full product acceptance. No main merge, GitHub push or release was performed.
- Independent task review of ee71034..d57b4e3: spec compliant, quality approved, no Critical/Important/Minor findings. Full-branch review remains a later gate after the other restoration tasks.
