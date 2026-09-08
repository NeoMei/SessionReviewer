# Problem Entry Contracts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Execute as the contract gate before problem service/UI implementation.

**Goal:** Make the accepted problem workflow reachable for an empty graph and give explicitly requested Agent placement a bounded asynchronous entry point.

**Architecture:** Extend the existing fixed-argv contracts, not the formal graph authority. A new human-only `apply_root` transition creates an explicitly confirmed root from an existing authenticated candidate. Explicit placement requests use the same proposal-only job lifecycle as other annotation requests; ordinary scans still use deterministic rules only.

**Tech Stack:** Existing Go CLI contracts, problemmap codecs, TypeScript CLI allowlist and fixture tests. No new dependency.

**Spec:** `docs/superpowers/specs/2026-09-04-obsidian-project-context-navigation-design.md` §§5,12.3,17.3,18.3,18.5; `2026-09-04-problem-map-placement.md` Tasks2–4. This document records bounded missing-entry reconciliation, not a replacement product design.

## Global Constraints

- The left rail contains real questions and decomposition only; top-level product categories remain in the top tabs.
- Formal structure changes require an explicit user action, exact review SHA, graph revision and candidate revision; failed CAS cannot partially write.
- Ordinary scanning starts zero Agent processes. Agent placement is proposal-only, never a formal graph write.
- No raw transcripts, hidden roles, arbitrary executable paths, shell strings or caller-supplied target files enter the command contracts.
- Existing `child|sibling|merge|keep_pending` recommendation values remain unchanged. Root creation is a human action, not an inferred recommendation.
- No real source scans, Vault edits, paid Agent calls, migration or release in this contract task.

### Task 1: Add explicit root application and bounded placement job commands

**Files:** Modify `internal/cli/contracts.go`, `contracts_test.go`; create `internal/cli/problem_entry_contracts_test.go`. Modify only the problem contract portions of `obsidian-plugin/src/cli/runner.ts` and its existing CLI contract tests when those functions are wired. The later service/UI task owns runtime invocation, job persistence and rendered controls; do not mark those complete here.

**Interfaces:** Extend existing `ProblemRequest` with `ExpectedGenerationID`, `JobID` and `ExpectedRevision`. Retain existing `ExpectedCandidateRevision`, `ExpectedProblemMapRevision` and `ExpectedReviewSHA256`. Parse these exact argument shapes:

```text
problems candidate transition --project-id <id> --candidate-id <id>
  --expected-candidate-revision <positive-int>
  --expected-problem-map-revision <nonnegative-int>
  --expected-review-sha256 <bare-sha256> --action apply_root --json

problems placement request --project-id <id> --candidate-id <id>
  --expected-candidate-revision <positive-int>
  --expected-problem-map-revision <nonnegative-int>
  --expected-generation-id <id> --json

problems placement status --project-id <id> --job-id <id> --json

problems placement cancel --project-id <id> --job-id <id>
  --expected-revision <positive-int> --json
```

- [ ] RED: a valid `apply_root` command without target must parse; the same command with any `--target-problem-id` must reject. Existing apply-child/apply-sibling/merge commands still require an existing target ID at service time, while keep/dismiss/restore still forbid it. Reject duplicate flags, unsafe IDs, unknown flags, fractional/negative/unsafe integers and malformed SHA.

```go
request, err := ParseProblemContract([]string{"candidate", "transition", "--project-id", "p", "--candidate-id", "c", "--expected-candidate-revision", "1", "--expected-problem-map-revision", "0", "--expected-review-sha256", contractTestSHA, "--action", "apply_root", "--json"})
if err != nil || request.Action != "apply_root" || request.TargetProblemID != "" {
    t.Fatalf("explicit empty-graph root entry: request=%+v err=%v", request, err)
}
```

- [ ] RED: placement request/status/cancel parse exactly their allowed fields; cross-project job lookup must remain part of the service contract. Reject stdin, a path, a prompt, an executable, a target-node override and request-only fields passed to status/cancel. Status is read-only; cancel must carry a positive job revision.
- [ ] Run `go test ./internal/cli -run 'Problem.*Contract|ProblemEntry|PlacementContract' -count=1`; preserve exact failing assertions before production edits.
- [ ] Implement a focused problem-placement parser using existing contract flag helpers. Keep recommendation JSON schema unchanged and avoid adding a fake persisted run before the service exists. Tests assert all parsed fields, not only absence of error.
- [ ] GREEN focused CLI tests and existing Go/plugin wire fixtures; scoped vet and diff check. Commit exact files and obtain independent spec/quality review before services consume the new shapes.

## Runtime acceptance carried into the existing problem service/UI tasks

- `apply_root` creates one formal node with `primary_parent_id=null`, appends to deterministic root sibling order, preserves every source turn ref and sets candidate status `applied`. It works for empty and populated graphs only after the user confirms “确认为顶层问题”. It never fires during scanning. The operation participates in shared publication/candidate crash reconciliation, with a deterministic entity ID so retries cannot create a second root.
- The existing `root` destination for move/reorder is a CLI sentinel only. Tests must resolve it to a null parent and reject collisions with an actual formal node ID where the current validator permits that spelling; do not silently choose the wrong node.
- Placement request authenticates generation, candidate revision, current graph revision and target dependencies before creating a durable run. Run identity includes project, normalized question, sorted dependency digests, rule version and proposal schema version. Identical identities return the same run without another Agent invocation; changed dependencies invalidate prior pending proposals. Failure/cancel/invalid output never advances successful dependencies.
- Placement status/cancel verify project ownership, never mutate formal Markdown and never return hidden/raw model output. UI offers explicit “请 Agent 协助归位”, warns about token use, shows status, supports cancel and offers the resulting candidate for normal human confirmation. Disabled/unconfigured Agent state has an actionable explanation; it does not launch automatically.
- The service must implement durable job reservation before spawn, terminal-state CAS and deterministic recovery of interrupted jobs using existing proposal-only lifecycle machinery. Cancellation and completion races produce one terminal state; invalid output stays a failed run, never an applied candidate.
- End-to-end tests must cover first root → child → second root → move to root → complete root reorder; stale candidate/graph/review rejection; fake Agent request → repeat request → status → cancel; and native keyboard confirmation. Contract-only GREEN is not runtime or UI acceptance.

## Reconciliation rulings

Ruling: Add `apply_root` as an explicit human action without adding a root recommendation enum — a fresh project has no valid existing target for child/sibling, and automatic hierarchy would violate the accepted authority boundary — cost if wrong is a bounded CLI/UI adjustment; no inferred structure is published.

Ruling: Placement uses project-qualified request/status/cancel commands over an existing candidate rather than accepting prompt text or arbitrary source paths — implements the accepted explicit Agent affordance while binding its evidence and cancellation to one project — cost if wrong is adapting later job wiring, not exposing raw sources or changing scan behavior.
