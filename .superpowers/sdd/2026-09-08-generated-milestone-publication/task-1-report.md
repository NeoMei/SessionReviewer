# Task 1 implementation report

## Status and implementation

DONE on base `a6303199d82ea83b7e5aefc2a988a87275e80c11`. Changes stayed within the exact Task 1 paths; no Task 2 scan/private-publication/UI code changed.

- Added `ScanMilestoneUpdate`, `RebaseMarkdownMilestones`, and `RenderMarkdownMilestoneUpdate`.
- The pending pair is authenticated with `ParseMarkdownDraft`. The generated input admits only reviewed machine milestone kinds, no decision/current-state/problem payload.
- Existing generated ownership requires machine identity plus all four valid scalar baseline/patch bindings. Prefix alone is insufficient and human/migrated ID collisions fail.
- Generated defaults for title, summary, conclusion, and impact/follow-up advance while active human patches remain. `restore_default`, `set`, `suppress`, `human_confirmed`, and `ai_candidate_confirmed` follow existing accepted semantics across repeated rescans.
- All old timeline entities and dependencies required by their references remain. Dependency capacity is checked on the de-duplicated union; conflicting same-snapshot content fails. Legacy refs are qualified only from their unique authenticated old dependency before a new ambiguous snapshot appears.
- Aggregate refs use canonical dependency-backed turn identity, avoiding duplicate qualified/legacy spellings. The existing `sourceTurnBindingIndex` is built once each for old/incoming/combined dependencies per rebase and passed to helpers.
- Arbitrary current state, graph, risk, loop, decision, or custom Markdown changes receive no new write authority. CRLF/comments/custom bytes survive the existing proven merge. Revision advances once for combined pending plus scan changes and remains stable for an accepted no-op.
- `RenderMarkdownMilestoneUpdate` recomputes the expected rebase, compares the complete presentation via canonical strict-JSON bytes with explicit encode errors, then calls the existing merge. No boolean escape or second parser was added.
- `RenderV4` accepts optional `MilestoneUpdate`, bypasses its legacy no-op only for that delta, uses the authenticated renderer on an existing baseline, and rejects initial/unproven mixed input.
- The existing frontmatter helper now also updates minimum reader/writer capability when historical ref qualification requires it.

## TDD evidence

Initial API RED:

```text
go test ./internal/reviewv4 ./internal/presentation -run 'MarkdownMilestone|GeneratedMilestone' -count=1
undefined: RebaseMarkdownMilestones
undefined: RenderMarkdownMilestoneUpdate
undefined: ScanMilestoneUpdate
```

Dependency-union RED:

```text
go test ./internal/reviewv4 -run '^TestMarkdownMilestoneDependencyCapacityCountsDistinctSnapshots$' -count=1 -v
overlapping dependencies consumed capacity twice: len=0 err=chain dependencies exceed array limit
```

Final GREEN covers a 32,769-entry exact overlap and a true 65,537-entry distinct-union overflow:

```text
--- PASS: TestMarkdownMilestoneDependencyCapacityCountsDistinctSnapshots (0.06s)
```

Reference semantic RED: `go test ./internal/reviewv4 -run 'RestoredConclusion|DeduplicatesQualified' -count=1 -v` passed restored conclusion but failed the equivalent qualified/legacy ref case with `markdown_format_invalid`. Root cause was public-spelling comparison instead of canonical binding identity. Both passed after using the existing canonical key, including conclusion `restore_default` after accept and a second rescan.

Long-session scale RED used 24 human-confirmed milestones and a 512-turn dependency:

```text
go test ./internal/reviewv4 -run '^TestMarkdownMilestoneUpdateIndexesLongAcceptedHistoryOncePerRebase$' -count=1 -v
long accepted history rebuilt dependency indexes per reference: allocations=69589
```

Passing existing indexes reduced the measurement to 21,736 allocations. The first 15,000 ceiling also counted fixed parser/clone work, so the regression ceiling was set at 30,000, which still distinguishes the old 69,589 behavior. Final GREEN:

