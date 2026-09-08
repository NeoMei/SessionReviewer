# Task 1 report: summary integrity

Status: implemented and locally verified on `codex/spec-ui-restoration` from base `25262c6c26fd969408df4e8a047ff2a57dc0cc66`.

## Scope delivered

- Recovery now accepts a link only when both predecessor and successor have the required result from `summaryTypedOutcome(fact, summaryTextMapping(fact))`; contradictory, unknown, unsupported, and started facts cannot validate recovery.
- Summary rule version is `summary-typed-fact-text-v3`; exact recovery identity, order, record digest/subject/fields, error evidence, and unresolved evidence remain enforced.
- Go and TypeScript require each displayed entry to have 1..64 unique source revision IDs.
- All five normal/error blocks require projection coverage `seen=total`, `indexed=shown`, `unprojected=omitted`, with `collapsed=undecodable=truncated=0`.
- Empty blocks, valid 40/32/8 capped blocks, a phase record omitted because it has 65 sources, and independent nonzero top-level source gaps remain valid.
- The existing plugin populated-summary test helper was corrected from `collapsed:8` to the producer's `unprojected:8`; no canonical JSON fixture bytes changed.
- The existing Go schema harness gained `uniqueItems` support because the tightened schema uses that standard keyword; no new validation engine or dependency was added.
- CLI runner coverage proves malformed summaries surface only the bounded localized `summary_failed` response.

## TDD evidence

- RED: `go test ./internal/inspect -run 'Summary.*(Recovery|Integrity|Coverage|Source)' -count=1` failed on conflicting recovery, five empty-source blocks, and all projection-coverage mutations.
- RED: `go test ./internal/memory -run '^TestSessionSummarySchemaRequiresNonemptyUniqueSources$' -count=1` failed because the schema accepted empty sources.
- RED: `npm --prefix obsidian-plugin test -- contracts-v4.test.ts` had its valid control pass, then failed because an empty source array was accepted.
- RED: `npm --prefix obsidian-plugin test -- cli.test.ts -t 'invalid summary block'` resolved instead of returning the bounded error.
- GREEN: focused Go inspect and schema-harness tests passed; `contracts-v4.test.ts cli.test.ts` passed 144/144.

## Frozen verification

- `go test ./internal/inspect ./internal/cli -count=1`: inspect passed in 26.992s; CLI was re-run alone to retain its final result and passed in 60.742s.
- `go test ./... -count=1`: exit 0; every package passed (longest: `internal/scan` 388.776s).
- `npm --prefix obsidian-plugin run check`: exit 0; lint, 27 files / 437 tests, typecheck, and production build passed.
- `go vet ./internal/inspect ./internal/cli ./internal/memory`: exit 0.
- `git diff --check`: exit 0.
- Controller independently rebuilt and passed the exact conflict-recovery probe and actual TS parser probe with `acceptedInvalidSummary: []`.

## Boundary

This task does not implement or certify milestone/source/provider workflows, migration, release, installation, production, or native-client acceptance. The unrelated `docs/verification/2026-09-08-release-execution.md` working-tree edit is not included.

## Round 1 review correction

- Independent review found the old canonical-order negative case used empty `source_revision_ids`, so entry validation rejected it before the intended sort check.
- The test-only correction gives both entries valid nonempty sources, proves the ordered positive control is accepted, then reverses them and requires the exact canonical-order error for both a normal block and the error block.
- Focused command: `go test ./internal/inspect -run 'TestValidateSummary(RejectsInvalidItemsRulesAndSort|RejectsNonCanonicalOrderAfterValidEntryChecks)$' -count=1` passed.
