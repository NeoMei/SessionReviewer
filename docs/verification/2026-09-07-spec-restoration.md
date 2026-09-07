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
| S01 | §3, §12.3.1 | Five top tabs in approved order; project header compact, risks/todos and coverage visible | Missing v4 shell; blocking |
| S02 | §4, §13.3/13 | Qualified milestone list, full history, five-part closure; no raw-event homepage | Current v4 only last milestone; blocking |
| S03 | §4.1, §5.1, §13.14 | Visible answers plus execution/verification sources; bounded authenticated drilldown | Visible Codex Q/A implemented; full closure pipeline missing; blocking |
| S04 | §5, §13.15/16 | Actual question tree, focus path/children/related nodes, orthogonal states | Contracts only; blocking |
| S05 | §5.3, §18.3/5 | Deterministic pending recommendations, explicit child/sibling/merge/move/reorder CAS | Runtime and UI missing; blocking |
| S06 | §6, §18.2/5 | Decisions/agreements, manual create/edit, candidate extract/confirm/ignore/restore | Legacy view exists; v4 runtime incomplete; blocking |
| S07 | §7, §13.1/2/12 | Full index, filters, retained unavailable sources, summary/search/paged events | Partial scan browser exists; filters/search/integration incomplete; blocking |
| S08 | §8, §18.6 | ModelPriceWatch cache, exact routed price, immutable snapshots, honest full-width cards | Price types exist; service/UI incomplete; blocking |
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
