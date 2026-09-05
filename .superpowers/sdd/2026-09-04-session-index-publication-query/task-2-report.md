# Task 2 implementation report

Status: DONE. Implementation is complete; the controller's independent review remains separate.

## Implemented and currently passing

- Cumulative immutable Session index builder with mixed-provider identities, absent accepted entries retained as unavailable, null timestamps preserved, typed terminal/coverage mapping, canonical ordering and rendering.
- Capacity failures use `sessionindex.ErrCapacityExceeded` for 65,537 Sessions and an actually rendered document over 64 MiB.
- Current SessionView inputs must exactly match the ProjectView/manifest dependency set; missing-current SessionViews retain accepted processing/history facts.
- Generation manifest carries exact per-Session measurements, retained SessionView dependencies, retained-facts digest, previous accepted index digest, and current index digest.
- Store loads and authenticates current/retained views, current/previous indexes, and re-runs the canonical builder to reject forged exact measurements or retained facts.
- Generation identity excludes only the current index self-reference and includes the stable previous accepted index binding; later-clock identical scans reuse immutable generation/index bytes.
- Retention traversal includes current/previous Session indexes and retained SessionViews; the published pointer is a protected graph root and is revalidated before cleanup.
- Gate A exact PATH/import/EDGE/reflection additions were controller-reviewed; the 42 permitted reflection records are copied in the approved dangerous-capability baseline without loosening the detector.

## TDD evidence so far

### RED

- `go test ./internal/memorystore -run TestPrepareGenerationRejectsSessionIndexMeasurementMismatch -count=1`
  - Failed because the store accepted a Session index whose `record_count`/coverage disagreed with authenticated manifest measurements.
- `go test ./internal/memorystore -run TestPrepareGenerationRejectsSessionIndexWithOmittedMeasurements -count=1`
  - Failed because a new indexed manifest with no exact measurement set was accepted.
- `go test ./internal/scan -run TestGenerationIdentityIncludesPreviousIndexButExcludesCurrentIndexDigest -count=1`
  - Failed because `PreviousSessionIndexDigest` was incorrectly excluded from generation identity.

### GREEN

- `go test ./internal/memorystore -run 'TestPrepareGenerationRejectsSessionIndex(MeasurementMismatch|WithOmittedMeasurements|WithMissingDependency)|TestPrepareGenerationRejectsForgedRetainedFactsDigest' -count=1`
  - PASS.
- `go test ./internal/scan -run 'TestGenerationIdentityIncludesPreviousIndexButExcludesCurrentIndexDigest|TestRunLaterClockIdenticalScan' -count=1`
  - PASS.
- `go test ./internal/sessionindex -run TestBuildRejectsRenderedDocumentAbove64MiBWithCapacitySentinel -count=1`
  - PASS; fixture uses 65,536 valid maximal rows and exercises the strict JSON 64 MiB bound.
- `go test ./internal/scan -run TestRunBuildsFromLastPublishedIndexAndPreservesUnavailableHistory -count=1`
  - Initially failed because the retained-facts fingerprint was computed before canonicalizing availability to `unavailable`; PASS after normalization. Covers a published baseline, an identity absent from both discovery and catalog, publication of its retained successor, and a third later-clock scan with stable generation/index/previous-index digests.
- `go test ./internal/scan -run 'TestRunBuildsFromLastPublishedIndexAndPreservesUnavailableHistory/retention' -count=1`
  - Initially failed because retention did not recognize `published_generation`; PASS after making it a protected root. Test moves dependencies outside the namespace, asserts the exact missing retained-view/index failure, restores them, runs cleanup, compares reachable accounting, and proves both objects remain readable.
- `go test ./internal/scan -run TestRunSessionIndexCapacityFailureLeavesCatalogAndPointersUnchanged -count=1`
  - PASS after adding the smallest private builder seam. A typed capacity failure occurs before catalog writes and leaves prepared/published pointers unchanged.
- `go test ./internal/sessionindex ./internal/scan ./internal/memorystore -run 'Build|Capacity|Cumulative|Measurement|Retained|LaterClock|GenerationIdentity|Retention' -count=1`
  - PASS: sessionindex 1.171s; scan 3.853s; memorystore 15.553s.
- `go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1`
  - PASS in 32.262s after canonical sorting of the exact approved baseline records.

## Final verification

- `go test -p=1 -timeout=5m -count=1 ./...`
  - Exit 1. Every reported package passed, including Task 2's `internal/memory`, `internal/memorystore`, `internal/scan`, `internal/sessionindex`, `internal/source`, `internal/source/codex`, `internal/sourcecatalog`, and `internal/strictjson`; the run reached the final `test/zerotoken` package and failed only `TestGateBEndToEndPublicationAndIdempotence`. Its stale expectation required a later wall clock to create a new private generation. Task 2 intentionally reuses the byte-identical generation, and prepared/published/result IDs were all the unchanged accepted ID.
- `go test ./test/zerotoken -run '^TestGateBEndToEndPublicationAndIdempotence$' -count=1`
  - PASS in 6.973s after updating only that old assertion/comment to the accepted identical-scan idempotence contract. Per controller instruction, the expensive full suite was not blindly rerun.
- `go vet ./...`
  - PASS, no output.
- `go mod tidy -diff`
  - PASS, no output/diff.
- `git diff --check`
  - PASS, no output.

## Exact Gate A dependency delta

- PATH: `internal/sessionindex`, `internal/strictjson`.
- EDGE (21): `memorystore -> sessionindex`; `scan -> sessionindex`; `sessionindex -> crypto/sha256, encoding/hex, errors, fmt, memory, strictjson, reflect, regexp, sort, strings, time`; `strictjson -> bytes, encoding/json, errors, fmt, io, reflect, strings, unicode/utf8`.
- Dangerous-capability baseline: 42 controller-reviewed reflection records, limited to existing `internal/sessionindex/validate.go` and `internal/strictjson/codec.go`; no `reflect.Call`, `Method`, `unsafe`, process, network, model, or dynamic-loading capability was added. Exact diagnostic evidence is retained at `.superpowers/sdd/2026-09-04-session-index-publication-query/reflection-delta.txt`.

## Files changed

- `internal/sessionindex/build.go`, `build_test.go`
- `internal/memory/types.go`, `schemas/generation-manifest-v1.schema.json`
- `internal/source/adapter.go`, `internal/source/codex/decode.go`
- `internal/scan/service.go`, `service_test.go`
- `internal/memorystore/store.go`, `store_test.go`, `retention.go`
- `test/zerotoken/gate_b_test.go`
- Gate A reviewed baseline files under `testdata/zero-token/`

## Self-review

- Scope is limited to Task 2 builder, exact measurement plumbing, store binding/authentication, retention, scan integration, and corresponding audit fixtures/tests. Task 3 publication rendering and later inspect/query work were not implemented.
- Cumulative history is always sourced from the last published/accepted index, never merely the latest prepared generation.
- Generation-free identity includes retained dependencies, exact measurements, retained-facts fingerprint, and stable previous index baseline; it excludes only the current index self-reference.
- Capacity is checked by the builder before the source catalog batch is applied. Store graph reconciliation independently re-builds the index so a forged but schema-valid index cannot advance the pointer.
- Legacy manifests without the optional extension retain their prior canonical representation; a manifest that binds a new Session index must supply exact measurement coverage.

## Concerns

- The single full-suite run is not a pristine all-green invocation because it exposed the stale Gate B expectation. The exact failing assertion was repaired and its focused regression passed; the full suite was intentionally not repeated per controller instruction.
