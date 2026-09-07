# Task 1 implementation report: provider-neutral memory contracts

## Status

DONE. Independent controller review is pending before dependent tasks proceed.

## Implementation

- Replaced only the generic memory contract's obsolete `provider == "codex"` gates with the existing `safeIDPattern` in source identity, exact JSONL location, SessionView dependency, SessionLineage dependency, and associated-usage validation.
- Kept the sole v1 source location shape as discriminated `jsonl` with a present payload and bounded nonnegative coordinates. Updated the error copy to describe the generic private-schema shape.
- Replaced provider constants in the six specified JSON Schemas with their existing `safe_id` definition. No other schema property or response shape changed.
- Reconciled the old exact-JSONL tests so well-formed Claude is no longer categorized as invalid; all non-provider JSONL wire/discriminator/payload assertions remain, with explicit negative and oversized coordinate cases added.
- Added runtime and schema contract coverage for `codex`, `claude`, `opencode`, `custom-provider`, and the exact 128-byte accepted boundary. Empty, uppercase, slash, backslash, traversal, whitespace, non-ASCII, NUL, and 129-byte providers reject.
- Added namespaced graph tests proving equal native IDs under Codex and Claude remain separate dependency and usage rows, while same-provider duplicates reject.
- Added a real source-catalog persistence lookup test for equal native IDs under two providers.
- Added real canonical memorystore tests covering observations, SessionViews, SessionLineages, ProjectView, GenerationManifest, prepare, publish, read-only reopen, namespaced lookups, persisted usage counts, and retention reporting. Cross-provider SessionView substitution fails both preparation and prepared read; a SessionView summarized from another provider's observation chunk also fails graph preparation.

## TDD evidence

### RED

Command:

```text
go test ./internal/memory -run 'ProviderNeutral|ProviderContract' -count=1
```

Result: exit 1. Relevant failures were `unsupported provider "claude" for private schema v1`, the same runtime failure for `opencode`, `custom-provider`, and the valid 128-byte ID, plus schema failures `$.provider is not exact const codex`. The namespaced dependency test failed with `invalid SessionView dependency`. These were expected because the obsolete provider allowlist and schema constants were still present.

The real-boundary RED expansion also failed before implementation:

```text
go test ./internal/memory ./internal/memorystore ./internal/sourcecatalog -run 'ProviderNeutral|ProviderContract' -count=1
```

Result: exit 1. Memory failed at the obsolete runtime/schema gates, memorystore failed while putting the Claude canonical observation, and sourcecatalog failed while upserting the Claude source record.

### GREEN

- `go test ./internal/memory ./internal/memorystore ./internal/sourcecatalog -run 'ProviderNeutral|ProviderContract' -count=1` — exit 0; all three packages passed.
- `go test ./internal/memory ./internal/memorystore ./internal/sourcecatalog ./internal/sessionview ./internal/projectview ./internal/source -count=1` — exit 0; six packages passed, including memorystore in 36.768s.
- `go vet ./internal/memory ./internal/memorystore ./internal/sourcecatalog ./internal/sessionview ./internal/projectview ./internal/source` — exit 0, no output.
- `go test ./...` — exit 0; every Go package passed (slowest reported package `test/zerotoken` in 276.260s).
- `cd obsidian-plugin && npm run check` — exit 0; ESLint passed, Vitest reported 26 files / 381 tests passed, TypeScript check and production esbuild completed.
- `git diff --check` — exit 0, no output.

## Files changed

- `internal/memory/types.go`
- `internal/memory/types_test.go`
- `internal/memory/provider_contract_test.go`
- `internal/memorystore/provider_contract_test.go`
- `internal/sourcecatalog/provider_contract_test.go`
- `schemas/source-catalog-v1.schema.json`
- `schemas/observation-v1.schema.json`
- `schemas/session-view-v1.schema.json`
- `schemas/session-lineage-v1.schema.json`
- `schemas/project-view-v1.schema.json`
- `schemas/generation-manifest-v1.schema.json`
- `.superpowers/sdd/2026-09-08-provider-neutral-memory-contracts/task-1-report.md`

## Self-review

- Re-read the brief and global constraints line by line after GREEN.
- Confirmed no remaining `provider != "codex"` branch exists in `internal/memory/types.go` and no `"const": "codex"` remains in the six changed schemas.
- Confirmed runtime tests, not schema tests, retain cross-field provider equality negatives; schema negatives use malformed provider IDs and source-location shape.
- Confirmed all canonical self-identities are recomputed after provider changes in tests.
- Confirmed source adapter registration, source manager dispatch, conversation-query capability checks, public response shapes, network/model calls, migrations, real Vault state, and release paths were untouched.
- Mutation check: restoring any removed Codex-only branch breaks valid-provider tests; weakening safe-ID checks breaks malformed/boundary tests; removing provider from namespace keys breaks duplicate/distinct tests; weakening graph identity checks breaks substitution tests.

## Concerns

No implementation concern found. Acceptance of provider-shaped records proves only generic schema validity; Claude/OpenCode discovery, decoding, registration, visible reads, and parity remain explicitly out of scope. Independent review remains the controller's next gate.
