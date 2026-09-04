# Obsidian Context Gate 0 Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Freeze and validate every v4 persistence, state-machine, CLI, migration, and provider-neutral contract before any of the six user-facing features are implemented.

**Architecture:** Keep the existing immutable Observation/SessionView/ProjectView store as the factual layer. Add strict v4 wire contracts and validators at package boundaries, with TypeScript mirrors for Obsidian. Conversation chains remain private deterministic derivatives; the formal problem map and milestone closures live in HumanPresentation, while placement candidates remain private. Gate 0 defines data shapes and command grammars only; feature services are implemented by the six follow-on plans.

**Tech Stack:** Go 1.26, JSON Schema, existing canonical JSON/digest helpers, TypeScript 5.8, Vitest, Obsidian 1.13.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`

## Global Constraints

- Start from the released `0.3.5` v3 architecture at commit `ea5b1ba` in isolated branch `codex/obsidian-context-v4`. The original dirty worktree remains untouched; do not reset, checkout, clean, or overwrite it.
- This plan is the prerequisite for the six feature plans dated 2026-09-04. Do not implement UI behavior here.
- Generic schemas use `(provider, session_id)` identity and never constrain provider to `codex`.
- Unknown prices are `null`, never numeric zero. Human-confirmed semantics and deterministic machine facts remain separate.
- All decoders reject duplicate JSON keys, unknown fields, oversized input, invalid UTF-8, non-canonical enums, inconsistent counts, and trailing JSON.
- Tests use fixtures and temporary directories only. Never read live Session stores or a real Vault.
- Every task runs focused tests first, then `go test ./...`, `go vet ./...`, `go mod tidy -diff`; plugin tasks also run `npm run check` in `obsidian-plugin`.
- Commit steps are conditional on the execution environment permitting Git mutation and the user authorizing it; otherwise record the checkpoint and continue without committing.

## File Structure and Ownership

- `schemas/`: normative JSON Schema documents for nine persisted/read contracts plus the `pricing-supplement-v1` input contract.
- `internal/reviewv4/`: review-presentation-v4 and machine-ledger-v4 types, codecs, cross-record invariants.
- `internal/sessionindex/`: session-index-v1 types and validation.
- `internal/inspect/`: session-summary-v1 and session-event-page-v1 response types.
- `internal/annotation/`: agent-annotation-v1 candidate and extraction-run types.
- `internal/pricing/`: pricing-snapshot-v1 types and validation only.
- `internal/conversationchain/`: conversation-chain-v1 types, canonical codec and invariants only.
- `internal/problemmap/`: problem-map-candidate-v1 types, graph validation and candidate transition validation only.
- `internal/cli/contracts.go`: command grammar, bounded scalar validators, and stable error codes; no service implementation.
- `obsidian-plugin/src/contracts/review-v4.ts`: TypeScript mirrors used by later repository and view work.
- `testdata/contracts/v4/` and `obsidian-plugin/tests/fixtures/v4/`: shared valid/invalid compatibility fixtures.

## Plan Set Coverage

| Spec area | Owning plan |
|---|---|
| Principles, version boundary, persisted contracts, state enums, compatibility matrix | This Gate 0 plan |
| Complete cumulative Sessions, provider fan-in, four-file publication, summaries/events/search | `2026-09-04-session-index-publication-query.md` |
| Five-tab shell, readable evolution, complete virtual list, CLI degradation, installed Obsidian behavior | `2026-09-04-obsidian-all-sessions-view.md` |
| Human decisions/agreements, candidate extraction/CAS, three-file human publication | `2026-09-04-decisions-and-candidates.md` |
| ModelPriceWatch cache/matching, billable quantities, immutable snapshots, supplements, usage cards | `2026-09-04-modelpricewatch-pricing.md` |
| Visible Q/A segmentation, cross-Session evidence chains, milestone closure and migration | `2026-09-04-conversation-chain-evolution-closure.md` |
| Formal problem tree, deterministic placement candidates and Obsidian interaction | `2026-09-04-problem-map-placement.md` |

---

### Task 1: Freeze the initial v4 schemas and shared enums

**Files:**
- Create: `schemas/review-presentation-v4.schema.json`
- Create: `schemas/machine-ledger-v4.schema.json`
- Create: `schemas/session-index-v1.schema.json`
- Create: `schemas/session-summary-v1.schema.json`
- Create: `schemas/session-event-page-v1.schema.json`
- Create: `schemas/agent-annotation-v1.schema.json`
- Create: `schemas/pricing-snapshot-v1.schema.json`
- Create: `schemas/pricing-supplement-v1.schema.json`
- Create: `testdata/contracts/v4/*.json`
- Modify: `internal/memory/api_compat_test.go`

**Interfaces:** The persisted fields and enums are exactly those in spec sections 15–17. `pricing-supplement-v1` is an input contract, not a persisted eighth artifact. `session-index-v1.coverage` enforces all three sum/length invariants. Decision, candidate, processing, source-availability, and price states are closed enums.

- [ ] **Step 1: Add valid minimum fixtures and one invalid fixture per invariant**

```go
func TestV4ContractFixtures(t *testing.T) {
    for _, name := range []string{"review-presentation-v4", "machine-ledger-v4", "session-index-v1", "session-summary-v1", "session-event-page-v1", "agent-annotation-v1", "pricing-snapshot-v1", "pricing-supplement-v1"} {
        validateFixture(t, "../../testdata/contracts/v4/"+name+".valid.json", name)
        rejectFixture(t, "../../testdata/contracts/v4/"+name+".invalid.json", name)
    }
}
```

The minimum `session-index-v1` schema starts with the concrete closed shape below and expands every referenced definition in the same file:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://sessionreviewer.local/schemas/session-index-v1.schema.json",
  "type": "object",
  "additionalProperties": false,
  "required": ["schema_version", "minimum_reader_version", "digest", "project_id", "generation_id", "project_view_digest", "generated_at", "sort_version", "coverage", "sessions"],
  "properties": {
    "schema_version": { "const": 1 },
    "sort_version": { "const": "started-at-desc-null-last-provider-session-v1" },
    "sessions": { "type": "array", "maxItems": 65536, "items": { "$ref": "#/$defs/session" } }
  }
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./internal/memory -run TestV4ContractFixtures -count=1`

Expected: FAIL because the schemas and fixtures do not exist.

- [ ] **Step 3: Implement the schemas with `additionalProperties: false` at every object boundary**

Use byte-length validation in Go for limits JSON Schema cannot express reliably. Keep nullable timestamps/rates explicit with `type: ["string", "null"]` or `type: ["number", "null"]`.

- [ ] **Step 4: Run GREEN and full Go gates**

Run: `gofmt -w internal/memory && go test ./internal/memory -count=1 && go test ./... && go vet ./... && go mod tidy -diff`

- [ ] **Step 5: Commit the contract checkpoint when authorized**

```bash
git add schemas testdata/contracts internal/memory/api_compat_test.go
git commit -m "feat: freeze project context v4 schemas"
```

---

### Task 2: Add strict Go wire types and validators

**Files:**
- Create: `internal/reviewv4/types.go`, `codec.go`, `validate.go`, `codec_test.go`
- Create: `internal/sessionindex/types.go`, `validate.go`, `validate_test.go`
- Create: `internal/inspect/types.go`, `validate.go`, `validate_test.go`
- Create: `internal/annotation/types.go`, `validate.go`, `validate_test.go`
- Create: `internal/pricing/types.go`, `validate.go`, `validate_test.go`

**Interfaces:**

```go
type SessionKey struct { Provider, SessionID string }
type ProcessingState string // complete|partial|error|unprocessed
type DecisionStatus string  // active|superseded|archived
type CandidateStatus string // pending|confirmed|ignored|not_decision|stale
type PriceStatus string     // pending|current|promotion|stale_estimate|manual_supplement|ambiguous|legacy_unverified|superseded

func sessionindex.Parse([]byte) (Document, error)
func sessionindex.Render(Document) ([]byte, error)
func reviewv4.Parse(review, history, ledger []byte) (Accepted, error)
func reviewv4.RenderLedger(MachineLedger) ([]byte, error)
func inspect.RenderSummary(SessionSummary) ([]byte, error)
func inspect.RenderEventPage(SessionEventPage) ([]byte, error)
func annotation.Validate(StoreRecord) error
func pricing.ValidateSnapshot(Snapshot) error
```

- [ ] **Step 1: Write table tests for identity, nullability, graph, and coverage invariants**

```go
func TestSessionIndexRejectsCrossProviderDuplicateOnlyWhenFullKeyMatches(t *testing.T) {
    doc := minimumIndex()
    doc.Sessions = []Entry{{Provider: "codex", SessionID: "same"}, {Provider: "claude", SessionID: "same"}}
    rebuildCoverage(&doc)
    if err := Validate(doc); err != nil { t.Fatal(err) }
    doc.Sessions[1].Provider = "codex"
    if err := Validate(doc); err == nil { t.Fatal("accepted duplicate provider/session identity") }
}
```

Also reject decision supersession cycles, `pricing_complete=true` with a nil rate or total, free price represented as null, and cursors present when `total=0`.

- [ ] **Step 2: Run RED**

Run: `go test ./internal/reviewv4 ./internal/sessionindex ./internal/inspect ./internal/annotation ./internal/pricing -count=1`

Expected: FAIL because the packages do not exist.

- [ ] **Step 3: Implement strict codecs using one canonical JSON helper**

Render maps/slices deterministically, normalize nil collections to empty JSON arrays where required, calculate digest after omitting the digest field, and compare the decoded semantic value after render.

- [ ] **Step 4: Run focused and full GREEN gates**

Run: `gofmt -w internal/reviewv4 internal/sessionindex internal/inspect internal/annotation internal/pricing && go test ./internal/reviewv4 ./internal/sessionindex ./internal/inspect ./internal/annotation ./internal/pricing -count=1 && go test ./... && go vet ./... && go mod tidy -diff`

- [ ] **Step 5: Commit when authorized**

```bash
git add internal/reviewv4 internal/sessionindex internal/inspect internal/annotation internal/pricing
git commit -m "feat: add strict v4 wire validators"
```

---

### Task 3: Freeze CLI grammar and failure codes without feature side effects

**Files:**
- Create: `internal/cli/contracts.go`, `contracts_test.go`
- Modify: `internal/cli/run_test.go`

**Interfaces:**

```go
const MaxInspectPageSize = 100
const MaxInspectQueryBytes = 256
const MaxDecisionInputBytes = 64 << 10
const MaxOpaqueCursorBytes = 4096

type ContractError struct { Code string; Message string }
func ParseInspectContract(args []string) (InspectRequest, error)
func ParseDecisionContract(args []string) (DecisionRequest, error)
```

Stable codes include `invalid_argument`, `generation_mismatch`, `stale_cursor`, `anchor_out_of_range`, `response_too_large`, `candidate_revision_conflict`, `review_preimage_conflict`, `session_index_capacity_exceeded`, and `migration_preview_stale`. Migration parsing fixes `--confirm-migration --expected-preview-digest <sha256>`; plain `sync` cannot authorize v3→v4.

- [ ] **Step 1: Add exact-argv allowlist tests for every command in spec 17.3–17.4**

```go
func TestParseInspectContractRejectsMixedCursorAndAnchor(t *testing.T) {
    _, err := ParseInspectContract([]string{"session-events", "--project-id", "project-p", "--provider", "codex", "--session-id", "s1", "--expected-generation-id", "g1", "--cursor", "opaque", "--anchor", "2", "--limit", "100", "--json"})
    if codeOf(err) != "invalid_argument" { t.Fatalf("code=%q err=%v", codeOf(err), err) }
}
```

- [ ] **Step 2: Run RED:** `go test ./internal/cli -run 'Test(ParseInspect|ParseDecision)Contract' -count=1` and expect missing parsers.
- [ ] **Step 3: Implement grammar-only parsing; do not add root dispatch or storage calls yet.** Reject arbitrary file arguments, simultaneous cursor/anchor, limit outside 1–100, oversized UTF-8 query/cursor, and unknown enum values.

```go
func ParseInspectContract(args []string) (InspectRequest, error) {
    if len(args) == 0 { return InspectRequest{}, contractError("invalid_argument", "inspect subcommand is required") }
    switch args[0] {
    case "session-summary": return parseSessionSummaryArgs(args[1:])
    case "session-events": return parseSessionEventArgs(args[1:])
    case "session-search": return parseSessionSearchArgs(args[1:])
    default: return InspectRequest{}, contractError("invalid_argument", "unknown inspect subcommand")
    }
}
```
- [ ] **Step 4: Run GREEN:** `gofmt -w internal/cli && go test ./internal/cli -count=1 && go test ./... && go vet ./... && go mod tidy -diff`.
- [ ] **Step 5: Commit when authorized:** `git add internal/cli && git commit -m "feat: freeze inspect and decision command contracts"`.

---

### Task 4: Mirror contracts in the Obsidian plugin

**Files:**
- Create: `obsidian-plugin/src/contracts/review-v4.ts`
- Create: `obsidian-plugin/src/data/contracts-v4.ts`
- Create: `obsidian-plugin/tests/contracts-v4.test.ts`
- Create: `obsidian-plugin/tests/fixtures/v4/*.json`

**Interfaces:**

```ts
export type ViewKind = "evolution" | "problems" | "decisions" | "sessions" | "usage";
export type SessionIdentity = Readonly<{ provider: string; sessionId: string }>;
export function parseMachineLedgerV4(source: string): MachineLedgerV4;
export function parseSessionIndexV1(source: string): SessionIndexV1;
export function parseSessionSummaryV1(source: string): SessionSummaryV1;
export function parseSessionEventPageV1(source: string): SessionEventPageV1;
export function parseCandidateListV1(source: string): CandidateListV1;
```

- [ ] **Step 1: Write fixture parity tests** that accept the same valid fixtures and reject the same invalid cases as Go.

```ts
it("rejects a mixed-generation index", () => {
  const value = validIndex();
  value.generation_id = "generation-other";
  expect(() => assertSnapshotBindings(validLedger(), value)).toThrow(/generation/i);
});
```

- [ ] **Step 2: Run RED:** `cd obsidian-plugin && npm test -- contracts-v4.test.ts`; expect missing modules.
- [ ] **Step 3: Implement strict parsers** with byte limits before `JSON.parse`, recursive allowed/required-key checks, safe-integer validation, and full `(provider, sessionId)` uniqueness.

```ts
export function parseSessionIndexV1(source: string): SessionIndexV1 {
  if (Buffer.byteLength(source, "utf8") > (64 << 20)) throw new Error("session index exceeds 67108864 bytes");
  rejectDuplicateJsonKeys(source);
  const root = object(JSON.parse(source), "$", SESSION_INDEX_KEYS, SESSION_INDEX_KEYS);
  const parsed = parseSessionIndexRoot(root);
  validateSessionIndexCoverage(parsed);
  return parsed;
}
```
- [ ] **Step 4: Run GREEN:** `cd obsidian-plugin && npm run check`.
- [ ] **Step 5: Commit when authorized:** `git add obsidian-plugin/src/contracts obsidian-plugin/src/data obsidian-plugin/tests && git commit -m "feat: mirror v4 contracts in Obsidian"`.

---

### Task 5: Prove v2/v3/v4 compatibility and migration fixtures

**Files:**
- Create: `internal/reviewv4/migrate.go`, `migrate_test.go`
- Create: `testdata/contracts/migration/v2-*`, `v3-*`, `v4-*`
- Modify: `internal/migrationv3/plan.go`, `plan_test.go`
- Modify: `internal/reviewv2/v3_test.go`
- Modify: `internal/cli/sync.go`, `sync_test.go`
- Modify: `internal/sync/service.go`, `service_test.go`

**Interfaces:**

```go
type MigrationPreview struct {
    SourceVersion int
    TargetVersion int
    PreservedDecisionIDs []string
    DefaultedFields map[string][]string
    RequiresSessionIndex bool
}
func PreviewMigration(review, history, ledger []byte) (MigrationPreview, error)
func MigrateAcceptedV3(review, history, ledger, sessionIndex []byte) (reviewv4.Accepted, error)
func MigrationPreviewDigest(MigrationPreview) string
```

- [ ] **Step 1: Write RED fixtures for the complete matrix:** v2 stays readable/migratable; v3 requires explicit dry-run and a bound session index; v4 opens directly; newer/partial/mixed generations fail closed.

```go
func TestMigrateAcceptedV3PreservesDecisionWithoutInventingFields(t *testing.T) {
    got := migrateDecision(reviewv2.Decision{ID: "decision-1", Title: "Keep v3", Rationale: "because", Impact: "scope"})
    if got.Provenance != "migrated" || got.Pinned || len(got.Supersedes) != 0 || len(got.SessionRefs) != 0 { t.Fatalf("invented migration data: %+v", got) }
}
```

- [ ] **Step 2: Run RED:** `go test ./internal/reviewv4 ./internal/migrationv3 ./internal/reviewv2 -run 'Migration|Compatibility' -count=1`.
- [ ] **Step 3: Implement pure preview/migration functions.** Map old decisions to `kind=decision`, `status=active` unless the old status maps exactly, empty new fields, `provenance=migrated`, `pinned=false`, `revision=1`; never infer reasons, relationships, or Sessions.

```go
func migrateDecision(old reviewv2.Decision) Decision {
    return Decision{ID: old.ID, Kind: "decision", OccurredAt: old.OccurredAt, Title: old.Title, Rationale: old.Rationale, Impact: old.Impact, Status: mapLegacyStatus(old.Status), Supersedes: []string{}, MilestoneIDs: []string{}, SessionRefs: []SessionKey{}, Provenance: "migrated", Pinned: false, Revision: 1}
}
```

- [ ] **Step 4: Add explicit CLI migration confirmation.** `sync --dry-run --json` returns the preview and digest without writes. `sync --confirm-migration --expected-preview-digest <digest> --json` recomputes under the project lock and returns `migration_preview_stale` if source bytes, target preimages, generation, or SessionView dependencies changed. Plain `sync` reports `migration_required` for v3.

```go
if request.ConfirmMigration {
    current := buildMigrationPreviewUnderLock(request.ProjectID)
    if MigrationPreviewDigest(current) != request.ExpectedPreviewDigest { return Result{}, contractError("migration_preview_stale", "migration preview changed") }
    return applyMigrationPlan(ctx, current)
}
```

- [ ] **Step 5: Run full gates:** `gofmt -w internal/reviewv4 internal/migrationv3 internal/reviewv2 internal/cli internal/sync && go test ./... && go vet ./... && go mod tidy -diff && (cd obsidian-plugin && npm run check)`.
- [ ] **Step 6: Commit when authorized:** `git add internal/reviewv4 internal/migrationv3 internal/reviewv2 internal/cli internal/sync testdata/contracts/migration && git commit -m "feat: prove v4 compatibility matrix"`.

---

### Task 6: Gate 0 completion audit

**Files:**
- Modify: `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md`
- Create: `docs/session-review/gate-0-evidence.md`

- [ ] **Step 1: Run the complete Go and plugin gates:** `go test ./... && go vet ./... && go mod tidy -diff && (cd obsidian-plugin && npm run check)`.
- [ ] **Step 2: Run the initial schema fixture tests on macOS and Windows CI.** Expected: the initial eight valid fixtures are accepted identically and their invalid fixtures are rejected with stable codes; Task 7 extends this matrix to ten.
- [ ] **Step 3: Search for forbidden placeholder tokens:** `rg -n $'\x54\x42\x44|\x54\x4f\x44\x4f|\x46\x49\x58\x4d\x45|\x69\x6d\x70\x6c\x65\x6d\x65\x6e\x74\x20\x6c\x61\x74\x65\x72|\x66\x69\x6c\x6c\x20\x69\x6e\x20\x64\x65\x74\x61\x69\x6c\x73|\x68\x61\x6e\x64\x6c\x65\x20\x65\x64\x67\x65\x20\x63\x61\x73\x65\x73|\x73\x69\x6d\x69\x6c\x61\x72\x20\x74\x6f' schemas internal/reviewv4 internal/sessionindex internal/inspect internal/annotation internal/pricing obsidian-plugin/src/contracts/review-v4.ts` and require no hit.
- [ ] **Step 4: Record exact commit, commands, pass counts, fixture list, and known non-Gate-0 work in `docs/session-review/gate-0-evidence.md`.**
- [ ] **Step 5: Mark Gate 0 complete in the spec only after every preceding check passes; commit documentation when authorized.**

---

### Task 7: Reopen Gate 0 for conversation-chain and problem-map contracts

**Files:**
- Create: `schemas/conversation-chain-v1.schema.json`
- Create: `schemas/problem-map-candidate-v1.schema.json`
- Create: `internal/conversationchain/types.go`, `codec.go`, `validate.go`, `codec_test.go`
- Create: `internal/problemmap/types.go`, `candidate_codec.go`, `validate.go`, `validate_test.go`
- Modify: `schemas/review-presentation-v4.schema.json`
- Modify: `internal/reviewv4/types.go`, `validate.go`, `codec_test.go`
- Modify: `schemas/agent-annotation-v1.schema.json`
- Modify: `internal/annotation/types.go`, `validate.go`, `validate_test.go`
- Modify: `internal/cli/contracts.go`, `contracts_test.go`
- Modify: `obsidian-plugin/src/contracts/review-v4.ts`, `src/data/contracts-v4.ts`
- Modify: `obsidian-plugin/tests/contracts-v4.test.ts`
- Create: `testdata/contracts/v4/conversation-chain-v1.{valid,invalid}.json`
- Create: `testdata/contracts/v4/problem-map-candidate-v1.{valid,invalid}.json`
- Create: `obsidian-plugin/tests/fixtures/v4/conversation-chain-v1.{valid,invalid}.json`
- Create: `obsidian-plugin/tests/fixtures/v4/problem-map-candidate-v1.{valid,invalid}.json`
- Modify: `docs/session-review/gate-0-evidence.md`

**Interfaces:**

```go
func conversationchain.Parse([]byte) (Document, error)
func conversationchain.Render(Document) ([]byte, error)
func problemmap.ParseCandidates([]byte) (CandidateStore, error)
func problemmap.RenderCandidates(CandidateStore) ([]byte, error)
func problemmap.ValidateGraph([]reviewv4.ProblemNode) error
func problemmap.PreviewMove(nodes []reviewv4.ProblemNode, problemID, newParentID string) (MovePreview, error)
```

- [ ] **Step 1: Add RED fixture parity tests for both new contracts, the expanded presentation and generic Agent annotations.** Assert user/assistant roles only, 4,096-byte persisted excerpts, authenticated source refs, exact chain dependency binding, one primary parent, no cycles, maximum two alternates/related nodes, deterministic candidates with nil Agent run, `missing` conclusions with empty text, and milestone summary annotations that use `confirmed_entity_id` without decision-only fields.

```go
func TestProblemGraphRejectsCycle(t *testing.T) {
    nodes := []reviewv4.ProblemNode{
        {ID: "p-a", PrimaryParentID: ptr("p-b")},
        {ID: "p-b", PrimaryParentID: ptr("p-a")},
    }
    if err := problemmap.ValidateGraph(nodes); err == nil { t.Fatal("accepted problem cycle") }
}
```

- [ ] **Step 2: Run RED.**

Run: `go test ./internal/conversationchain ./internal/problemmap ./internal/reviewv4 -count=1 && (cd obsidian-plugin && npx vitest run tests/contracts-v4.test.ts)`

Expected: FAIL because the new packages, schemas and parser branches do not exist.

- [ ] **Step 3: Implement strict Go codecs and presentation graph validation.** Reuse the canonical JSON helper and five stable wire error families. Normalize empty collections to arrays, reject hidden-role fields and arbitrary raw tool output keys, and calculate digests after omitting only the digest field.

```go
func ValidateConclusion(c ClosedLoopConclusion) error {
    if c.Kind == ConclusionMissing && c.Text != "" { return contractError("wire_contract_invalid", "missing conclusion contains text") }
    if c.Kind != ConclusionMissing && strings.TrimSpace(c.Text) == "" { return contractError("wire_contract_invalid", "conclusion text is required") }
    return nil
}
```

- [ ] **Step 4: Freeze the CLI grammar.** Add read-only `inspect conversation-chain`, `problems candidates list` and `evolution summary-candidates list`; add CAS write grammars for requested milestone summarization, summary confirmation, problem candidate transition, move and reorder. Reject message cursors without a turn unit, missing target IDs for apply/merge, target IDs on keep/dismiss, and incomplete sibling order arrays. Freeze the per-source read ceiling at 64 KiB and require truncation coverage instead of silent clipping.

```go
case "conversation-chain":
    return parseConversationChainArgs(args[1:])
case "problems":
    return parseProblemArgs(args[1:])
```

- [ ] **Step 5: Mirror both contracts and graph invariants in TypeScript.** Parse the new valid fixtures, reject the same invalid fixtures with the same stable family, and extend `ViewKind` to `"evolution"|"problems"|"decisions"|"sessions"|"usage"`.

```ts
export function parseConversationChainV1(source: string): ConversationChainV1;
export function parseProblemMapCandidateV1(source: string): ProblemMapCandidateV1;
export function assertProblemGraph(nodes: readonly ProblemNodeV4[]): void;
```

- [ ] **Step 6: Run the extended local Gate 0.**

Run: `gofmt -w internal/conversationchain internal/problemmap internal/reviewv4 internal/cli && go test -p 1 -timeout 5m -count=1 ./... && go vet ./... && go mod tidy -diff && (cd obsidian-plugin && npm run check) && git diff --check`

Expected: PASS; 10/10 contract fixture pairs have byte-identical Go/TypeScript copies and the ordinary zero-token tests still observe zero Agent starts.

- [ ] **Step 7: Replace the stale Gate 0 conclusion with exact extension evidence.** Record commit, commands, pass counts, the 10-contract matrix, migration fixtures, and keep the conclusion `LOCAL COMPLETE / WINDOWS CI PENDING` until a pushed commit has a successful native Windows job.

- [ ] **Step 8: Commit the reopened gate when authorized.**

```bash
git add schemas internal/conversationchain internal/problemmap internal/reviewv4 internal/cli testdata/contracts/v4 obsidian-plugin/src obsidian-plugin/tests docs/session-review/gate-0-evidence.md
git commit -m "feat: extend v4 contracts for problem context"
```
