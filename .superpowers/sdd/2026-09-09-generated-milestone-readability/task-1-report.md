# Task 1 Implementer Report

## Status

DONE

## Implemented

- Added a closed Chinese title map for every existing qualified milestone category.
- Replaced raw retained-excerpt rendering with exact revision-by-ID rendering for summaries, execution evidence, and verification evidence.
- Added closed kind/state labels, category-specific typed-field allowlists, neutral absent/mismatched-revision fallback, and existing UTF-8/redaction bounds.
- Kept qualification, stable IDs, timestamps, source references, answer text, baseline ownership, and no-op publication behavior unchanged.
- Removed the obsolete raw rendering helpers so there is only one projector path.
- Updated deliberately old English assertions in `milestone_evidence_test.go`; the controller explicitly allowed the same mechanical update in `milestone_test.go`, without weakening qualification behavior.
- Added regressions covering five qualified categories, question-only and contradictory verification, adversarial delimiters/multilingual/multiline/secret/path/long values, input-order determinism, missing/mismatched revisions, human-preserving generated-text rebase, CRLF/custom Markdown, identical no-op generation, missing source, and actual scan/readable rescan behavior.

## TDD Evidence

### RED

Command:

```text
go test ./internal/presentation -run 'MilestoneReadability' -count=1
```

Expected pre-implementation failures included:

```text
title="Machine-observed verification"
summary="verification (passed): component=package; exit_code=0; passed=true; failed=false"
```

All five qualified category cases failed on the old English titles/raw field assignments. The first adversarial draft also produced a fixture setup error because validated observation field values must be single-line. I corrected that test by keeping semicolon/equal/Chinese/path/secret/long UTF-8 in authenticated fields and placing multiline raw text in the excerpt; a separate direct formatter-boundary test covers multiline typed data without weakening production input validation. The rerun then failed only on intended old production output.

### GREEN

Command:

```text
go test ./internal/presentation -run 'MilestoneReadability' -count=1
```

Result:

```text
ok  github.com/neomei/SessionReviewer/internal/presentation  0.417s
```

## Verification

Focused projector and existing evidence assertions:

```text
go test ./internal/presentation -run 'MilestoneReadability|ClosureEvidence|ProjectMilestonesQualifiesOnlyClosedMachineEvidence|ProjectMilestonesSelectsLatestSameCategoryFactByInstantThenIdentity' -count=1
ok  github.com/neomei/SessionReviewer/internal/presentation  0.571s
```

Focused rebase and actual scan regressions:

```text
go test ./internal/reviewv4 -run 'MarkdownMilestoneReadability' -count=1
ok  github.com/neomei/SessionReviewer/internal/reviewv4  0.447s

go test ./internal/contextupdate -run '^TestRunPublishesQualifiedMilestoneAndKeepsIdenticalScanByteStable$' -count=1
ok  github.com/neomei/SessionReviewer/internal/contextupdate  6.763s
```

Required full package checks:

```text
go test ./internal/presentation ./internal/reviewv4 ./internal/contextupdate -count=1
ok  github.com/neomei/SessionReviewer/internal/presentation  12.173s
ok  github.com/neomei/SessionReviewer/internal/reviewv4  1.492s
ok  github.com/neomei/SessionReviewer/internal/contextupdate  10.750s
```

Static/diff checks:

```text
go vet ./internal/presentation ./internal/reviewv4 ./internal/contextupdate
exit 0, no output

git diff --check
exit 0, no output
```

Ordinary scan zero-Agent regression:

```text
go test ./test/zerotoken -run '^TestMarkdownV4OrdinaryScanPublishesMilestoneWithoutAgentStart$' -count=1
ok  github.com/neomei/SessionReviewer/test/zerotoken  2.403s
```

Controller-owned independent checks reported readable/privacy/identity success from the actual authenticated projector and preserved no-op/human-edit receipts from the publication overlay. The controller owns the final actual CLI and strict TypeScript/Chrome 1200/390 verification after this behavior-ready handoff.

## Files Changed

- `internal/presentation/milestone_readability.go`
- `internal/presentation/milestone_readability_test.go`
- `internal/presentation/milestone.go`
- `internal/presentation/milestone_evidence.go`
- `internal/presentation/milestone_evidence_test.go`
- `internal/presentation/milestone_test.go`
- `internal/reviewv4/markdown_milestone_update_test.go`
- `internal/contextupdate/milestones_test.go`
- `.superpowers/sdd/2026-09-09-generated-milestone-readability/task-1-report.md`

## Self-Review

- Confirmed the formatter consumes only the revision selected by the exact retained revision ID and never reparses `Excerpt` delimiters.
- Confirmed state display reuses the strict verification classifier; generic command exit 0 still does not qualify a milestone.
- Confirmed allowlists omit `tool_id`, `status`, `passed`, `failed`, file/remote hashes, and raw excerpts; useful `git_head` remains.
- Confirmed unknown kinds and absent/mismatched revisions remain neutral and do not display a supplied success state.
- Confirmed stable IDs, kinds, timestamps, source refs, counters, answer text, qualification categories, generated baseline hashes, and five-part closure order were not changed.
- Confirmed no dependency, migration, provider, UI, source-adapter, native install, push, merge, or release changes.

## Concerns

None. Command-qualified producer gaps and broader provider/native coverage remain outside this task, as specified.
