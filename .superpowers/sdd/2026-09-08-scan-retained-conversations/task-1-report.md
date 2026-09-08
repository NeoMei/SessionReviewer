# Task 1 report — bind source-prefix capability and immutable chains into scan

## Status and base

- Status: DONE
- Implementation base: `b9fac5c` (`docs: record full plugin topology regression gate`)
- Scope: Task 1 only. Retained-query/renderer wiring, source-root precedence for query, migrations, release, real user sources, daily Vault data, network calls, and Agent calls were not changed.

## What was implemented

### Authenticated visible-source capability

- Added provider-neutral `source.VisibleReader` plus typed `UnsupportedCapabilityError` / `ErrVisibleReaderUnsupported`.
- Added Manager dispatch by the authenticated `SourceRecord.Provider`; unregistered providers fail closed and a provider without the capability cannot fall through to another adapter or synthesize empty complete coverage.
- Added the Codex capability. It validates the source record, exact provider, availability, UUID identity, and every project association against the adapter bindings before reading. It delegates the accepted frozen prefix to `ReadPublishedVisible`; prefix namespace/hash/line/byte checks remain authoritative.
- Reconciled raw reader counters with deterministic `conversationchain.MaterializeVisible` segmentation. The result distinguishes captured, context, orphan, body truncation, oversized, malformed, raw source-record counts, and complete/incomplete state. Bytes appended after freeze are outside the authenticated prefix; mutation inside that prefix is rejected.

### Scan-to-chain publication lifecycle

- Planned one pure conversation-chain document immediately after each SessionView and active-lineage reconciliation.
- Resolved the exact active observation revisions from the current spool and authenticated prior observation chunks, verifying each revision ID before materialization rather than treating only the newest spool as full history.
- Added current conversation roots before generation hashing, reconciled previous current/history roots by `(provider, session, SessionView)`, and retained authenticated historical roots across append, supersession, source loss, repeated missing scans, and restoration.
- Persisted chain CAS objects after authenticated chunks/views and before prepared-generation advancement. Capacity rejection happens before new chain writes. Cancellation is checked per Session and before/after chain persistence boundaries, so the prepared pointer never advances on cancellation.
- Full source bodies remain local to one Session materialization and are cleared before proceeding; only bounded sanitized excerpts and typed facts enter the immutable chain.

### Persistent coverage and unsupported-capability state

- Added optional private `materialization_coverage_v1` to the Go document, TypeScript contract/parser, JSON schema, and paired canonical fixtures.
- The extension is included in `Document.Digest`, while legacy documents that omit it keep identical bytes and explicitly unknown diagnostics. `DependencyProofV1` and its digest preimage were not changed.
- Strict validation requires safe integers, `source_records >= visible + oversized + malformed`, `visible = captured + context + orphan`, truncation subsets, exact agreement with retained document visible/captured counts, derived `source_incomplete`, and no `answered` turn when the authenticated source materialization is incomplete.
- A selected available Session whose adapter lacks `VisibleReader` produces no forged chain. Its authenticated SessionView persists exact diagnostic `visible_reader_unsupported`; the Session index is `partial` with one warning and the scan is `completed_with_issues`. The compact public reason enum is deliberately unchanged because no truthful exact enum exists; Task 2 must expose the SessionView diagnostic on drilldown. Integrity, provider/project authentication, hash, and canonical-materialization failures still abort preparation and are never reclassified as unsupported.

### Static capability inventory

- Gate A changed by exactly two deterministic production import edges: `internal/source -> internal/conversationchain` for the capability contract and `internal/scan -> internal/conversationchain` for pure chain planning. No dangerous API, process, network, or Agent boundary was added or removed.

## TDD evidence

### RED

1. Actual scan integration before implementation:
   - Command: `go test -overlay /tmp/session-reviewer-scan-chain-qa.6VBnCG/overlay.json ./internal/scan -run '^TestControllerScanRetainedConversationProbe$' -count=1`
   - Exit 1: a valid real Codex UUID rollout prepared one Session but the manifest had 0 conversation roots, expected 1. This proved the missing source-prefix -> chain -> manifest path.
2. Focused repository contract/lifecycle tests were introduced and run with `go test ./internal/source ./internal/source/codex ./internal/scan -run 'VisiblePrefix|ScanConversation|RetainedConversation' -count=1`; they initially failed/failed to compile until the new capability, Manager/Codex implementations, store method, manifest roots, and lifecycle integration existed.
3. Gate A replay initially reported exactly two unapproved import edges (`source -> conversationchain`, `scan -> conversationchain`). The inventory was updated only for those reviewed deterministic edges.
4. Go coverage integrity negative controls initially failed because validation accepted a known all-zero/mismatched materialization extension alongside retained turns (`mismatched known diagnostics accepted: <nil>`).
5. Go and TypeScript source-integrity controls initially failed because `source_incomplete=true` still permitted `answer_state=answered` (`incomplete source claimed answered turn: <nil>` / parser did not throw).
6. The first broad focused Go package run found five pre-existing scan tests whose shared fake adapter unintentionally acquired the optional interface but defaulted disabled. The harness default was corrected to capability-enabled; the dedicated unsupported test disables it explicitly. The history control then exposed only an over-specific error-prefix assertion, which was narrowed while preserving proof that failure occurs before publication advancement.
7. The first full Go suite found one stale materializer fixture: it claimed complete coverage despite one ambient context message and a >64 KiB assistant body. The fixture now reports exact context/truncation counters and expects a partial answer.
8. The first final plugin check stopped at lint on two unused destructured digest bindings in the new negative test. The preimages now remove `digest` explicitly without unused variables.

### GREEN

