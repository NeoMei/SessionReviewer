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
