# Annotation Candidate Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. This replaces the obsolete ProjectState/AnnotationRevision storage interface in the decisions plan while retaining its accepted lifecycle requirements.

**Goal:** Persist dependency-bound decision, agreement and milestone-conclusion candidates privately with atomic CAS and an immutable audit history.

**Architecture:** Reuse the existing strict `annotation.StoreRecord`, `Annotation`, `Run`, Parse and Render contracts. Store canonical immutable record revisions under a private project namespace and atomically switch a small authenticated head. Public queries remain read-only; candidate actions cannot write Markdown or advance scan generations through this store.

**Tech Stack:** Existing Go annotation, strictjson, pathguard, project advisory locks and atomicfile helpers; no new dependencies.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§6,9,17.3/4,18.2/5; original `2026-09-04-decisions-and-candidates.md` Task2.

## Global Constraints

- Private candidates never enter Project/Vault sync or formal project meaning before explicit confirmation.
- Existing candidate wire bytes remain unchanged. A private head has its own schema and hash; do not replace existing StoreRecord with the old plan's illustrative ProjectState type.
- Candidate identities, source dependencies, created time, kind, target and proposal text are immutable across state transitions; an edited confirmation belongs in the HumanPresentation result, not rewritten candidate evidence.
- Confirmed, not_decision and stale candidates are terminal. Ignored may restore to pending. Milestone candidates cannot take not_decision; decision actions cannot mutate milestone candidates.
- Ordinary scan/read/rule operations start zero Agents. No extraction process, public publication, network, real sources, Vault or release in this task.
- Private root permissions, source namespace identity, bounded canonical bytes, CAS, cancellation and atomic replacement are required; read-only access must not create locks/directories or recover state by writing.

### Task 1: Persist canonical candidate revisions and guarded state transitions

**Files:** Create `internal/annotation/store.go`, `store_paths.go`, `store_transition.go` and corresponding focused tests. Reuse existing `types.go/validate.go` without public shape changes. Create `schemas/annotation-store-head-v1.schema.json` and synthetic valid/invalid head fixtures if the new persistent envelope is not already represented by an existing exact reusable head contract. Do not modify reviewjob or publication in this task.

**Interfaces:**

```go
type StoredState struct {
    Revision uint64
    Digest string
    Record StoreRecord
}
func OpenStore(dataRoot, projectID string) (*FileStore, error)
func OpenStoreReadOnly(dataRoot, projectID string) (*FileStore, error)
func (s *FileStore) Load(context.Context) (StoredState, error)
func (s *FileStore) CompareAndSwap(ctx context.Context, expectedRevision uint64, expectedDigest string, next StoreRecord) (StoredState, error)
func (s *FileStore) Close() error
```

Use `dataRoot/projects/<safe-project-id>/annotations/` with `revisions/<bare-sha256>.json`, `head.json`, and `store.lock`. A head is strict `{schema_version:1, project_id, revision, digest}`, revision1..2^53-1, digest `sha256:` plus64 lowercase hex. Record digest is SHA256 of existing canonical Render bytes. Initial CAS expects revision0 and empty digest. Missing state is typed `ErrStoreNotFound`, not a silently created empty persisted record. Return typed revision conflict containing only current revision/digest, never private candidate text.

- [ ] RED real temporary-store test: create canonical record, CAS from absent, close/reopen read-only and load identical canonical bytes/digest. Second same expected head fails conflict; a genuine second transition increments head exactly once. Identical next canonical bytes with matching expected head is a no-op, not a new revision. Prior immutable record remains unchanged after head advancement.

```go
first, err := store.CompareAndSwap(ctx, 0, "", record)
if err != nil || first.Revision != 1 { t.Fatal("initial candidate CAS failed") }
if _, err := store.CompareAndSwap(ctx, 0, "", record); !errors.Is(err, ErrCandidateRevisionConflict) {
    t.Fatal("stale candidate writer accepted")
}
```

