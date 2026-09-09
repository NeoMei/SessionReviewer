# Task 2 implementation report: authenticated scan milestone publication

## Outcome

Implemented the real zero-Agent scan path from prepared immutable SessionView, observation-revision, and ConversationChain objects into v4 milestone publication. Initial publication now installs exact generated milestone baselines. Existing publication authenticates the pending Markdown against the accepted ledger, rebases only the narrow scan milestone update, preserves human-owned fields/history, and publishes through the existing four-file Project/Vault transaction.

Publication now proves each public ChainDependency against an exact current or retained root in the selected private manifest, including project/provider/Session/view identity, dependency digest, and the complete ordered turn set, before a journal intent or public write. Context cancellation propagates through ProjectView, index, SessionView, observation-chunk, and chain object loads.

The no-op path returns the old accepted generation and exact bytes only when the pending Markdown, full projected milestone semantics, ledger semantics, session-index semantics, all eight Project/Vault bytes, exact ProjectViewDigest, and current/retained private roots are unchanged. It does not normalize away a changed Git branch/HEAD/dirty-state ProjectView. Its index normalization deep-clones all mutable entries, slices, and pointers before comparison.

## TDD evidence

### RED

1. `go test ./internal/contextupdate -run TestRunPublishesQualifiedMilestoneAndKeepsIdenticalScanByteStable -count=1`
   - Exit 1: initial real scan produced `timeline=[] baselines=[]`.
2. `go test ./internal/publication -run TestPublicationRejectsMilestoneWhosePrivateChainIsNotRooted -count=1`
   - Exit 1: a public-valid, re-signed milestone referencing a chain absent from the manifest was accepted.
3. `go test ./internal/reviewv4 -run TestMarkdownSensitiveSourceMasksAuthenticatedGeneratedSourceRefs -count=1`
   - Exit 1: authenticated generated high-entropy SessionView/turn references reached the human-content detector.
4. During integration, an identical real scan initially changed `docs/session-review/项目回顾.md`; diagnostic Git status showed untracked `.session-reviewer-directory.lock` files. The fixture was corrected to ignore only the scanner's generated documents and directory locks, establishing genuinely identical probe inputs.
5. Controller changed-Git positive control: an empty Git commit was initially suppressed by an identity-only shortcut (`milestone_scan_controller_test.go:137`). Exact old/new ProjectViewDigest equality was restored as a mandatory no-op condition.

### Focused GREEN

- `go test ./internal/contextupdate -run TestRunPublishesQualifiedMilestoneAndKeepsIdenticalScanByteStable -count=1 -v`
  - Exit 0; final assertion-complete rerun: `ok .../internal/contextupdate 9.302s`.
  - Covers real temporary Git project + Vault, qualified five-part machine verification, a separate user-only Session producing no milestone, exact four initial milestone baselines, all eight Project/Vault bytes stable under a later real clock, pending Vault goal/conclusion edits, appended supported evidence, Project/Vault convergence, human patch/baseline metadata, stable human-confirmed conclusion refs, post-human no-op, source disappearance/restoration with exact old refs and honest `unavailable`/`available`, cancellation before render with no public changes, and a changed tracked project state producing a new revision.
- `go test ./internal/publication -run 'Test(PublicationRejectsMilestoneWhosePrivateChainIsNotRooted|VerifyPrivateChainBindingsAuthenticatesExactRootAndTurnSet)' -count=1 -v`
  - Exit 0, `ok .../internal/publication 1.812s`.
  - Covers public-valid rooted and retained-root positives; invented dependency digest, stored-but-unreferenced object, wrong historical view, foreign provider, and missing turn negatives; pre-cancel propagation; re-signed public ledger rejection before journal intent and before all eight public destinations.
- `go test ./internal/reviewv4 -run TestMarkdownSensitiveSourceMasksAuthenticatedGeneratedSourceRefs -count=1 -v`
  - Exit 0, `ok .../internal/reviewv4 0.423s`.
  - Covers exact authenticated generated-ref masking plus tampered generated ref and unrelated human lookalike rejection/scanning.
- `go test ./internal/contextupdate ./internal/publication ./internal/syncproject ./internal/reviewv4 ./test/zerotoken -count=1`
  - Exit 0: contextupdate `24.506s`, publication `163.891s`, syncproject `72.593s`, reviewv4 `2.646s`, zerotoken `146.847s`.
- `go test ./internal/presentation -run 'TestProjectMilestones(RejectsMalformedAuthenticatedInputs|FailsClosedAboveCapacity)|TestClosureEvidenceProjectionContainsNoCanaryBytes' -count=1`
  - Exit 0, `12.807s`; maps equal native Session IDs across providers, capacity fail-closed, and raw excerpt absence.
- `go test ./internal/publication -run 'TestMarkdown(ScanNoOpStillChecksReceiptBaseAndVaultPreimages|CancellationBeforeReceiptPreservesPriorAcceptance|NewGenerationCancellationConvergesThroughNormalRecovery|EditRejectsVaultPreimageAndIndexChanges)' -count=1`
  - Exit 0, `27.994s`; maps Project/Vault/index/receipt/Base preimage conflicts and during-publication cancellation/recovery.
