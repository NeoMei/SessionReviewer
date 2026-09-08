# Historical Turn Bindings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after immutable chain CAS and retained scan/query integration, before automatic milestone projection.

**Goal:** Keep a milestone or problem's accepted historical answer bound to its original chain snapshot when the same Session is scanned again.

**Architecture:** Retain the existing provider/Session/turn identity, adding an optional exact SessionView digest to a source-turn reference. A presentation can name multiple authenticated snapshots of one Session; an unqualified legacy reference is valid only when exactly one listed snapshot supplies it. Querying a historical answer selects a manifest-retained root explicitly, never the current Session's replacement content.

**Tech Stack:** Existing Go reviewv4/problemmap/inspect contracts, canonical JSON, JSON Schema, TypeScript parsers and fixed-argv CLI. No dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§4.1,5.1,9,17.3/4,18.4. This resolves the concrete current one-snapshot-per-Session limitation, not a new history browser or migration project.

## Global Constraints

- Existing turn identities do not change. Provider and Session remain required; snapshot identity disambiguates content, not user-message identity.
- Only current or retained immutable manifest roots are queryable. An arbitrary digest or path is never an authority.
- Omitted optional fields preserve old canonical bytes. Existing legacy fixture parsing remains; no automatic migration or rewrite of prior published generations.
- New snapshot-qualified public data requires minimum reader/writer0.4.3. Old-format documents keep their existing capability floors; do not label new data as readable by an older writer.
- Maximum65536 dependency entries and256 refs per entity/segment remain; no union document with a fabricated digest.
- Zero Agent starts; no network, raw source persistence, real-Vault mutation or release within these tasks.

Candidate authority clarification2026-09-09: `problem-map-candidate-v1` has opaque dependency digests, not a full ChainDependency map. Its codec validates optional snapshot syntax, exact raw reference tuple uniqueness, conditional reader0.4.3 and canonical preservation only; it cannot resolve aliases or authenticate evidence. Presentation validates unique canonical bindings against its full dependencies. The later confirmation service must resolve candidate refs against authenticated dependencies before publishing; this task must not invent a new candidate evidence map or treat successful candidate parsing as acceptance.

Schema boundary clarification2026-09-09 (navigation spec §17.3): Go/TS retain the exact historical predicate and canonical capability floors. Standard Schema enforces structural constraints, declaration pairing, qualified-ref downgrade rejection and at least two dependencies for unqualified0.4.3; arbitrary cross-item provider/Session/view equality remains mandatory runtime validation. Reciprocal fixtures must explicitly distinguish structural schema validity from complete acceptance, including same-session two-view and distinct-session controls in presentation and nested ledger. No schema-only acceptance, custom vocabulary, wire changes or weakened runtime checks.

### Task 1: Add exact snapshot-qualified source references across shared contracts

**Files:** Modify `internal/reviewv4/types.go`, `validate.go`, `codec.go`, `document_projection.go`, `markdown_draft.go`, `markdown_render.go` and focused tests; `internal/problemmap/validate.go` and codec tests; `schemas/review-presentation-v4.schema.json`, `machine-ledger-v4.schema.json`, `problem-map-candidate-v1.schema.json`; `obsidian-plugin/src/contracts/review-v4.ts`, `src/data/contracts-v4.ts` and shared fixture/canonical tests. Create `internal/reviewv4/source_turn_binding.go`, `source_turn_binding_test.go`. Update only specific version-capability consumers that otherwise reject supported0.4.3 data; preserve older floors and their tests.

**Interfaces:** Add `SessionViewDigest string` with JSON `session_view_digest,omitempty` to existing `reviewv4.SourceTurnRef`; TypeScript equivalent optional string. Existing ChainDependency already carries exact SessionViewDigest and DependencyDigest. Add pure resolver:

```go
func ResolveSourceTurnDependency(ref SourceTurnRef, dependencies []ChainDependency) (ChainDependency, error)
```

- [ ] RED fixture tests for one legacy dependency with omitted field (identical canonical bytes), two snapshots of one provider/Session with the same stable turn ID but different answers, exact-qualified selection, wrong snapshot, missing turn, duplicate identical dependency tuple and ambiguous unqualified reference. Same native ID across providers remains separate. A ref must resolve exactly once; missing and ambiguous are errors.
- [ ] RED minimum capability tests: new qualified refs or multiple snapshots for one Session require0.4.3 presentation and containing ledger/candidate reader floors; old0.4.0/0.4.1 fixtures remain unchanged. Unsupported future versions still reject. Test Go/TS/schema agreement, unknown fields and duplicate JSON keys.
- [ ] Change dependency uniqueness to `(provider,session,session_view_digest)` and require a single exact dependency digest for that tuple. Source-turn uniqueness includes the optional snapshot coordinate; reject duplicate refs that resolve to the same canonical tuple even if one uses legacy shorthand. Aggregate closure refs and each segment must resolve to the same canonical binding.
- [ ] Preserve new fields through cloning, Markdown generated source-reference rendering, strict decode/render and canonical hash ordering. For a legacy ref with one accepted dependency, later projection may qualify it using that exact old dependency; it must not choose the newest view or rewrite human text. Do not silently qualify ambiguous input.

