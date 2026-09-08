# Task 2 implementation report

## Status

DONE

## Implementation

- Added the optional exact `SessionViewDigest` selector to `inspect.ConversationRequest` and the fixed CLI flag `--session-view-digest <digest>`.
- Kept omitted-selector behavior unchanged: current Session index membership remains the publication gate, unqualified responses retain the current `session_view_digest`, and availability-only fallback can still expose a distinct `evidence_session_view_digest`.
- For qualified requests, resolves only a matching current or retained conversation-chain root in the authenticated published manifest, checkpoints the bounded root traversal, loads and validates that root's SessionView/revisions/chain, and binds response identity and cursors to the selected view plus chain dependency.
- Performs source-catalog lookup only after the selected retained graph is authenticated. If the one current catalog record does not exactly match the historical view after append/replacement, returns authenticated retained excerpts with `retained_excerpt`; it does not reject the chain, synthesize/relabel a SourceRecord, or create a historical catalog.
- Shared the existing redaction/materialization/coverage policy helper between default and qualified source-backed reads so privacy and completeness semantics cannot diverge.
- Forwarded optional `sessionViewDigest` through the plugin runner using fixed argv. Runner and renderer now bind pages to `sessionViewDigest ?? expectedSessionViewDigest`; qualified evidence-view mismatches fail closed.
- Included the selected digest in renderer identity, so current -> old -> other-old updates reload and late old responses cannot trigger message reads or overwrite the later selection.
- Did not add milestone UI, source catalogs, publication changes, source writes, model/network calls, release work, or real Vault mutations.

## TDD evidence

### RED

1. CLI contract selector:

   `go test ./internal/cli -run 'TestConversationChainContractRequiresTurnForMessageCursorAndCapsSourceReads' -count=1`

   Failed to compile because `InspectRequest.SessionViewDigest` did not exist. This was the expected missing wire contract.

2. Historical append/rescan selector:

   `go test ./internal/inspect -run 'TestHistoricalConversationSelectionAfterAppendUsesExactRetainedSnapshot' -count=1`

   Failed to compile because `inspect.ConversationRequest.SessionViewDigest` did not exist. This was the expected missing inspection selector.

3. Plugin runner and renderer identity:

   `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts`

   Failed 3 tests: qualified response was rejected against the current digest, malformed selector reached the process seam and timed out, and a snapshot-only identity update retained the prior response. These were the expected missing runner/identity guards.

4. Selector cancellation:

   `go test ./internal/inspect -run 'TestHistoricalConversationSelectorChecksCancellationWhileScanningManifestRoots' -count=1`

   Failed with raw `context canceled`, proving the new root traversal lacked the required checkpoint/public diagnostic before the loop fix.

The initial combined RED command accidentally ran the Go package from `obsidian-plugin/` and failed setup with `stat .../obsidian-plugin/internal/cli: directory not found`; it was corrected immediately and is not treated as product evidence.

### GREEN

1. Focused Go:

   `go test ./internal/inspect ./internal/cli -run 'TestHistoricalConversationSelectionAfterAppendUsesExactRetainedSnapshot|TestHistoricalConversationSelectorChecksCancellationWhileScanningManifestRoots|TestConversationChainContractRequiresTurnForMessageCursorAndCapsSourceReads' -count=1`

   Passed: `internal/inspect` 3.671s, `internal/cli` 0.408s.

2. Focused plugin:

   `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts`

   Passed: 2 files, 61 tests, 1.98s. A final frozen replay also passed 61/61 in 8.73s.

3. Scoped vet:

   `go vet ./internal/inspect ./internal/cli`

   Exit 0, no output.

4. Full Go:

   `go test ./... -count=1`

   Retained session 1593 completed exit 0. Notable changed packages: `internal/cli` 260.179s, `internal/inspect` 185.737s, `internal/scan` 374.236s; all packages passed.

   An earlier identical full-Go invocation was launched inside a parallel orchestration call. It emitted passing early package output, but its session handle/final exit was not exposed; PID 99169 was observed until exit. It is deliberately not claimed as evidence and was replaced by retained session 1593 above. There was no observed product failure.