- `go test ./internal/syncproject -run 'Test(ReadMarkdownForScanKeepsOldAcceptanceSeparateFromVaultOnlyDraft|ReadMarkdownForScanDoesNotBlessAnEditAfterMerge|RunMarkdownCancelledBeforeSyncDoesNotRecoverOrPublish)' -count=1`
  - Exit 0, `2.196s`; maps exact four-file draft preimages, after-build human edit races, and pre-sync cancellation.
- `go test ./internal/memorystore -run TestRetentionFailsClosedOnCorruptGraphNamespaceRedirectAndPermissions -count=1`
  - Exit 0, `2.213s`; maps corrupt authenticated graph fail-closed behavior and non-mutation.

### Final gates after production freeze

- `go test ./...`
  - Exit 0. All packages passed; representative long packages: contextupdate `56.834s`, publication `350.253s`, memorystore `284.766s`, scan `419.033s`, syncproject `175.020s`, zerotoken `279.582s`.
- `go vet ./...`
  - Exit 0, no output.
- `git diff --check`
  - Exit 0, no output.
- Controller-owned frozen gates, not rerun by this worker:
  - plugin `npm run check`: 27 files / 484 tests plus lint, typecheck, build, PASS at 12:22:46;
  - private-root overlay PASS `4.578s`;
  - complete rebase overlay PASS `0.432s`;
  - actual CLI lifecycle overlay PASS `11.353s`, including initial/repeat, human edits, append, source gone/restored, privacy canary, changed Git HEAD, and repeat no-op;
  - actual scan-produced presentation/ledger/index strict TypeScript and five-tab Chrome shell at 1200/390 PASS, screenshot inspected by controller.

## Files owned

- `internal/contextupdate/milestones.go` (new): bounded current-root loader and exact active-revision join.
- `internal/contextupdate/milestones_test.go` (new): real scan lifecycle regression.
- `internal/contextupdate/service.go`: context-aware private loads and milestone wiring into both v4 branches.
- `internal/contextupdate/v4.go`: initial baselines, narrow existing rebase, exact no-op gate, defensive typed cloning.
- `internal/publication/chain_binding.go` (new): current/retained private-root proof.
- `internal/publication/chain_binding_test.go` (new): private-binding matrix and fail-before-writes proof.
- `internal/publication/service.go`: invokes private proof before publication repair/journal/public writes.
- `internal/reviewv4/markdown_document.go`, `markdown_document_test.go`: approved narrow authenticated generated-source-ref masking and negative controls.
- `internal/reviewv4/markdown_milestone_update.go`: minimally exports `AddScanMilestoneBaselines` so initial scan reuses the canonical scalar-field/hash authority; no duplicate baseline algorithm was introduced.

## Self-review

- No raw transcript text is passed to `ProjectMilestones`; only validated typed views, chains, and exact active revision bodies are used.
- Duplicate active revision IDs are compared as fully decoded typed `ObservationRevision` values with `reflect.DeepEqual`; there are no ignored JSON encoding errors and conflicting immutable bodies fail closed.
- The source-ref masker performs one bounded regex pass per authenticated generated block and O(1) exact-set membership. It does not iterate every retained turn over every block, mask arbitrary hash/turn patterns, or mask human fields/custom prose. Generated-block equality against the authenticated ledger remains mandatory.
- Public source-ref membership continues to be validated by `reviewv4.LoadProjection`; the new gate adds the missing private manifest-root/object proof and does not relax the public validator.
- Current-state goal/stage/status/next action remain empty or human-owned; scan projection creates no decisions/problems/workflow/default follow-up.
- No pricing/candidate behavior, source capability, detector inventory, model/network invocation, main Vault write, release, or capability-baseline relaxation was added.
- Full-task zero-Agent assurance comes from the real contextupdate lifecycle plus renderer/publication tests and the zerotoken suite; Gate A's narrower static import closure alone is not claimed as complete orchestration proof.

## Concerns and boundaries

- Controller found that the pre-existing Git status parser rejects a valid `?? docs/` directory record as `git_status_malformed`. This is tracked as a separate scoped fix and was not hidden by the Task 2 no-op logic.
- Source producers for commit/release/deployment/version milestones and typed rollback remain out of scope; this task publishes only fact kinds already produced and accepted by the reviewed projector.
- Plugin/browser/controller overlays are independent acceptance evidence supplied by the controller; this worker did not modify or commit controller planning/progress/release documents.

## Review fix round 1 (2026-09-09)

### Findings closed

