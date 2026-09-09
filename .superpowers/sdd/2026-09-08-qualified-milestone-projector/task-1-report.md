# Task 1 implementation report: qualified milestone projector

## Status

DONE_WITH_CONCERNS. The scoped pure projector is implemented and tested in the four authorized `internal/presentation` files. It is not wired into scan, publication, Markdown rebase, or UI.

## Implemented

- Added the declared `MilestoneSessionInput`, `MilestoneInput`, `MilestoneProjection`, and `ProjectMilestones` API.
- Authenticates every supplied Session triple independently with `conversationchain.ValidateRetainedEvidence`, plus an explicit canonical self-digest comparison because the authenticator validates digest shape but does not recompute `Document.Digest`.
- Rejects duplicate selected `(provider, session_id)` snapshots, foreign project bindings, inactive/substituted revisions, malformed metadata, omitted in-turn qualifying active facts, and every capacity overflow without partial success.
- Qualifies only supported passed verification and typed commit/release/deployment/version facts. Generic commands, file changes, failed/unknown/conflicting verification, command failure, and prose do not qualify.
- Emits one milestone per qualifying turn. `Timeline.Kind` is the controller-approved machine vocabulary `machine_verification|machine_commit|machine_release|machine_deployment|machine_version`; strongest-category priority is release, deployment, version, commit, verification.
- Stable ID hashes the fixed `qualified-turn-v1` generator family with project/provider/Session/turn identity. It excludes generation, displayed kind, timestamps, and free text, so adding stronger evidence keeps the same ID.
- Sorts by parsed RFC3339Nano instant then stable ID while preserving the exact source timestamp string. Equal-instant strongest facts use sequence then revision ID.
- Emits the existing five-part closure: retained trigger; last nonempty bounded visible answer or `no_visible_answer`; bounded typed execution or `no_execution_evidence`; contradiction-aware typed verification or `not_verified`; empty `not_captured` impact/follow-up. Partial/truncated coverage remains explicit.
- Re-redacts secrets and absolute paths before byte-bounded output. Raw observation/tool excerpts and hidden-role canaries never enter the result.
- Returns exact snapshot-qualified source references and only dependencies used by emitted milestones. Each included dependency retains the input chain's exact dependency digest and complete ordered turn-ID set.
- Counts supported qualifying facts before the first captured user as unassigned and never attaches them to a later question.
- Builds active-revision lookup once per Session, avoiding quadratic verification lookup across thousands of qualifying turns.

## TDD evidence

### RED 1: missing interface

Command:

`go test ./internal/presentation -run 'Milestone|ClosureEvidence' -count=1`

Exit 1 at compile time with `undefined: ProjectMilestones`, `undefined: MilestoneInput`, `undefined: MilestoneSessionInput`, and `undefined: MilestoneProjection`. This is interface-compilation RED, not executed behavioral RED.

### RED 2: executable behavior

After adding only the declared API shell returning an empty projection, the same command exited 1 with concrete assertions including `verified turn missing` and missing qualified verification/commit turns. This proved the production-built fixtures executed and failed because behavior was absent.

### RED 3: review-found chronology and contradiction defects

Repository regressions reproduced both controller findings before their fixes:

- Fractional chronology emitted `2026-09-08T01:00:02.9Z` before the earlier `2026-09-08T01:00:02Z`; an equal-instant alternate spelling also defeated sequence selection.
- A commit-qualified turn containing verification `Outcome=passed`, `exit_code=1`, `failed=true` rendered `verification (passed)`.

Command:

`go test ./internal/presentation -run 'TestProjectMilestones(SortsFractional|SelectsLatest)|TestClosureEvidenceDoesNotRenderContradictory' -count=1`

Exit 1 with all three intended behavioral failures. The amended command then passed.

## GREEN and gates

- `go test ./internal/presentation -run 'Milestone|ClosureEvidence' -count=1` — PASS, 13.261s after final lookup refactor; includes the 65,537-milestone fail-closed fixture.
- `go test ./internal/presentation ./internal/conversationchain ./internal/reviewv4 -count=1` — PASS: 13.296s, 0.717s, 2.029s after final lookup refactor.
- `go vet ./internal/presentation ./internal/conversationchain ./internal/reviewv4` — PASS after final lookup refactor.
- Scoped `git diff --check` — PASS after final lookup refactor.
- Fully captured `go test ./...` — PASS at amended semantic freeze, terminal session `75860`, explicit `FINAL_FULL_GO_EXIT=0`; log `/tmp/session-reviewer-qualified-milestone-final-full-go.log`, final package `test/zerotoken` PASS 295.189s. A later semantics-preserving refactor moved the exact revision map from per-turn to per-Session; the affected focused/package/vet/diff gates above were rerun. Per controller ruling, the final whole-branch gate will cover the eventual integrated candidate rather than rerunning every unchanged package for this allocation-only amendment.
- Earlier full suite attempt listed all packages passing but its wrapper exited after completion because zsh reserves `status`; it is not counted as a successful gate.
- Controller amended-freeze probes, session `46389`: chronology/conflicting-closure overlay PASS 0.678s; identity/hash/omission/privacy/stable-turn overlay PASS 0.424s; actual Codex decode through visible-source, retained materialization, and projector PASS 0.529s. These are supporting integration evidence, not repository-owned TDD.
- Controller plugin gate: `npm run check` PASS, 27 files / 484 tests plus lint, types, and build. This task changed no plugin files.