```go
got, err := ResolveSourceTurnDependency(SourceTurnRef{Provider:"codex", SessionID:"s", TurnUnitID:"turn-a", SessionViewDigest:oldView}, dependencies)
if err != nil || got.SessionViewDigest != oldView { t.Fatal("historical turn was rebound") }
```

- [ ] GREEN focused reviewv4/problemmap/canonical tests, full Go suite, full plugin check, vet/diff check. Commit exact files and obtain independent spec/quality review before query wiring.

### Task 2: Authenticate selected historical snapshots in conversation drilldown

**Files:** Modify `internal/inspect/conversation.go`, `conversation_retained.go` and tests; `internal/cli/contracts.go`, `inspect.go` and tests; conversation-page schema/Go/TS fixtures only for an explicit optional selection field if needed; `obsidian-plugin/src/cli/runner.ts`, contract types and tests. No milestone UI or public publication yet.

**Interfaces:** Add optional `SessionViewDigest` to `inspect.ConversationRequest`, exposed as fixed argv `--session-view-digest <digest>` for `inspect conversation-chain`; keep existing request arguments and default-current behavior. Plugin request uses optional `sessionViewDigest`. The response's existing `session_view_digest` is the actual selected snapshot, not the current index's digest.

Integration clarification after retained-query557565d: apply the last sentence only to an explicitly snapshot-qualified request. Existing unqualified responses must preserve `session_view_digest` as the current index view and, for availability-only historical fallback, `evidence_session_view_digest` as its real historical evidence view. Do not break that established distinction or default cursor behavior. With an explicit selector, require response `session_view_digest` to equal that selector and any emitted evidence-view digest to agree; current index membership remains separately authenticated inside inspect. Runner and supplied-loader/renderer identity checks must compare against `sessionViewDigest ?? expectedSessionViewDigest`, while retaining current generation/provider/Session checks. Test both forms, their cursor separation, and explicit-selection mismatches. This does not authorize selecting an arbitrary unreferenced CAS object.

Read-only source-catalog preflight2026-09-09: `sourcecatalog.ReadAuthenticated` reads the one current provider/Session record and requires its exact digest; it is not a historical-record store. Explicit historical selection must authenticate the manifest-retained chain/view before deciding whether that exact historical source record is available. If its record no longer matches after append/replacement, retain authenticated excerpts and mark the full body unavailable; do not reject the selected chain solely for this mismatch, synthesize a SourceRecord, relabel a current record, or add a new historical catalog in this task. Cursor dependency construction for retained-only results must use the selected authenticated chain/view identity without requiring invented source fields. Add an actual append/rescan test where current source remains available but the old catalog digest differs.

- [ ] RED published-store tests: new scan replaces a SessionView while a prior manifest-retained chain remains queryable by its exact view; default request selects current; wrong project/provider/Session/digest or unreferenced CAS object fails. Identical turn IDs with different answer excerpts cannot cross snapshots. Current index presence authenticates Session membership, not the historical view's contents.
- [ ] Extend already authenticated inspection context with selected chain-root/view lookup, reusing manifest graph checks. Historical active facts are verified against their own view, not current lineage. Source-backed full-body reads require that old prefix's authenticated catalog record; when unavailable, return retained excerpts with explicit unavailable-body status.
- [ ] Bind all turn/message cursors to selected snapshot and chain dependency. Reject crossing current/historical views even under one published generation. Retain64-message/1MiB response bounds, cancellation checkpoints, readonly permissions and final project/generation recheck.
- [ ] RED fixed-argv tests for optional digest in both turn-index and turn-message mode, duplicate/unknown flags, digest syntax and response mismatch. No shell execution, arbitrary path, or caller-selected executable. Existing no-digest commands must retain byte/behavior compatibility.
- [ ] GREEN inspect/CLI focused and full Go, full plugin check, read-only mutation sentinels and canonical fixtures. Independent review before closure source controls consume the new option.

## Preflight rulings

Ruling: Use optional exact snapshot qualification instead of overloading a single dependency with merged history — one Session can have accepted old and new answers with different authentication — cost if wrong is a bounded Go/TS wire extension to revise, not silently altered human provenance.

Ruling: Preserve legacy omitted-field bytes and qualify only from a uniquely authenticated old dependency during a new publication — user removed migration, not preservation of existing accepted references — cost if wrong is additional compatibility testing; no prior generation is rewritten.

| Shared interface | Producer / consumer | Check |
|---|---|---|
| Task1 internal | optional qualifier -> resolver/canonical/hash/version floor | Omission unchanged; ambiguity rejected; capability bump conditional on extension |
| Task2 internal | explicit view selector -> retained manifest root -> response/cursor | Never reads an arbitrary digest or relabels current data |
| Task1/Task2 | public qualified ref -> CLI selector | Same exact view digest; provider/Session/turn still mandatory |
| Prior CAS/Task2 | private historical roots -> authorized view/revision graph | Current Session index establishes membership only |
| Future milestone rebase | accepted source refs -> new generated fields | Preserve historical bindings before adding current dependencies |

Remaining milestone projector, human Markdown rebase and UI source controls remain separate tasks; this plan alone does not produce a populated evolution view.