- Moved the existing-v4 semantic no-op result behind `VerifyMarkdownScanNoOpLocked`. Under the already-held publication owner it now revalidates the accepted receipt, Markdown Base, and all four Project plus four Vault live destinations, returns hashes only from those live reads, and performs no writes. It rejects duplicate/foreign destinations, detached or re-signed index bytes, project/generation/view/index mismatches, released ownership, and cancellation before or after the final reads.
- Added task-owned zero-Agent coverage for the ordinary `contextupdate.Run` -> renderer -> four-file publication route. The test publishes one real qualified milestone, asserts zero review tokens, exact five-call allowlisted Git runtime accounting, unchanged authenticated source-prefix bytes, absence of Agent/reviewjob packages from the orchestration closure, and the exact current process launch/import inventory. The shared Gate A AST visitor remains the authority; only four typed `Run` and two Markdown-span `Start` false positives in the directly audited packages are explicitly removed. New `os.StartProcess`, `Command.Start`, method-value, import, or other process records therefore fail the inventory.
- Added a self-contained two-provider scan fixture with the same native Session ID. Synthetic named adapters drive actual `scan.Run`, `loadScanMilestones`, and `publishV4Scan`; both Project and Vault retain distinct provider-qualified SessionView digests, chain dependency digests, source refs, and milestone IDs. No production provider registration or test-only exported hook was added.

### TDD / regression evidence

- RED before the no-op API hardening: controller overlay accepted duplicate final destinations, unrelated `ExpectedIndexDigest`, mismatched `Plan.GenerationID`, and a separately re-signed `Index` detached from the canonical live plan index (`PASS` was therefore false behavior; overlay exit 1, `1.862s`). The prior review also demonstrated that the synthesized no-op branch never re-read receipt/Base/live destinations. The controller's after-read late Vault edit positive boundary was retained as an independent reproduction.
- GREEN: `go test ./internal/publication -run 'TestMarkdownScanNoOp(RevalidatesEveryLivePreimageAfterScanRead|RejectsIncompleteOrMismatchedIdentity|IsReadOnlyAndHonorsCancellationAndOwnership|ReturnsCancellationThatArrivesDuringFinalRead)' -count=1 -v` -> PASS, `14.561s`. The first test builds its preimages through actual `ReadMarkdownForScan`, then independently mutates receipt, Base, each of four Project files, and each of four Vault files; every case conflicts without overwriting the changed bytes or touching unrelated destinations. The other tests cover positive read-only hashes, API identity binding, pre-cancel, late cancel, and released owner.
- GREEN additional integration coverage: `go test ./internal/contextupdate -run 'TestScanPublicationKeepsEqualNativeSessionIDsProviderQualified|TestRunPublishesQualifiedMilestoneAndKeepsIdenticalScanByteStable' -count=1 -v` -> PASS, `8.234s`.
- GREEN additional capability coverage: `go test ./test/zerotoken -run 'TestMarkdownV4(OrdinaryScanPublishesMilestoneWithoutAgentStart|ContextUpdateUsesProvidedProcessRecorder)' -count=1 -v` -> PASS, `3.666s`.
- GREEN affected suites after freeze: `go test ./internal/contextupdate ./internal/publication ./internal/syncproject ./internal/reviewv4 ./test/zerotoken -count=1` -> PASS: contextupdate `32.022s`, publication `181.043s`, syncproject `75.140s`, reviewv4 `3.573s`, zerotoken `157.449s`.
- GREEN race/API gate: `go test -race ./internal/publication -run 'TestMarkdownScanNoOp(RevalidatesEveryLivePreimageAfterScanRead|RejectsIncompleteOrMismatchedIdentity|IsReadOnlyAndHonorsCancellationAndOwnership|ReturnsCancellationThatArrivesDuringFinalRead)' -count=1` -> PASS, `23.401s`.
- GREEN static/final checks: `go vet ./...` -> exit 0/no output; `git diff --check` -> exit 0/no output.
- Controller independent frozen evidence, not rerun by this worker: final no-op overlay `PASS 3.329s`, actual CLI lifecycle with final no-op path `PASS 7.048s`, and plugin `484` tests plus lint/typecheck/build PASS.

### Files and self-review

- Production: `internal/contextupdate/v4.go`, `internal/publication/markdown.go`.
- Tests: `internal/contextupdate/provider_collision_test.go`, `internal/publication/markdown_test.go`, `test/zerotoken/gate_a_test.go`, `test/zerotoken/markdown_v4_test.go`.
- Report: this file. Final commit subject: `fix: close scan publication review findings` (SHA reported in the immutable handoff).
- The no-op verifier consumes the existing `MarkdownSyncPlan` rather than adding another preimage type. It narrows that interface by requiring exactly the four canonical files and binds the supplied index bytes to the canonical index file, parsed project/generation/view/digest, accepted receipt index guard, and live bytes. `ProjectViewDigest` remains an exact equality gate; changed Git facts still publish normally.
- No capability baseline was relaxed: the new process assertion derives current records with the existing cross-platform visitor and fails on additions. No model/network/provider capability, public configuration seam, production adapter registration, pricing path, Vault outside the temporary fixtures, release, merge, or push was added.
- Concern boundary unchanged: native Claude/OpenCode discovery/acceptance and full-product release acceptance remain separate controller tasks.