## Repository-owned regression coverage

- Real synthetic scan -> append/rescan keeps the same turn ID while adding a revised assistant answer.
- Default selection returns the current full three-message turn; explicit old selection returns only the original two-message authenticated retained snapshot and literal `Historical answer.` excerpt.
- Current and historical message cursors reject each other with `stale_cursor` within one published generation.
- Wrong project/provider/Session and an actual unreferenced SessionView CAS object fail closed.
- Malformed and duplicate CLI digest flags fail; omitted flags preserve existing calls.
- Qualified index and turn-message runner argv are exact; mismatched response/evidence identities fail; malformed selectors invoke zero process callbacks.
- Current -> old -> other-old renderer updates and delayed old replies retain the later authenticated selection.
- Synthetic data, project, Vault, and source file bytes remain unchanged across selected reads.

## Controller-owned frozen gates

Reported by the controller on the frozen diff:

- Full plugin check exit 0: lint, types, build, 27 files / 481 tests.
- Actual CLI parser selector/duplicate/path controls exit 0.
- Actual original -> append/rescan -> selected historical CLI lifecycle, literal old answer, default/explicit cursor rejection, and byte sentinels exit 0.
- Rebuilt runner fixed argv, identity mismatch, and invalid-selector zero-process probe exit 0.
- Renderer selected-identity reload and stale old -> other-old race probes exit 0.
- Fresh real-CLI bytes -> TypeScript parser -> Chrome keyboard current/historical/current replay at 1200px and 390px exit 0 with no console errors or overflow.

## Files changed

- `internal/inspect/conversation.go`
- `internal/inspect/conversation_retained.go`
- `internal/inspect/conversation_retained_test.go`
- `internal/inspect/conversation_test.go`
- `internal/cli/contracts.go`
- `internal/cli/contracts_test.go`
- `internal/cli/inspect.go`
- `obsidian-plugin/src/cli/runner.ts`
- `obsidian-plugin/src/view/render-conversation.ts`
- `obsidian-plugin/tests/conversation-page.test.ts`
- `obsidian-plugin/tests/render-conversation.test.ts`
- `.superpowers/sdd/2026-09-08-historical-turn-bindings/task-2-report.md`

## Self-review

- Confirmed default dependency digest and cursor construction remains in the original `conversationPage` path; only qualified requests use the selected chain/view dependency construction.
- Confirmed current Session index authentication still runs before the qualified branch and the shared final project/generation recheck still wraps it.
- Confirmed selected-root lookup cannot authorize arbitrary CAS objects and does not use candidate opaque public dependency digests as private proof.
- Confirmed response item/byte bounds, cursor bounds, fixed executable/no-shell protections, and renderer page/dependency guards remain intact.
- `git diff --check` passed before final gates.

## Concerns

- Historical full bodies are intentionally unavailable once the single current source-catalog record no longer matches; authenticated excerpts remain available. A historical catalog is explicitly outside Task 2.
- Milestone projection and evolution/source-selection controls remain separate work and were not implemented here.

## Fix round 1 at `7599e6b`

### Implementation

- Corrected the public conversation-page capability floor: omitted selection still emits `0.4.0`; any explicit `SessionViewDigest`, whether current or retained, emits `0.4.3` for both index and message pages.
- Kept the capability change local to conversation pages. Go rendering, the JSON Schema, and the TypeScript parser accept exactly `0.4.0` and `0.4.3`; unsupported future values remain invalid. Shared summary/event identity validation was not changed.
- Bound the runner and supplied-loader renderer to the request's exact expected floor. Selected requests reject downgraded `0.4.0` responses, while omitted requests reject unexpected selected-only `0.4.3` responses.
- Added direct explicit-current coverage for `source_full`, the literal revised full body, the `0.4.3` floor, and index/message cursor separation from the omitted-current `0.4.0` form.
- Added direct schema-validator coverage for both valid floors and the unsupported-future negative. Existing canonical omitted-selector fixture bytes remain unchanged.