- Focused visible/source/scan chain tests: passed after capability and lifecycle integration.
- Exact source/scan lifecycle regressions cover initial chain, identical byte/generation reuse, second turn, append, active-revision supersession, historical reachability, source deletion plus repeated rescan, restoration, unsupported capability, fatal integrity contrast, cancellation before chain persistence, cancellation after CAS before pointer advance, and capacity preservation.
- `go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1 -v`: PASS; Gate A `154/154 terminal, 151 indexed`, zero model tokens (about 51 seconds during iteration).
- `go test ./internal/conversationchain -run '^TestMaterializeRetainedIgnoresAmbientUserAndBoundsUTF8$' -count=1 -v`: PASS (1/1, 0.512s).
- `go vet ./internal/source/... ./internal/scan ./internal/conversationchain ./internal/memorystore ./test/zerotoken`: exit 0, no output.
- `npm run check` in `obsidian-plugin`: exit 0; ESLint clean, 27/27 test files and 450/450 tests passed, TypeScript/esbuild production build passed.
- `go test ./... -count=1`: exit 0; all packages passed, including `internal/scan` (343.340s), `internal/source/codex` (21.603s), `internal/memorystore` (233.884s), and `test/zerotoken` (206.603s).
- Controller-owned independent final external overlay: actual Codex scan plus real repeat/removal/restoration lifecycle passed on the final working files (1.661s). This is corroborating evidence, not a replacement for repository gates.

## Files changed

- `internal/source/visible.go`, `internal/source/visible_test.go`
- `internal/source/manager.go`
- `internal/source/codex/visible.go`, `internal/source/codex/adapter_test.go`
- `internal/scan/conversation.go`, `internal/scan/conversation_test.go`
- `internal/scan/service.go`, `internal/scan/service_test.go`
- `internal/conversationchain/types.go`, `codec.go`, `retained.go`, `validate.go` and focused tests
- `internal/memory/api_compat_test.go`
- `schemas/conversation-chain-v1.schema.json`
- `testdata/contracts/v4/conversation-chain-v1.coverage.valid.json`
- `obsidian-plugin/src/contracts/review-v4.ts`
- `obsidian-plugin/src/data/contracts-v4.ts`
- `obsidian-plugin/tests/contracts-v4.test.ts`
- `obsidian-plugin/tests/fixtures/v4/conversation-chain-v1.coverage.valid.json`
- `testdata/zero-token/gate-a-production-import-edges.txt`
- This report.

## Self-review

- Re-read the Task 1 brief and reviewed the complete production/test diff against source authentication, generation identity, persistence ordering, immutable history, cancellation, capacity, privacy, coverage integrity, Go/TypeScript/schema parity, and zero-Agent boundaries.
- Checked that the two canonical coverage fixtures are byte-identical and digest-valid, that legacy omitted-extension rendering remains unchanged, and that the dependency-proof preimage was not modified.
- `git diff --check` is clean. No controller Task 2 artifacts or unrelated documentation are included in the implementation commit.

## Issues and concerns

- No correctness blocker remains for Task 1.
- Deliberate boundary for Task 2: the exact unsupported capability reason is persisted in the private authenticated SessionView and reflected as partial/warning in the index, but is not mapped to a misleading existing public reason enum. Task 2 must render/query that exact private diagnostic.
- The controller's retained-query-after-source-removal and scan/query root-precedence probes remain Task 2 REDs and were intentionally not implemented here.

## Fix round 1 — authenticate legacy project bindings before capability classification

- Independent review found that `ReadVisiblePrefix` classified a non-UUID legacy/native Session identity as unsupported before authenticating its project associations. A foreign-project legacy record could therefore reach scan's bounded unsupported path (`completed_with_issues`, no chain) instead of failing closed.
- RED external control:
  - Command: `go test -overlay /tmp/session-reviewer-scan-chain-qa.6VBnCG/codex-binding-overlay.json ./internal/source/codex -run '^TestControllerLegacyVisibleProjectBinding$' -count=1 -v`
  - Exit 1: `foreign project binding downgraded to capability limitation: source provider "codex": visible source reader capability is unsupported`.
- RED repository control:
  - Command: `go test ./internal/source/codex -run '^TestReadVisiblePrefixClassifiesLegacyIdentityButNeverIntegrityFailureAsUnsupported$' -count=1 -v`
  - Exit 1 at `adapter_test.go:266`: the foreign legacy binding returned `ErrVisibleReaderUnsupported` instead of a fatal binding error.
- Fix: moved the UUID/native-capability classification after complete project-binding authentication. Provider, availability, and source-record validation remain first; a bound legacy identity still returns typed unsupported, while a foreign-bound legacy identity now fails authentication and can never be caught as unsupported.
- GREEN focused repository controls:
  - Command: `go test ./internal/source/codex -run '^(TestReadVisiblePrefixAuthenticatesProjectAndReadsOnlyFrozenPrefix|TestReadVisiblePrefixClassifiesLegacyIdentityButNeverIntegrityFailureAsUnsupported)$' -count=1 -v`
  - Exit 0: both tests passed, covering valid UUID/provider/frozen-prefix/hash behavior, bound legacy unsupported, foreign legacy fatal binding rejection, and UUID integrity failure never reclassified.
- GREEN external control:
  - Command: `go test -overlay /tmp/session-reviewer-scan-chain-qa.6VBnCG/codex-binding-overlay.json ./internal/source/codex -run '^TestControllerLegacyVisibleProjectBinding$' -count=1 -v`
  - Exit 0: controller legacy project-binding probe passed.
- Fix files: `internal/source/codex/visible.go`, `internal/source/codex/adapter_test.go`, and this report. No Task 2, schema, scan lifecycle, Vault, main, remote, or release changes.
