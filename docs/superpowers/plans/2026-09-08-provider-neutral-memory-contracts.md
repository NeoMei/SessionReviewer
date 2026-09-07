# Provider-Neutral Memory Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Run after the Codex decoder review, before retained materialization, private-search mixed-provider fixtures or adapter registration.

**Goal:** Remove the obsolete Codex-only restriction from generic immutable memory contracts while preserving exact source identity and location validation.

**Architecture:** Generic schemas accept bounded safe provider IDs; an actual registered SourceAdapter remains the capability boundary. A source location remains an exact discriminated JSONL coordinate for the currently supported source representation. Removing a provider literal must not remove any source/hash/identity/graph invariant or claim an unimplemented reader.

**Tech Stack:** Existing Go memory, JSON Schema fixture validator, source manager and private store tests; no dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§12.2,13.11,16.2,17.1,17.3.

## Global Constraints

- Generic contracts are provider-neutral: `provider` is a safe ID, not a `codex` enum.
- Namespaced identity is `(provider,session_id)`; same native IDs across providers are distinct.
- Preserve source hash, exact JSONL discriminant, nonnegative bounded coordinates, project/source/ref identity equality, revision digest, active lineage and all graph reconciliation.
- An accepted provider-shaped record is not proof of a working SourceAdapter. Do not enable Claude/OpenCode in production, remove the current conversation-query unsupported capability boundary, or claim parity in this task.
- No new source location kind, raw transcript persistence, Agent/network, migration, real-Vault mutation or release.

### Task 1: Separate generic identity validity from registered reader capability

**Files:** Modify `internal/memory/types.go`, `types_test.go`; create `internal/memory/provider_contract_test.go`; update `schemas/source-catalog-v1.schema.json`, `observation-v1.schema.json`, `session-view-v1.schema.json`, `session-lineage-v1.schema.json`, `project-view-v1.schema.json`, `generation-manifest-v1.schema.json`. Add focused real-store tests in `internal/memorystore/provider_contract_test.go` and source-catalog identity tests if the existing fixture helpers allow direct reuse. No new public response shape.

**Interfaces:** Existing `ValidateSourceRecord`, `ValidateObservationRevision`, `ValidateSessionView`, `ValidateSessionLineage`, `ValidateProjectView`, `ValidateGenerationManifest`, canonical codecs and stores retain their signatures. Use existing `safeIDPattern` (`^[a-z0-9][a-z0-9._-]{0,127}$`) for every generic provider field. Existing `source.Manager` still rejects an unregistered provider at dispatch.

- [ ] RED runtime and schema tests clone canonical valid fixtures for `codex`, `claude`, `opencode` and `custom-provider`, updating all connected provider identities and canonical hashes. All generic contracts should accept each well-formed provider. Reject empty, uppercase, slash/backslash, traversal, whitespace, non-ASCII,128/129-byte overflow boundary and NUL provider values using the exact existing safe-ID length. A128-byte matching ID is accepted;129 bytes is rejected.
- [ ] RED namespaced graph tests: two Sessions with the same native ID under different providers remain two distinct dependencies/usage rows; duplicate same-provider identity rejects. Mismatched observation key/ref provider or SessionView summary source still rejects even though each provider is independently valid. Mismatched lineage/provider, unknown location discriminator, absent JSONL payload, bad coordinates and forged revision digest all continue to fail.

```go
for _, provider := range []string{"codex", "claude", "opencode", "custom-provider"} {
    value := validObservation(validObservationKey(), "adapter-1", map[string]string{"exit_code":"0"})
    value.Key.Provider, value.Ref.Provider = provider, provider
    value.RevisionID = ObservationRevisionID(value)
    if err := ValidateObservationRevision(value); err != nil {
        t.Fatalf("generic provider %q rejected: %v", provider, err)
    }
}
```

- [ ] Run RED `go test ./internal/memory -run 'ProviderNeutral|ProviderContract' -count=1`.
- [ ] Replace only the obsolete `provider != "codex"` branches in generic source identity/location and dependency/usage validators with safe-ID checks. Keep exact JSONL support and adjust error copy to describe the generic supported shape. Replace corresponding JSON Schema provider constants with the same safe-ID pattern and length, preserving all other properties and canonical legacy bytes.
- [ ] Reconcile old tests that assert Claude is categorically invalid: replace them with invalid-safe-ID or mismatched-identity cases. Schema cannot enforce cross-field equality, so single-field provider mismatch remains a runtime test; schema tests use malformed IDs/discriminator shape instead. Preserve every non-provider assertion from `TestCodexV1UsesExactDiscriminatedJSONLSourceLocations` under a neutral name.
- [ ] Add real-store publication/reload test for a mixed-provider generation, using actual canonical observations, SessionViews, lineages and ProjectView. Verify counts remain two for equal native IDs, retained lookup stays namespaced, corrupt cross-provider substitution fails preparation/read, and the valid snapshot passes retention reporting. Keep tests synthetic and offline; existing source.Manager unregistered-provider rejection remains unchanged.
- [ ] GREEN `go test ./internal/memory ./internal/memorystore ./internal/sourcecatalog ./internal/sessionview ./internal/projectview ./internal/source -count=1`, scoped vet and one full `go test ./...`; plugin check validates unchanged shared consumer fixtures. `git diff --check`, commit exact files, report RED/GREEN and independent review before dependent tasks.

## Preflight ruling

Ruling: Generic schema validity is not adapter capability — the accepted spec explicitly requires safe provider IDs, while actual execution remains gated by registered adapters — cost if wrong is a stricter future allowlist at registration, not acceptance of mismatched hashes or untrusted source execution.

This is a prerequisite correction, not completion of S11. Real Claude/OpenCode discovery, freezing, decoding, authenticated visible reads, registration, failure isolation and native parity still follow in the release queue.