### TDD and reproduction evidence

RED before the production correction:

- Controller actual-CLI lifecycle probe at `7599e6b`: `historical-capability-probe.mjs` passed default `0.4.0` and selected-view identity, then failed because the explicit historical response emitted `0.4.0` instead of required `0.4.3`.
- Controller actual-CLI explicit-current extension: failed in 2.857s after proving three messages and `source_full`; the only mismatch was the emitted `0.4.0` floor instead of `0.4.3`.
- Controller rebuilt runner probe: the qualified `0.4.3` response failed in the standalone parser after the default positive, proving the parser still hard-coded `0.4.0`.
- Repository schema mutation check after adding the focused regression: temporarily restoring the old schema `const` and running `go test ./internal/memory -run '^TestConversationPageSchemaAcceptsOnlyLegacyAndSnapshotQualifiedCapabilities$' -count=1` failed as expected with `conversation page schema rejected reader 0.4.3: $.minimum_reader_version: want const 0.4.0`.

GREEN on the final frozen diff:

- `go test ./internal/inspect ./internal/memory -run 'TestHistoricalConversationSelectionAfterAppendUsesExactRetainedSnapshot|TestConversationPageRendererAcceptsOnlyLegacyAndSnapshotQualifiedCapabilities|TestConversationPageFrozenFixtureRendersEmptyArrays|TestConversationPageSchemaAcceptsOnlyLegacyAndSnapshotQualifiedCapabilities' -count=1` passed: `internal/inspect` 3.508s, `internal/memory` 0.355s.
- `go vet ./internal/inspect ./internal/memory` exited 0 with no output.
- `npm test -- --run tests/conversation-page.test.ts tests/render-conversation.test.ts` passed 2 files / 64 tests in 1.63s.
- `git diff --check` exited 0.

Controller-owned final frozen verification:

- Full plugin check passed: lint, types, build, 27 files / 484 tests.
- Actual-CLI append/rescan lifecycle passed for omitted current `0.4.0`, explicit current/historical index and message `0.4.3`, literal current and retained answers, cross-mode cursor rejection, and no-write byte sentinels.
- Rebuilt runner capability and fixed-argv probes, selected-identity stale-response race probe, and fresh actual-CLI-wire browser replay passed. Production was unchanged between those probes and the final freeze.

### Files changed in fix round 1

- `internal/inspect/conversation.go`
- `internal/inspect/conversation_retained_test.go`
- `internal/inspect/conversation_test.go`
- `internal/memory/api_compat_test.go`
- `schemas/conversation-page-v1.schema.json`
- `obsidian-plugin/src/contracts/conversation-page.ts`
- `obsidian-plugin/src/data/conversation-page.ts`
- `obsidian-plugin/src/cli/runner.ts`
- `obsidian-plugin/src/view/render-conversation.ts`
- `obsidian-plugin/tests/conversation-page.test.ts`
- `obsidian-plugin/tests/render-conversation.test.ts`

### Self-review

- Confirmed explicit-current and historical requests share the same selector-presence capability rule and both page modes are produced through the same `conversationPageBound` path.
- Confirmed standalone consumers do not accept arbitrary/future versions, while request-aware consumers enforce exact request/response capability reciprocity.
- Confirmed the existing `validateIdentity` path remains unchanged for summaries and event pages.
- Confirmed omitted-selector canonical fixture content and default `0.4.0` behavior remain unchanged.
- Confirmed no source, Vault, network, model, release, milestone UI, or source-catalog behavior was added.

### Concerns

- None introduced by this fix. The original Task 2 historical-body and future milestone-projection boundaries remain as documented above.