- [ ] RED safety tests: invalid/foreign project ID, symlink or renamed namespace, non-private file/parent, corrupt record digest, truncated/duplicate/unknown head or record fields, wrong record project,64MiB+1 record, pointer revision overflow. Bound head16KiB and record64MiB before allocation/decoding. Do not repair corrupt state or substitute an older record silently.
- [ ] RED real concurrency tests: two FileStores race one expected head, exactly one successful change; losing writer leaves current head and published immutable record valid. Inject failure before immutable write, after immutable write and before head replacement; old head remains authoritative. An orphan immutable revision is harmless and is not automatically deleted. Cancellation at each checkpoint leaves no advanced head.
- [ ] RED read-only hash/filesystem snapshot: Load neither creates missing namespace nor creates a lock, chmods, recovers or changes files. ReadOnly CompareAndSwap rejects before filesystem mutation. Namespace replacement after open must not redirect a later read or write to unrelated files.
- [ ] RED lifecycle table tests: pending->confirmed/ignored/not_decision/stale; ignored->pending/stale. Reject terminal mutation, skipped/doubled candidate revision, deletion, duplicate IDs and mutation of immutable candidate fields. For milestone kind reject not_decision. A transition of one kind leaves all other-kind entries byte-identical. A new candidate has revision1, pending status and a referenced completed run; status-only transitions increment candidate revision once, while unchanged entries keep it.
- [ ] RED run preservation tests: run identity/project/extractor/prompt/dependency set/created time never changes; allowed pending->running or cancelled, running->completed/failed/cancelled, terminal unchanged. Run updates preserve candidate references and cannot delete historical runs. Failed/cancelled runs cannot introduce new candidates. Empty completed runs are preserved so successful extraction of no candidates can be represented without fake annotations.
- [ ] Run RED `go test ./internal/annotation -run 'CandidateStore|CandidateTransition|AnnotationHead' -count=1`.
- [ ] Implement exact rooted namespace creation with existing pathguard/atomicfile helpers, private immutable put, a project advisory lock and rechecked expected head immediately before atomic replacement. Read and validate the canonical next record and semantic transition before publishing the head. Do not hold a candidate lock while acquiring any future publication lock; future services own the cross-store transaction ordering.
- [ ] Keep record ordering deterministic without rewriting existing semantic arrays in ways that change prior bytes. Existing Parse/Render validates bounds and dependencies; strengthen missing UTF-8/time validation only with narrowly scoped current fixtures if proven necessary, never by silently changing the public schema. Return errors without raw file paths or candidate content in typed conflict fields.
- [ ] GREEN focused tests, `go test ./internal/annotation ./internal/strictjson ./internal/pathguard -count=1`, scoped vet, full Go suite once, diff check. Commit exact files, report RED/GREEN and immutable/read-only/CAS evidence. Independent task review precedes extraction and human-confirmation orchestration.

## Preflight

| Producer / consumer | Check |
|---|---|
| Existing StoreRecord / private head | Public canonical body unchanged; private head separately validates digest and revision |
| Current head / immutable history | New head selects a full canonical record; old evidence remains addressable and unchanged |
| Lifecycle / future human confirmation | Store proves transitions, not human authorization; publication service must independently bind confirmed entity |
| Completed extraction / candidate creation | Candidates and completed run publish in one candidate-record CAS; failed run introduces no candidate |
| Read-only queries / namespace | No writer lock, repair, chmod or directory creation on read |
| Future candidate/publication locks | Never acquire publication ownership while holding this candidate lock; reconcile confirmed entity after accepted publication |

Ruling: Use an immutable canonical StoreRecord plus versioned private CAS head instead of redefining the existing annotation wire document — preserves current validated contracts and retains audit evidence — cost if wrong is adapting the private store API, not rewriting public project data.

This task does not implement candidate extraction, watermark selection, human publication, CLI or UI. Those accepted requirements remain pending in the decisions/conclusion workflow; no claim of decision feature completion follows from private storage alone.
