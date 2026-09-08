# Current Markdown Initialization Reuse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute after the active retained-conversation query task has passed independent review. This is not the fresh-init bootstrap task.

**Goal:** Repeating `init --write` on the same configured, current Markdown project succeeds without changing its published files or accepting pending human edits.

**Architecture:** Share one pure, draft-aware four-file identity parser between current format detection and initialization. The initializer recognizes current files before its legacy reader, but current public files alone never authorize rebuilding private state in another data directory. Fresh-project creation and legacy behavior remain unchanged in this bounded correction.

**Tech Stack:** Existing Go reviewv4/sessionindex/pathguard/project/syncproject and temporary CLI fixtures; no new dependency.

**Spec:** `docs/superpowers/specs/2026-09-05-v4-human-markdown-codec-design.md` §§4,7,8,10,12; `docs/verification/2026-09-08-release-execution.md` current-format reuse blocker.

## Global Constraints

- Human Markdown is authoritative for allowed editable fields; a pending draft is not an accepted publication.
- Initialization must not rewrite, delete, migrate, accept, or regenerate the four existing projection files or their Vault copies.
- Public format/identity validation is not private manifest, accepted receipt, or merge Base proof. Do not fabricate those objects.
- Preserve pinned physical roots, bounded regular-file reads, no-overwrite, exact project/Vault mapping and lock/revalidation safeguards.
- Two Markdown documents and ledger remain bounded to64MiB each; index uses existing65536-entry/64MiB bounds.
- No Agent start, network, real source/Vault access, push or release in implementation/tests. User excluded old-project migration and cross-platform acceptance expansion, not existing safeguards.

### Task 1: Reuse authenticated current-format identity without modifying its draft

**Files:** Create `internal/reviewv4/markdown_projection.go`, `markdown_projection_test.go`; modify `internal/syncproject/format.go` and `format_test.go`, `internal/project/init.go` and `init_test.go`; add a focused real lifecycle test in `internal/cli/init_current_test.go`. Do not change `contextupdate` or fresh initialization seed behavior in this task.

**Interfaces:** New pure type and helper:

```go
type MarkdownDraftProjection struct {
    Ledger MachineLedger
    Draft MarkdownDraft
    SessionIndex sessionindex.Document
}
func ParseMarkdownDraftProjection(review, history, ledger, index []byte) (MarkdownDraftProjection, error)
```

- [ ] Add RED helper tests from `testdata/contracts/v4/markdown/{review.md,history.md,ledger.json,index.json}`. Parse accepted fixtures; edit goal with `ParseMarkdownDocument("项目回顾.md", review)` and `ReplaceFields(map[FieldKey]string{{Entity:"project-overview",Name:"goal"}:"human pending goal"})`; confirm helper preserves that draft and Project/generation/index identity while `LoadProjection` still rejects it. Read fixture bytes via existing `mustRead` test helper.

```go
parsed, err := ParseMarkdownDraftProjection(edited, history, ledger, index)
if err != nil || parsed.Draft.Presentation.CurrentState.Goal != "human pending goal" {
    t.Fatalf("pending draft identity: %v", err)
}
if _, err := LoadProjection(edited, history, ledger, index); err == nil {
    t.Fatal("draft was misrepresented as an accepted publication")
}
```

- [ ] Add negative controls for missing index, wrong index Project/generation/ProjectView/digest (re-render otherwise valid mutated indexes with `sessionindex.Render`), invalid ledger self-digest, absent document projection, changed reserved frontmatter, generated-region edit, and malformed UTF-8. Each has an otherwise valid positive control; no test may fail only because its fixture checksum was never rebuilt.
- [ ] Run RED `go test ./internal/reviewv4 -run 'MarkdownDraftProjection' -count=1`. Implement by composing existing validators, not duplicating Markdown parsing:

```go
l, err := DecodeLedger(ledger)
if err != nil { return MarkdownDraftProjection{}, err }
if l.DocumentProjection == nil { return MarkdownDraftProjection{}, errors.New("current Markdown projection is required") }
d, err := ParseMarkdownDraft(MarkdownPair{Review:review,History:history}, l)
if err != nil { return MarkdownDraftProjection{}, err }
i, err := sessionindex.Parse(index)
if err != nil { return MarkdownDraftProjection{}, err }
if i.ProjectID != d.Presentation.ProjectID || i.GenerationID != d.Presentation.GenerationID ||
   i.ProjectViewDigest != d.Presentation.ProjectViewDigest || i.Digest != l.SyncHashes.SessionIndexDigest {
    return MarkdownDraftProjection{}, errors.New("Markdown index binding mismatch")
}
return MarkdownDraftProjection{Ledger:l,Draft:d,SessionIndex:i}, nil
```