```text
--- PASS: TestMarkdownMilestoneUpdateIndexesLongAcceptedHistoryOncePerRebase (0.04s)
```

## Focused result

```text
go test ./internal/reviewv4 ./internal/presentation -run 'MarkdownMilestone|GeneratedMilestone' -count=1 -v
```

Final result: exit 0. All 12 Task 1 reviewv4 tests and both RenderV4 tests passed (`reviewv4 0.641s`, `presentation 0.919s`). Log: `/tmp/session-reviewer-task1-final-focused.log`.

Coverage includes initial append; pending goal; exact CRLF/comment/custom-block preservation; complete-next/current-state tamper rejection; generated-region/ledger hash/project/baseline/patch/ownership/collision negatives; accepted and pending title/summary/conclusion/impact edits; later append; one revision; confirmed provenance/refs; restored defaults across accepts; historical dependency retention and qualification; canonical ref de-duplication; conflict/capacity/invalid-next/no-op/overflow behavior; RenderV4 integration and initial fail-closed input.

## Freeze history and gates

### Freeze 1

Controller-owned checks passed: `npm run check` exit 0 (27 files / 484 tests, lint, TypeScript, production build), external rebase overlay exit 0 in 0.948s, Go-to-TypeScript strict/tamper checks, and Chrome 1200/390 full/recent/keyboard/no-overflow/no-console checks.

The first owned `go test ./... -count=1` failed only `test/zerotoken/TestGateAZeroTokenCore`, reporting four new production reflection records: the `reflect` import and three `reflect.DeepEqual` uses. Every other package passed. Root cause was the prohibited reflection capability. The fix used explicit dependency equality plus complete strict-JSON presentation equality; no fields or encoding errors are ignored.

Targeted correction passed:

```text
go test ./internal/reviewv4 ./internal/presentation -run 'MarkdownMilestone|GeneratedMilestone' -count=1
go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1
```

Gate A passed all 154 entries.

### Freeze 2

Controller-owned checks at 10:21:58 passed: `npm run check` (27 files / 484 tests plus lint/typecheck/build), actual projector 0.539s, rebase overlay 0.442s, strict wire/tamper, and Chrome checks. The owned full Go run passed every package including `test/zerotoken 182.185s`. This freeze reopened only for the long-session index correction.

### Freeze 3 and final submitted diff

Controller-owned checks at 10:36:05 passed: `npm run check` (27 files / 484 tests plus lint/typecheck/build), actual projector 0.413s, complete rebase overlay 0.404s, strict wire/tamper, and Chrome 1200/390 mode/keyboard/layout/console checks. Controller reported no new finding.

Owned final full Go:

```text
go test ./... -count=1
```

Exit 0; 59 captured package lines, no `FAIL`; `internal/presentation 14.850s`, `internal/reviewv4 4.546s`, `test/zerotoken 187.506s`. Log: `/tmp/session-reviewer-task1-final-go-index.log`.

Owned final vet:

```text
go vet ./...
```

Exit 0, no output. Log: `/tmp/session-reviewer-task1-final-vet.log`.

`git diff --check`: exit 0, no output.

## Files changed

- `internal/reviewv4/markdown_milestone_update.go` (new)
- `internal/reviewv4/markdown_milestone_update_test.go` (new)
- `internal/reviewv4/markdown_render.go`
- `internal/presentation/render_v4.go`
- `internal/presentation/render_v4_test.go`
- `.superpowers/sdd/2026-09-08-generated-milestone-publication/task-1-report.md`

## Self-review and boundary

Re-read the Task 1 brief/context and final production diff after the last change. Confirmed the no-delta identity guard remains; the delta path derives and compares the complete expected presentation; dependency union/alias/index behavior is bounded; error paths return zero presentation or empty Markdown/plan before write construction; and no out-of-scope production file changed.

No known Task 1 correctness concern remains. This proves only the authenticated pure rebase/render seam. Real scan wiring, private-manifest authorization, atomic Task 2 publication, and final product acceptance remain downstream/controller-owned.
