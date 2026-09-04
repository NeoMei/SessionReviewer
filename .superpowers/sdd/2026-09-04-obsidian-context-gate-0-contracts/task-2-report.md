# Task 2 report: strict Go v4 wire types and validators

## Status

Complete on `codex/obsidian-context-v4`. The eight Task 1 schemas and fixtures were not modified.

## Replacement and TDD evidence

The replacement began from commit `04cfc2e`, whose focused tests passed despite a shallow implementation. I added behavior tests before rewriting the affected paths. The expanded RED run is recorded in `evidence/task-2-red-expanded.txt` and failed for the intended missing behavior:

- `Accepted.SessionIndex` was untyped and failed the compile-time field probe.
- Session index accepted an unknown state reason, malformed summary digest, and oversized generation references.
- Inspect accepted malformed source revision IDs and unknown event kinds.
- Annotation accepted invalid candidate/confirmed-decision relationships.
- Pricing accepted `pricing_complete=true` without resolved route evidence.

The ensuing implementation replaced those paths, after which the focused six-package test command passed. The final tests also cover missing required fields, explicit nullability, duplicate keys at arbitrary depth, invalid UTF-8 on decode and encode, trailing values/garbage, the 64 MiB ceiling, deterministic render, provider/session composite identity, coverage equations, stable ordering, decision cycles and missing successors, candidate/run references, free numeric-zero pricing versus unknown null pricing, line/subtotal/total reconciliation, HTTPS provenance, unset/tampered digests, and cross-file project/generation/hash binding.

## Implementation and contract review

- `internal/strictjson` is the shared dependency-free boundary: bounded UTF-8, duplicate-key scan, exactly one JSON value, unknown-field rejection, required/nullability tags, and deterministic bounded output.
- `reviewv4`, `sessionindex`, `inspect`, `annotation`, and `pricing` preserve the frozen snake_case fields, versions, enums, limits, nullable pointers, and required arrays. Every JSON parse path uses `strictjson`.
- `session-index-v1.digest` and `machine-ledger-v4.sync_hashes.ledger_sha256` are computed from deterministic JSON with their own digest field omitted. Render paths normalize required nil collections, reparse, revalidate, and compare semantic values.
- `reviewv4.Parse` validates review-presentation-v4 plus machine-ledger-v4 and binds the raw `项目历史.md` UTF-8 bytes by SHA-256. `LoadProjection` additionally requires and returns a concrete validated `sessionindex.Document`, rejecting unset digests and project, generation, ProjectView, or digest disagreement.
- The history input is intentionally raw Markdown, not a second JSON contract. This follows the design ownership table and avoids rejecting real human-readable `项目历史.md` files.
- Dependency direction is acyclic: `strictjson` has no project dependency; `pricing`, `annotation`, `inspect`, and `sessionindex` depend only on it; `reviewv4` composes `pricing` and `sessionindex`.

## Fresh verification

- Schema fixture boundary: `go test ./internal/memory -run 'TestV4Contract' -count=1` — PASS.
- Focused: `go test ./internal/strictjson ./internal/reviewv4 ./internal/sessionindex ./internal/inspect ./internal/annotation ./internal/pricing -count=1` — PASS.
- Full serialized gate: `go test -p 1 -timeout 5m ./...` — PASS through `test/zerotoken`; elapsed 52.36s.
- `go vet ./...` — PASS.
- `go mod tidy -diff` — PASS with no diff.
- `git diff --check` — PASS.

## Concerns

No unresolved Task 2 blocker. Runtime validation deliberately adds semantic invariants that JSON Schema cannot express (coverage arithmetic, graph consistency, canonical digest verification, cross-file bindings, and price reconciliation) without changing the frozen wire shape.

## Fix Round 1

The review found two Critical, four Important, and one Minor gap in the initial replacement. Regression tests were added before implementation. The focused RED output is retained in `evidence/task-2-fix1-red.txt`; it demonstrated acceptance of case-folded aliases, loss of explicit empty optional arrays, wrapped coverage arithmetic, contamination from historical incomplete pricing, a known aggregate beside an unknown model cost, and form-feed URL whitespace.

The strict decoder now constructs exact allowed-key sets recursively for structs, embedded fields, pointers, slices, and typed map values while preserving arbitrary keys only at declared map boundaries. Patch and baseline optional arrays use presence-bearing pointers, so omitted and explicit-empty values remain distinct in canonical ledger input. Coverage equations use checked addition. Ledger aggregate completeness is derived only from validated current snapshot IDs, and any included model with unknown cost requires a null aggregate. Pricing provenance rejects all Unicode whitespace and control characters. Cross-file mutation tests now require every mutated artifact to render and validate independently, recompute dependent hashes, and fail only at the intended projection binding.

Fresh verification after the fixes:

- Focused six-package command: PASS.
- Frozen Task 1 fixture boundary: PASS.
- `go test -p 1 -timeout 5m ./...`: PASS through `test/zerotoken` in approximately 50 seconds.
- `go vet ./...`, `go mod tidy -diff`, and `git diff --check`: PASS.

No frozen schemas, fixtures, documentation, or plans were changed. No unresolved Fix Round 1 concern remains.