- [ ] Replace `syncproject/format.go`'s current Markdown branch's duplicated draft/index checks with the helper. Preserve the old JSON-v4 `LoadProjection` branch and v2/v3/diagnostic-status behavior. Add current pending-draft positive and malformed/mismatched-index negative format tests; run `go test ./internal/syncproject -run 'DetectFormat|Markdown' -count=1`.
- [ ] Add initialization RED tests using temporary current four-file fixtures and a matching existing config mapping. Existing fixture paths map review/history to `reviewv2.ReviewRelativePath`/`HistoryRelativePath`, ledger to `MachineLedgerRelativePath`, index to `docs/session-review/.session-reviewer/session-index.json`. Repeat init with `Random:errorReader{}` so accidental new identity generation fails. Assert exact before/after bytes for all four files, config, and Vault copies, both accepted and goal-edited draft variants.
- [ ] Inventory all four target paths through the already pinned `os.Root`. None means existing fresh branch; complete current four-file set must pass the shared parser; all other partial sets that include an index fail. Legacy three-file set without an index continues through the existing legacy reader. Bound every read and reject redirected/nonregular files with existing `pathguard.ReadStableRegularRootFile` semantics. Do not classify invalid current data as an absent projection.
- [ ] Carry an internal current-format flag alongside the resolved Project ID. Require an existing same-physical-root, same-Vault mapping for current-format reuse. If mapping is absent or the data root is new, return the existing identity-conflict error with a bounded private-state-recovery explanation; do not construct an empty private scaffold as evidence that public current files were accepted there. This task does not implement lost-fragment/private-state recovery.
- [ ] Preserve the current four-file preimages across all initialization mutation callbacks and recheck pinned physical identity. A concurrently edited file is preserved; fail with `ErrInitializationStateChanged` before publishing a changed mapping. Do not accept a newly changed draft merely because both old/new text are structurally valid. Cover concurrent creation, deletion, goal edit, index replacement and root redirection with existing injected hooks, asserting no public overwrite.
- [ ] Negative init tests: mapped/current Project ID mismatch, wrong Vault, any missing current file, malformed ledger, invalid index binding, unsupported minimum version, current/legacy splice, and no-mapping/new-data attempt. Preserve existing legacy tests unchanged; the newly recognized current case must not weaken prior refusal cases.
- [ ] Real CLI regression must use real services: seed only a temp config, valid UUID Codex rollout and empty temp git commit; `scan` publishes the real four files/manifest/receipt, then `init --write` reports reuse. Repeat with an allowed project goal edit; assert raw bytes unchanged by init, then rescan through the existing merge path and verify the human goal persists in both Project and Vault. No handcrafted publication proof. Controller external `TestControllerPublishedV4InitReuse` in `/tmp/session-reviewer-scan-chain-qa.6VBnCG/new-project-cli-overlay.json` is the independent RED to rerun, not a dependency of repository tests.
- [ ] GREEN focused `go test ./internal/reviewv4 ./internal/project ./internal/syncproject ./internal/cli -run 'MarkdownDraftProjection|Current.*Init|Init.*Current|Initialize|DetectFormat' -count=1`, scoped vet, full `go test ./... -count=1`, full plugin check, diff check. Report exact output and no-mutation evidence, commit only task files, then independent spec and quality review.

## Preflight / self-review

| Shared boundary | Producer/consumer agreement |
|---|---|
| reviewv4 helper / format detection | Draft-aware binding, not accepted-state reads; old formats unchanged |
| reviewv4 helper / initializer | Exact four-file identity before legacy reader; no import of syncproject/memorystore into project |
| current public identity / config | Reuse only existing exact mapping; cannot reconstruct private state from public files |
| init callbacks / human draft | Exact preimages and physical identity rechecked; no document writes or draft acceptance |
| fresh initialization / current reuse | Fresh seed defect remains separate and cannot be silently marked fixed by this task |

The helper closes one pure codec gap and both consumers use it; tests include a real publication route, not only synthetic hashes. This plan does not choose the pending new-project contract or introduce a permanent identity sidecar. Source loss, provider parity, milestones, problems, decisions, pricing and full native/release gates remain open.