## Files changed

- `internal/presentation/milestone.go`
- `internal/presentation/milestone_test.go`
- `internal/presentation/milestone_evidence.go`
- `internal/presentation/milestone_evidence_test.go`
- This report only.

## Self-review

- Corrected the initial long-chain test to require the dependency's complete chain turn set rather than only milestone-referenced turns.
- Added validation proving an unqualified/unreferenced malformed Session cannot be skipped after another Session already produced output.
- Added real Codex producer-shaped verification fields (`status=test`, `exit_code=0`, `passed=true`, `failed=false`) and the boolean-conflict negative.
- Added syntactically valid false chain-digest and canonically rehashed omitted-active-result regressions.
- Replaced lexical timestamp ordering with parsed-instant ordering and preserved exact source timestamp spelling.
- Reconciled public verification display against the exact active revision so contradictory retained `passed` state cannot produce a passed claim.
- Removed the per-qualifying-turn active revision map allocation; lookup is now constructed once per Session.
- No legacy projector, wire/schema, scan, publication, UI, Vault, external source, model, network, or release code changed.

## Concerns and mandatory follow-up

- No authenticated typed rollback source/retained-chain shape exists. `thread_rolled_back` is conversation bookkeeping, not project rollback proof. This projector therefore cannot implement typed rollback qualification without a separately reviewed producer plus retained-policy contract; it deliberately does not infer rollback from command text or prose.
- Current Codex production decoding emits the supported verification shape but does not emit commit/release/deployment/version typed facts. Those pure/retained categories are covered by repository synthetic contracts, not claimed as current real-provider coverage. Producer coverage remains a separate mandatory gate.
- This task does not close S02/S03 and proves no scan/publication/rebase/UI/Vault/release behavior.

## Review fix round 1

Independent review of `6749246` found that `milestoneVerificationPassed` ignored an explicitly present `passed` field. Consequently, `Outcome=passed`, `exit_code=0`, `failed=false` incorrectly qualified when `passed` was `false`, zero, malformed, or noncanonical; a turn independently qualified by a commit also rendered that contradictory verification as passed.

### TDD RED

Command:

`go test ./internal/presentation -run 'TestProjectMilestonesQualifiesOnlyClosedMachineEvidence|TestClosureEvidenceDoesNotRenderContradictoryVerificationAsPassed' -count=1`

Exit 1. The false, zero, malformed (`many`), and noncanonical (`01`) cases each reported `fact counts=1/0 want=0/0`; the commit-qualified false case rendered `verification (passed): ... passed=false`. This reproduced the reviewer finding with repository-owned fixtures.

### Fix and GREEN

- Added a closed positive-evidence parser: when `passed` is present, only exact `true` or a canonical unsigned decimal count greater than zero is accepted. An absent `passed` field remains compatible with the prior typed outcome/exit/failure contract.
- The same predicate feeds closure display, so explicit-invalid `passed` values render `conflict`, never `passed`, when another typed fact independently qualifies the turn.
- Added positives for exact `true`, canonical count `12`, and absent-field compatibility, plus negatives for `false`, `0`, `many`, and `01`.

Verification after the fix:

- Targeted command above — PASS, 0.406s.
- `go test ./internal/presentation -run 'Milestone|ClosureEvidence' -count=1` — PASS, 13.015s including capacity.
- `go test ./internal/presentation ./internal/conversationchain ./internal/reviewv4 -count=1` — PASS: 13.794s, 0.545s, 2.078s.
- `go vet ./internal/presentation ./internal/conversationchain ./internal/reviewv4` — PASS.
- Scoped `git diff --check` — PASS.
- Per controller scope, unchanged full Go and plugin suites were not rerun; final whole-branch gates remain mandatory.

Self-review: the change is confined to the existing verification predicate and its two covering test files. No retained, scan, publication, wire/schema, UI, producer, or capability baseline changed. Existing rollback and provider-production concerns above remain unchanged.
