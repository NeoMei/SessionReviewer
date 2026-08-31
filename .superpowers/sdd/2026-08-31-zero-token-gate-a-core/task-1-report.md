# Gate A Task 1 Report: Freeze the Private Memory Contracts

## Status

DONE. Task 1 is implemented and verified on branch `codex/release-0.2.8`. No subagents or external reviewers were used.

## Implementation summary

- Added provider-neutral private-memory records for the content-free SourceCatalog, immutable observed revisions, SessionView, ProjectProbeState, ProbeCheck, ProjectView, and generation manifests.
- Kept `ObservationRevision` exactly at the observed-fact boundary: stable key, immutable revision ID, authenticated source reference/hash, typed fields, bounded excerpt, and adapter provenance. Semantic-only kinds and raw-conversation field names are rejected.
- Stored one validated `accounting.SessionUsage` in each SourceRecord, with an authenticated frozen boundary and explicit source availability. Project-associated usage refers to that catalog record by digest rather than copying its full usage payload.
- Added deterministic `Digest` and `ObservationRevisionID`. Digesting uses a normalized defensive JSON copy, canonical map encoding, sorting only semantically unordered collections, preservation of ordered SessionView dependencies, invalid UTF-8 rejection, and NaN/Inf rejection.
- Added strict validation for schema version 1, RFC3339Nano timestamps, lowercase SHA-256 values, bounded strings/arrays, safe IDs, duplicate IDs, terminal-count reconciliation, exact frozen-source-to-SessionView reconciliation, and active/inactive revision lineage conflicts.
- Added four strict Draft 2020-12 JSON schemas. Every machine-owned object is closed with `additionalProperties: false`; constrained maps use `patternProperties` plus `additionalProperties: false`.

## Files

- `internal/memory/types.go`
- `internal/memory/digest.go`
- `internal/memory/types_test.go`
- `internal/memory/digest_test.go`
- `schemas/source-catalog-v1.schema.json`
- `schemas/observation-v1.schema.json`
- `schemas/session-view-v1.schema.json`
- `schemas/project-view-v1.schema.json`
- `.superpowers/sdd/2026-08-31-zero-token-gate-a-core/task-1-report.md`

## RED evidence

### Initial contract RED

Command:

```text
go test ./internal/memory -count=1
```

Output (exit 1):

```text
# github.com/neomei/SessionReviewer/internal/memory [github.com/neomei/SessionReviewer/internal/memory.test]
internal/memory/types_test.go:173:26: undefined: SourceRecord
internal/memory/types_test.go:201:28: undefined: ObservationKey
internal/memory/types_test.go:205:92: undefined: ObservationRevision
internal/memory/types_test.go:223:27: undefined: DerivedRecord
internal/memory/types_test.go:236:25: undefined: SessionView
internal/memory/types_test.go:257:24: undefined: ProjectProbeState
internal/memory/types_test.go:274:24: undefined: ProbeCheck
internal/memory/types_test.go:278:25: undefined: ProjectView
internal/memory/types_test.go:300:32: undefined: GenerationManifest
FAIL github.com/neomei/SessionReviewer/internal/memory [build failed]
FAIL
```

Reason: the tests referenced the required private-memory API before any production type or hashing implementation existed.

### Reconciliation and unordered-collection RED

Command:

```text
go test ./internal/memory -run 'Test(DigestNormalizesSemanticallyUnorderedProjectCollections|ValidationRejectsSemanticRawDuplicateAndImpossibleContracts|GenerationManifestReconcilesFrozenSourcesWithSessionViews)$' -count=1
```

Output (exit 1):

```text
--- FAIL: TestDigestNormalizesSemanticallyUnorderedProjectCollections
    unordered associated usage changed digest
--- FAIL: TestValidationRejectsSemanticRawDuplicateAndImpossibleContracts/terminal_session_without_SessionView_dependency
    error = <nil>, want "SessionView dependency count" rejection
--- FAIL: TestGenerationManifestReconcilesFrozenSourcesWithSessionViews
    unmaterialized frozen source accepted or misclassified: <nil>
FAIL
```

Reason: the first implementation did not yet normalize associated-usage rows or require exact SessionView coverage for every terminal/frozen source. The implementation was then minimally tightened.

### Cross-collection duplicate-ID RED

Command:

```text
go test ./internal/memory -run 'TestValidationRejectsSemanticRawDuplicateAndImpossibleContracts/duplicate_derived_record_across_project_collections' -count=1
```

Output (exit 1):

```text
--- FAIL: TestValidationRejectsSemanticRawDuplicateAndImpossibleContracts
    --- FAIL: TestValidationRejectsSemanticRawDuplicateAndImpossibleContracts/duplicate_derived_record_across_project_collections
        error = <nil>, want "duplicate" rejection
FAIL
```

Reason: `witnessed_state` and `derived_records` were individually unique but did not initially share one ProjectView-level ID namespace. The validator now rejects duplicates across both collections.

## GREEN evidence

Brief command:

```text
gofmt -w internal/memory && go test ./internal/memory -count=1
```

Output (exit 0):

```text
ok  github.com/neomei/SessionReviewer/internal/memory  0.180s
```

Broader relevant package command:

```text
go test ./internal/accounting ./internal/memory -count=1
```

Output (exit 0):

```text
ok  github.com/neomei/SessionReviewer/internal/accounting  0.414s
ok  github.com/neomei/SessionReviewer/internal/memory      0.550s
```

Static checks:

```text
go vet ./internal/memory
```

Output: no diagnostics; exit 0.

The four schemas were parsed with `jq`; a recursive audit returned `true` for every schema, proving every declared object has `additionalProperties: false`. A forbidden-property declaration scan returned no matches.

Repository regression command:

```text
go test ./... -count=1
```

Output: exit 0. All Go packages passed, including `internal/apply`, `internal/cli`, `internal/memory`, `internal/prepare`, `internal/reviewjob`, `internal/reviewv2`, `internal/sync`, `skill/session-reviewer/tests`, and `test/reviewjob`; packages without tests were reported as such.

## Self-review

- Re-read the task brief and only the concrete design sections needed for SourceCatalog ownership, observation supersession, SessionView/ProjectView dependencies, and ProbeState versus ProbeCheck time separation.
- Verified the exact required Observation types/tags and exact terminal-state values.
- Confirmed ObservationStore records expose no rationale, intent, complete transcript, or complete tool-output field, while View records carry dependency-bound deterministic results.
- Confirmed `ProjectProbeState` has no `CheckedAt`; `ProbeCheck` owns the UTC check timestamp.
- Confirmed `SessionViewDependencies` remain ordered in hashing, while project IDs, revision dependency sets, probe file sets, remote hashes, and associated-usage rows normalize as unordered collections.
- Added exact source/session terminal reconciliation and cross-collection derived-record duplicate detection found during review.
- Removed one unused private sorting helper.
- Confirmed `git diff --check` was clean before final staging and no unrelated tracked changes were present.

## Concerns

None blocking for Task 1. Persistence, adapter decoding, materialization, probing, reduction, and generation publication remain intentionally outside this task and must consume these frozen contracts without moving semantic conclusions into ObservationStore or duplicating SourceCatalog usage.
