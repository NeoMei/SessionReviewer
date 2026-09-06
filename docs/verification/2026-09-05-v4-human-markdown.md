# V4 human Markdown codec verification

Date: 2026-09-06

## Current follow-up — authorized N1/N2 repair in progress, not debt-free

The user explicitly removed cross-platform CI and real legacy-project migration
from this batch's delivery gates, choosing separately initiated rescans for old
projects. Those checks are excluded by decision, not marked passed. No rescan,
deletion or overwrite of real human content is authorized by this change.

Approved follow-up Tasks 13–16 start from 5f70657: fix the three known Minor debts
(safe file-specific read diagnostics, YAML binding presentation preservation,
indexed bulk field edits), verify cold-start no-runtime behavior, independently
review and rerun the complete local gate. Earlier completed-batch results below
remain bound to their original source; they are not this follow-up's evidence.

Previous blocked source: `90e8e62fbed365a45295953d1b978c51cbfd31b4`. Original read-diagnostic,
YAML-binding and bulk-index debts are fixed and task-reviewed; cold-start native
and automated coverage is established. Final fix re-review nevertheless found
two new Important flow-YAML regressions, independently reproduced below. The
branch is not approved for integration and remaining technical debt is not cleared.
The prescribed single final repair/re-review wave is exhausted; no second wave
was silently started. A new bounded continuation is required for N1/N2, followed
by complete frozen-source verification. Scope exclusions remain unchanged.

The user subsequently authorized that bounded continuation with “好的继续”.
Task 17 starts at scope commit `3e5fe7d` (unchanged production source `90e8e62`).
The controller re-ran the independent read-only probe: both legal-input cases
still pass the core codec but fail sync entry indexing, and consecutive suffix
deletion still fails the overlap guard. The existing syncdoc suite passes using
valid Go cache, demonstrating its missing coverage rather than refuting the
defects. New regression-first repair, independent review and the complete local
gate are in progress; no completion or integration is implied by this approval.

Latest Task 17 checkpoint: source `9b0ab71`, second task fix round in progress.
Original N1/N2 reproductions now pass after `418498a`/`13f38c8`; task review
found quoted `,#` token ambiguity, fixed by validated quoted-node spans in
`ff1e397`. Its disclosed nested-value separator-comment duplication and edited
comment loss were treated as in-scope, regression-tested and corrected in
`6dee24d`/`9b0ab71`, not deferred. Final affected-package run on that candidate:
syncdoc 1.118s, syncproject 60.741s, publication 140.533s; 23 focused syncdoc
variants and two authenticated publication tests also passed.

Scoped re-review nevertheless found two new Important combinations: changing a
surviving nested value while deleting the final following keys can lose its
unchanged separator comment (or reject overlapping edits if the comment changes);
legal `,#comment` without intervening whitespace can be misread as structural
punctuation. The controller independently reproduced successful rendering with
the comment absent, changed-comment overlap rejection, and core-codec acceptance
followed by sync-parser rejection of the no-space comment. No actual publication
of the comment-loss result or real-user data change was attempted. Both are
assigned to Task 17 fix round 2; the final ordered seven-command gate has not
started, and this branch is still not approved or debt-free.

The same `9b0ab71` CLI was built and run only against the approved synthetic
fixture (SHA256 `0d137ea2db5b9d725063bfbae6aad3c927c738ea2ab4f436bd16e84e5a21192d`).
Status -> scan -> sync -> status all exited0 with no stderr, all154 Sessions were
indexed, issues/tokens were0, and all eight public file hashes and mtimes stayed
unchanged. That repeat-scan/no-op receipt does not refute the newly discovered
editing combinations. No merge, push, release or production installation.

### Task 13 — read diagnostics closed

Commits 8183b01 and b28f099 preserve the failed fixed relative document and a
closed missing/permission-denied/read-failed category, including throwing error
getters/proxies, without exposing source text. First-load and same-project stale
UI remain read-only; the generic stale explanation no longer falsely attributes
IO failures to identity/revision mismatch. Independent review and one scoped
re-review closed both findings with no new finding. Original full plugin check
passed 235 tests; final fix passed focused 41 tests, lint and build. The final
whole-source full check is still pending.

Native synthetic ColdStartVault on b28f099 used a fresh isolated Obsidian 1.13.7
process/profile with all discovered executable candidates inaccessible, no
preferred runtime and no selected runner. The actual renderer confirmed the
home candidate denied and all system/PATH candidates absent. Read-only warning,
current goal, milestone 16, manual read-only refresh and native-history reading
worked. The human conclusion and independent verification text stayed visible;
four public file hashes were unchanged. Removing only the copied index showed
the exact relative missing-file reason and truthful stale banner, then its exact
bytes were restored. Candidate bundle SHA256:
2372870c681bda0d2b6ae87bfbff2d893afcc0172e05953bf1af9fd41cb84306.
This closes the previously unobserved all-candidates-unavailable startup case
for that plugin candidate; final-source hash equivalence/repeat remains required.
No installed CLI, production Vault or regular Obsidian profile was modified.

### Task 14 — YAML binding presentation closed

Commit dbeaa03 replaces only changed validated scalar value spans, retaining
quoted keys, tags, comments, spacing, flow delimiters and existing line endings.
No-op and unchanged scalar spellings remain byte-identical. Actual RED exposed
quoted-key refusal, unwanted normalization and block-scalar corruption; final
focused render/parse, sensitive-identity and malformed-YAML tests passed, followed
by complete reviewv4 and syncdoc tests. Independent review: approved, no findings.

The fixture-bound CLI was also exercised against the existing authorized
Project/Vault experiment: only Project frontmatter presentation was edited to
quoted keys/tagged values with trailing comments. Real sync exited0 and advanced
revision8 to9 while retaining the exact presentation on both sides. All body
bytes beyond frontmatter remained identical to the pre-test copies, all four
Project/Vault files matched and status returned in_sync1. CLI SHA256:
7929afc4f4cfe48d3c5407fb923c1f818039d3c46994984db9a88af223bce4ba.
Accepted experiment review/history/ledger/index hashes respectively:
cea9ab71245f534e19df765c57620678edcba710909e9d4ecac13670544dc1cc,
cc0a816f111c6c9abbbef36ed8fd4df845ecaacfbf1455e72342415872a19f2d,
72f45540e69e37682e360e74d3514c2d9df1f18b4ca9f2f7d7b34fa4eb022a21,
a94ee8eb1b4a018bb73f38720abce2188490ce375609e27bf41d333cba67cd1d.
An initial unsupported sync --json invocation exited2 before mutation; the
successful sync used the documented plain command. No real project was scanned.

### Task 15 — indexed bulk edits closed

Implementation commit b347274 changes only four reviewv4 Go files. Per-operation
live-field and semantic metadata indexes replace repeated entity/baseline/patch
searches; validation, original identity/hash, no-op and historical orphan rules
remain required. The maximum valid 65,536-field Apply plus baseline-carry test
passed in1.207s package time; the pre-fix Apply experiment exceeded an external
45s timeout. The committed test has no wall-clock threshold.

On the same Apple M5 host, fixed three-iteration benchmarks repeated three times
showed full Apply at512 fields changing from2.361–2.877s,93.47MB and2.103M
allocations to2.630–2.765ms,2.139MB and11.9K allocations. At4096 fields the
optimized Apply took20.26–20.80ms. Baseline carry at512 fields changed from
3.799–3.850ms to0.473–0.486ms; its operation-local allocations increased from
679928B to1406778B. This explicit memory tradeoff removes repeated searches,
not all validation/sorting cost; Apply still includes final deterministic sort.

Complete affected reviewv4/contextupdate/syncdoc tests and focused MarkdownV4 /
GateB zero-token integration passed. Independent task review approved spec and
quality with no Critical/Important/Minor findings. The surrounding byte
preservation, human precedence, failure atomicity and authentication gates remain
in the final whole-flow suite rather than being inferred from the index diff.

The controller also built the candidate CLI at clean2bddfc7 (code b347274),
SHA256 c8dcc079e561dd30eddab4263b72ad935a55d7370f17add363c7ce4a20c3d7f5,
and ran actual synthetic-project status→scan→sync→status. All four commands
exited0 with empty stderr. The scan reported154 source/indexed Sessions,
0 issues and0 review tokens, same generation; final status was in_sync1.
All eight public files retained exact bytes and mtimes, including revision9's
quoted/tagged YAML bindings and comments. No production project was scanned.
Cold-start automated coverage and the new final frozen-source full gate remain
pending. These measurements are not a whole-repository completion or release claim.

### Task 16 — cold-start source and native evidence closed

Tests-only commit5affeb3 adds real discoverRuntime all-candidate absence and
second-call freshness checks, then exercises plugin onload→registered real
ProjectEvolutionView with a public-valid v4 fixture. Both native document
buttons work, the view remains pending-private/read-only with a clear runtime
diagnostic, and no structure/acceptance/scan or Vault-write path is invoked.
The initial focused run was GREEN (2files/6tests): no product defect was
reproduced. First full check found a missing test-only type annotation; after
that correction, full plugin lint/test/build passed19files/242tests.

Independent review approved the coverage; its cleanup Minor was fixed by5f1835e
and scoped re-review closed it without new findings. The production plugin has SHA256
2372870c681bda0d2b6ae87bfbff2d893afcc0172e05953bf1af9fd41cb84306,
identical to the previously verified real no-runtime native fixture. There are
no plugin source/dependency changes since b28f099, nor Go source/module changes
since b347274. Thus native UI and synthetic CLI evidence have explicit code
equivalence; the final full-suite run is still required.

### Final branch review — one cross-layer correction required

At frozen5f1835e, all original three Minor debts were triaged as addressed.
Task16's independent review found only lifecycle-test cleanup; fix5f1835e closes
the view in finally and checks all four watcher disposals. Scoped re-review
closed that finding with no new issues.

The single whole-branch review found0 Critical,1 Important,0 new Minor. The
Important finding is supported flow-style YAML custom-field merging: the shell
adapter indexes each top-level entry by whole physical lines, so entries sharing
one line have empty/overlapping spans. A Vault-only custom-field edit can drop
fixed bindings from the intermediate merge and fail its final parse. This refuses
a legitimate edit before publication; it is not evidence of corrupted files
being published. Binding-only YAML tests did not exercise this merge boundary.
A bounded source-span correction with authenticated sync/status/reopen regression
was implemented in90e8e62; its remaining re-review blockers are recorded below.
No entire-mapping normalization or validator weakening was allowed.

The preliminary full Go command at the same clean HEAD passed (approximately
312.9s;59 package result lines,41 cached and3 no-test packages). The ordinary
large-Session test was not excluded; scan reported a cached package result.
Commands2–7 were not launched after the review finding. Therefore the full
ordered gate is incomplete, not passed. A fresh ordered sequence will follow
the reviewed correction. No merge, push, release or production mutation occurred.

### Final fix and scoped re-review — N1/N2 remain open

Commit90e8e62 changes only the v4 frontmatter index/rewriter and adjacent
syncdoc/publication tests. Actual RED reproduced the original invalid merged
document in both direct source-unit merge and authenticated StatusMarkdown.
Focused GREEN covered first/middle/last custom edits, nested values, multiline
flow, CRLF/comments, add/remove, authenticated status→sync→reopen, no-op and
invalid-edit no-write protection. A Chinese custom value precedes revision and
generation in the integration fixture. Complete uncached affected
syncdoc/syncproject/publication tests passed (0.734s/50.490s/112.631s).

The single scoped re-review marked original I1 addressed but found two new
Important blockers, not optional polish:

| ID | Confirmed regression | Required next correction |
|---|---|---|
| N1 | The new flow scanner treats an internal quote in a plain scalar (`team's`) as a quoted token, and rejects a valid trailing comma (`custom_owner: keep,}`). Even no-op ParseV4/status can fail. | Use YAML-consistent token boundaries and handle the optional final separator without normalizing untouched bytes. Cover plain internal quotes, trailing commas and actual merge/no-op paths. |
| N2 | Removing the last two consecutive custom entries emits overlapping byte edits and fails its overlap guard. | Derive delimiter ownership from surviving entries or coalesce adjacent removals; cover deleted suffixes and preserve all surviving bindings. |

The controller independently ran an ignored, read-only Go probe against the
checked-in synthetic Markdown/ledger fixture at90e8e62. It performed no fixture
or accepted-state write. Exact final probe output:

```text
control: codec=<nil> sync=<nil>
plain_apostrophe: codec=<nil> sync=invalid sync document: invalid v4 Markdown flow frontmatter entry boundary
trailing_comma: codec=<nil> sync=invalid sync document: invalid v4 Markdown flow frontmatter entry boundary
adjacent_delete_input: <nil>
adjacent_delete_merge: invalid sync document: overlapping v4 Markdown flow frontmatter edits
```

An initial diagnostic attempt passed generic Units instead of SemanticUnits to
the deletion API and correctly received `machine-reserved field changed`; it
was corrected before the N2 result above. Neither rejection demonstrates corrupt
publication: current guards refuse the work. The valid-input compatibility
regressions remain real and were not downgraded or silently deferred.

The latest clean90e8e62 `go test ./...` completed with exit0:59 package lines,
42 cached,3 no-test packages,14 fresh timings; zerotoken325.962s. The ordinary
large-Session test was not excluded (scan package result was cached). Commands
2–7 of the ordered final gate were not started after the scoped findings, so
vet/tidy/exactGateA/race/diff/plugin are not a completed final sequence on this
candidate. Earlier plugin242/full-source receipts remain source-specific.

The controller's final candidate CLI build has SHA256
1bfa731ad4bf9c6908f19fe2e5846fab553f6feb1b4d4044188f6d5cb35f2caa.
The existing synthetic154-session status→scan→sync→status repeat still passed,
with zero issues/review tokens and all eight public bytes/mtimes unchanged.
This block-style/no-op fixture does not exercise or refute N1/N2. Plugin bundle
and cold-start fixture file hashes remain identical to the prior native receipt.

No merge, push, release, installed CLI/plugin replacement or real-project scan
was performed. The named worktree, diagnostics, review reports and experimental
fixtures are preserved. A new bounded repair batch is needed; original Session
Index Tasks4–7 remain separate unfinished feature work.

## Historical checkpoint — preceding local corrective batch complete

Source: `6ad0c05a56eb097a27c1c044f5b0f43bc49e8c7e`. Complete ordered tests ran
on unchanged clean `da3d2df332d9b923b226fe6120e4a0249cd6cf00` (one docs-only
commit after that source). All seven local gates passed. Task reviews and the
single final scoped review closed all Important findings; three Minor remain.
Final-source native experiment passed already-open-view edit/sync recovery,
conflict/generated-region refusal, runtime-loss/manual-refresh recovery and
154-Session zero-token scan with paired hashes and complete history.

This completes this corrective batch's implementation and local verification,
not final delivery or all technical-debt cleanup. Native platform CI, applicable
real legacy-project migration, cold-start discovery with every runtime absent,
and the three Minor items remain open. No merge, push, release or production
plugin/Vault replacement occurred. The branch and experiment evidence are kept.
Details and exact final receipts are at the end; earlier entries below are
historical, including failed candidates, and are not the current verdict.

## Historical M9 starting checkpoint

Source commit: `4357dc15e582a85e289e1a8a7decc08092c38956`

Status: `DONE_WITH_CONCERNS` (not deliverable)

This record separates automated integration, native Markdown behavior, plugin UI,
native CI, migration, and GitHub state. A pass in one row does not substitute for
another.

## Automated integration

The M9 end-to-end regression reuses the Gate B fixture and exercises 154 synthetic
Session sources, 16 validator-accepted milestones, one decision, and a formal
problem graph. It edits the goal, decision rationale, milestone conclusion, and
custom Markdown shell, then runs the real deterministic scan, Markdown sync, and
accepted-projection reopen.

The regression verifies:

- exact Project/Vault equality for review, history, ledger, and Session index;
- preservation of all 154 Sessions and the first and last of 16 milestones;
- a `human_confirmed` conclusion without changing machine verification, source
  references, or the formal problem graph;
- private manifest binding to the public Project view and Session index;
- byte-identical and mtime-identical later-clock reopen. At this source commit,
  the snapshot was taken after the same-input reopen, so it did not yet prove an
  immediate before/after comparison for that first reopen;
- `ReviewRunTokens == 0` and only the existing allowlisted Git probes through an
  injected deny-by-default recorder.

The focused command passed:

```text
go test ./test/zerotoken -run 'MarkdownV4|GateB' -count=1
```

Exit `0`: `ok github.com/neomei/SessionReviewer/test/zerotoken 44.269s`.

Gate A passed at the same source commit:

```text
go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1
```

Exit `0`: `ok github.com/neomei/SessionReviewer/test/zerotoken 35.583s`.
The reviewed frozen-baseline delta is 3 transitive paths, 32 direct import edges,
and 29 conservative capability records. The 29 records are 12 reflection
imports/calls and 17 `.Output`/`.Start` struct-selector references; inspection
found no new executable or network path. Baselines were not blindly regenerated.

## Stable-HEAD verification

The prescribed sequence was started from the clean source commit above:

| Command | Exit | Evidence |
|---|---:|---|
| `go test ./...` | 1 | Eight shared-corpus parser-outcome assertions failed; all other emitted packages, including `test/zerotoken`, passed. |
| `go vet ./...` | 0 | No output. |
| `go mod tidy -diff` | 0 | No output. |
| Gate A command above | 0 | `35.583s`. |
| `go test -race -timeout 30m ./... -skip '^TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB$'` | interrupted / incomplete | The run reproduced the same eight corpus failures. With the non-race suite already definitively failed, the controller directed an operator interrupt. The shell returned `1` after Ctrl-C with `signal: interrupt`; this is neither a pass nor an ordinary completed test failure. Last output: `internal/scan FAIL 921.811s`, `scanjob 7.888s`, `session 2.214s`, `sessionindex 24.795s`, `sessionview 1.722s`. |
| `git diff --check` | 0 | No output; this was run while race was active and must be rerun after any source fix. |
| `obsidian-plugin: npm run check` | 0 | ESLint, 19 test files / 186 tests, TypeScript, and production build passed. |

The eight failing corpus cases are:

- `draft-duplicate-baseline`
- `draft-invalid-baseline-hash`
- `draft-baseline-generation-mismatch`
- `draft-baseline-kind-mismatch`
- `draft-baseline-value-missing`
- `draft-baseline-values-present`
- `draft-duplicate-human-patch`
- `draft-orphan-patch-collision`

`internal/reviewv4/markdown_document_test.go:290-304` compares the result of raw
`ParseMarkdownDocument` calls with `ExpectedMarkdownCode`. These eight codes
depend on ledger baseline/patch state and are draft/projection validation errors,
so both raw review and history parsing return `nil`. This is an open test-layer
failure requiring review; no validator was weakened or source fix made in this
evidence commit.

## Persistent isolated fixture

The final-root, identity-bound fixture was provisioned with a test-only opt-in:

```text
SESSION_REVIEWER_M9_FIXTURE_ROOT='/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-session-index-v1/.superpowers/sdd/2026-09-05-v4-human-markdown-codec/native-fixture-4357dc1' go test ./test/zerotoken -run '^TestMarkdownV4EndToEnd$' -count=1 -v
```

Exit `0`: test `67.86s`, package `68.104s`. The fixture refuses a nonempty target
and any symlink ancestor, creates only at an explicitly selected absent or real
empty directory, and never deletes an existing tree. `fixture.sha256` records the
initial files; `baseline/` contains 830 regular files made before native actions.

Vault root:

```text
/Users/neomei/项目/codexprojects/SessionReviewer/.worktrees/codex-session-index-v1/.superpowers/sdd/2026-09-05-v4-human-markdown-codec/native-fixture-4357dc1/Vault
```

Initial SHA-256 values:

- review: `a139efc682ccbeb225b4a1be10163d0577ac5aa610894ef0684d05e3115c4b834`
- history: `549a9654060fafc05fbeab2118cfa473e017b5401de7aa0c46804c0b27fd6796`
- ledger: `b09163cb123687a5585a51fad53d5aeb5a2cc4ed2a876c9b44e754091ac61a76`
- index: `ea424ea8f438a635a1c7152510dfed2d907591cbf75eef6d38a3433469c7ed51`

## Native Obsidian evidence

The controller used Obsidian 1.13.7 with native Markdown source/read mode. The
candidate plugin was not installed, and no production Vault, plugin, mapping, or
ledger was changed.

Confirmed:

- both notes opened; the full history reached milestone 16 and the review showed
  16 total / 5 recent;
- native edits to goal, rationale, conclusion, and custom text survived explicit
  fixture-bound CLI `sync`, `scan`, close, and reopen;
- scan exited `0` with `154/154` Sessions, 0 issues, 0 review tokens, and generation
  `scan-64cdc115e5a96386c1efd65c59e45f96`;
- Project/Vault files matched after the accepted native edits: review
  `f8bc7fa12f626ad09ebe2b9cbebe76738673fa6837c7c5db0d63b378349069b2`,
  history `cafc9969f0a858e920b9d07fb865b5b1761e95f44ffafa83c887a41b48f2aac8`,
  ledger `4cd600c39c1329913ca2c7676cac2328bfbd4c26c8df1e897724cd9af3cfa217`,
  and unchanged index
  `ea424ea8f438a635a1c7152510dfed2d907591cbf75eef6d38a3433469c7ed51`;
- machine verification and the formal problem graph remained unchanged;
- divergent Project/Vault goal edits caused sync exit `1`, conflicts `1`,
  operations `0`, with both drafts retained and the accepted ledger unchanged;
- modifying generated verification caused sync exit `1`, preserving the draft and
  accepted ledger;
- the known conflict and machine-region edits were restored exactly, followed by
  a final fixture sync exit `0`.

The reopened native-read screenshot is stored in the ignored fixture at
`native-fixture-4357dc1/native-reopened-conclusion.png`.

This is evidence for plain native Markdown editing and explicit CLI acceptance or
rejection. It is not evidence for candidate-plugin UI, plugin conflict messaging,
plugin pending status, or the no-CLI UI.

## Open acceptance defect

The native document retains contradictory empty-state prose even after real
entities exist:

```text
暂无里程碑。
暂无决策。
暂无正式问题。
```

Fresh rendering emits these as unmarked shell text in
`internal/reviewv4/markdown_render.go` around lines 137, 165, and 207. Incremental
rendering correctly preserves unmarked/custom shell and appends typed entities,
so it has no ownership marker permitting the old placeholder to be rewritten.
Deleting arbitrary unmarked text would violate the custom-shell preservation
contract. A reviewed ownership marker or neutral initial wording is required.

## Remaining gates

- Local full Go suite: failed as described above.
- Race: the pre-fix run reproduced the corpus failures and was then interrupted;
  it is incomplete and must be rerun after correction on a stable HEAD.
- Candidate plugin UI, conflict diagnostics, pending state, and no-CLI behavior:
  not executed.
- Native macOS Intel/ARM and Windows CI: not executed because no push or PR was
  authorized; the existing workflow already includes these jobs.
- Real old-project reconstruction/migration: not executed.
- GitHub, release, deployment, and production Vault: no push, PR, CI run, merge,
  release, deployment, or production mutation occurred.
- Whole-branch review remains required after the open failures are resolved.

The correct handoff remains `DONE_WITH_CONCERNS`; the original Task 3 must not be
promoted to “implementation and local regression complete.”

## Fix round 1 evidence

Fix commit: `689fe3c` (`fix: keep markdown section guidance truthful`). The
committed verification artifact itself was introduced in `a797414` and remains in
this fix range.

The review's four Important findings were addressed as follows:

- The shared corpus now has a distinct optional `expected_document_code` for
  syntax-only parsing while retaining `expected_markdown_code` for ledger-backed
  draft/projection validation. The four syntax failures declare both fields; the
  eight baseline/patch-state cases remain draft-only expectations and still run
  through `ParseMarkdownDraft` in Go and `parseMarkdownV4` in TypeScript.
- Fresh empty documents now use neutral durable section guidance, such as
  `已接受的里程碑（如有）列于下方。`. No generated region, catalog entry, or
  heuristic deletion was added. A unit regression proves incremental typed entity
  append keeps the wording truthful and preserves unrelated custom bytes,
  including literal user-authored copies of the old placeholder strings.
- The M9 end-to-end regression asserts that the Project review/history contain no
  contradictory old placeholder beside the seeded decision, problem, and 16
  milestones.
- The idempotence regression now snapshots all eight Project/Vault files and
  mtimes before the same-input reopen, compares immediately afterward, advances
  `Now`, and compares again.

TDD RED before the renderer edit:

```text
go test ./internal/reviewv4 -run 'MarkdownDocumentSharedCorpusParserOutcomes|MarkdownRenderEmptySectionGuidance' -count=1
```

Exit `1`: the semantic split initially exposed the four syntax cases missing
`expected_document_code`; after those fixture fields were added, the focused run
failed only because fresh output still contained `暂无里程碑。`.

Focused GREEN on the amended tree:

```text
go test ./internal/reviewv4 -run 'MarkdownDocumentSharedCorpusParserOutcomes|MarkdownRenderEmptySectionGuidance' -count=1
```

Exit `0`: `ok github.com/neomei/SessionReviewer/internal/reviewv4 0.412s`.

Covering checks before the fix commit:

- `go test ./internal/reviewv4 -count=1`: exit `0`, `0.378s`.
- `go test ./test/zerotoken -run '^TestMarkdownV4EndToEnd$' -count=1`:
  exit `0`, `24.692s`.
- `go test ./test/zerotoken -run 'MarkdownV4|GateB' -count=1`: exit `0`,
  `55.076s`.
- `obsidian-plugin: npm test -- --run tests/markdown-v4.test.ts`: exit `0`,
  1 file / 44 tests, `480ms`.
- `git diff --check`: exit `0` before commit.

The original `native-fixture-4357dc1` remains historical evidence and retains its
unmarked old shell; this fix does not and must not rewrite it heuristically. A
fresh post-fix fixture is required to demonstrate that newly created documents
remain noncontradictory through the real scan/sync evolution. Pre-release data
already rendered with the old unmarked placeholder cannot be distinguished from
identical user prose, so automatic removal is intentionally unsupported pending
an explicit migration/ownership decision.

The long stable-HEAD full Go/race/vet/tidy/Gate A/npm sequence is intentionally
deferred until the scoped re-review confirms this fix. Candidate-plugin approval
and plugin/no-CLI UI acceptance also remain pending; no plugin acceptance is
claimed here.

## Follow-up verification at `64f2535` — 2026-09-06

This section supersedes the pending statuses immediately above without changing
the historical failed/interrupted results. The exact ordered suite completed at
clean `64f253563caa37db5996b3b2729e240f2170e2e0` with no source changes between
commands:

| Check | Exit | Evidence |
|---|---:|---|
| `go test ./...` | 0 | All emitted packages passed; zerotoken 223.321s; large Session test included. |
| `go vet ./...` | 0 | No output. |
| `go mod tidy -diff` | 0 | No output. |
| Gate A exact selector | 0 | zerotoken 36.617s. |
| Prescribed race command | 0 | No race report; scan 936.131s, zerotoken 530.482s. Only the previously specified large Session race exclusion applied. |
| `git diff --check .` | 0 | No output. |
| Plugin `npm run check` | 1 | 186/187 tests passed; contracts-v4 corpus-key allowlist omitted `expected_document_code`; build not reached. |

Full local acceptance remains **red**, not all-green. A separate explicit
`npm run build` subsequently exited0 to freeze the native candidate; this does
not override the failed complete check.

### Fresh fixture and authorized native candidate

The authenticated fixture was provisioned at its final isolated path at
`64f2535`; the real end-to-end test passed in 25.572s. Native Obsidian1.13.7
showed the sixteenth milestone, preserved first conclusion and verification,
neutral section guidance, and review total16/recent5 with a working complete
history link. This uses synthetic accepted milestones, not automatic semantic
promotion or real legacy migration.

The user subsequently authorized enabling the candidate plugin **only in the
experimental Vault**. The built main.js SHA256 was
`c0d5a413e98504fdeb47493b4f10280615b3c597c8007f2145f22a850573565f`.
Its native command opened the v4 read-only project view; the native-note button
opened the sole editable Markdown body. Editing the goal to
`Native plugin pending goal` in Obsidian triggered `有未同步修改 · 只读` and a
one-field pending count, without accepting the change automatically. Explicit
fixture-bound CLI sync exited0, paired review hashes matched, and the plugin
watcher cleared pending to `待私有验证 · 只读`, not accepted/ready.

A controlled generated-evidence modification caused the plugin to retain its
prior public snapshot with `已过期 · 只读` and
`markdown_generated_region_modified`; real CLI sync exited1. The exact known
evidence string was restored. All four Project/Vault file pairs were subsequently
verified byte-identical. No production Vault, installed production plugin,
source sessions or mapping was changed.

This experiment exposed a separate integration defect: the present candidate CLI
can perform ordinary v4 sync, but `sync status` still routes through the legacy
engine and fails. The plugin labels that failure `CLI 不可用`. This proves a
safe service-failure fallback, **not** absent-executable discovery or a healthy
v4 status connection. Those gates remain open.

### Whole-branch review and remaining gates

Independent review of `c7fd0bb..64f2535` found0Critical,5Important,3Minor. Important
items concern repeat editing across scan generations, genuine legacy directory
layout, read-only prepared-pointer loading, cancellation before acceptance, and
sensitive-content checks during migration. They require fixes despite the Go
suite passing. One combined fix wave is in progress; its modified working tree
has not inherited the preceding test results.

Candidate healthy-status integration, true no-CLI runtime, complete native
conflict workflow, reviewed fix HEAD full verification, native Windows/macOS CI
and applicable real old-data migration remain unproven. No push, merge, release
or deployment is claimed. Original Session Index Task3 is not promoted to local
completion by this report.

### Combined fix commit `e281a89`

The combined source/test fix wave is committed as
`e281a89c2735a27fca50698e34e284dae2c9a0bb`. It adds authenticated generation-only
baseline carry-forward, legacy-layout readonly opening, non-mutating prepared
loads, cancellation checks around accepted-receipt durability, migration
sensitive-content refusal, and the missing corpus-key validation.

The implementer's covering command
`go test ./internal/reviewv4 ./internal/contextupdate ./internal/memorystore ./internal/syncproject ./internal/publication ./internal/migrationv4 ./test/zerotoken -count=1`
passed; the real Gate B new-Session/new-generation/second-edit/sync/reopen path
passed. Plugin `npm run check` passed lint,19 files/188 tests and build. Both
before-receipt rollback and after-receipt cancellation-reporting cases passed.
These are covering results, not the required final whole-repository gate.

The single scoped final re-review is pending. All three Minor findings remain
explicitly unresolved: precise Vault read diagnostics, YAML key/comment byte
preservation during rebinding, and quadratic bulk field-edit lookup. The native
v4 status route remains unfixed; no readiness API or fake status success was
introduced.

An additional native experiment temporarily moved only the already-bound
experiment startup wrapper out of its executable path. A newly opened project
view remained readonly with its CLI-unavailable explanation and could still open
the native history. The exact wrapper was restored. This covers loss of a bound
runtime after plugin load, not cold-start discovery with every CLI candidate
absent. Production binaries were untouched.

### Scoped final review result — still blocked

The single scoped review of `64f2535..e281a89` is complete. It verifies that the
legacy-layout opener, readonly prepared loader, cancellation boundary and npm
corpus omission are addressed. It does **not** accept the full fix wave:

- **I1 Important remains:** restoring a field to its generated value removes its
  human patch but leaves a live baseline; the next generation does not carry that
  unpatched baseline, so editing again fails.
- **I5 Important remains:** raw old-JSON scanning does not inspect sensitive
  plaintext materialized by JSON escape decoding into prospective Markdown.
- **N1 Important is new:** the new scalar-only check rejects valid historical
  list/orphan baselines supported by the existing codec and migration.
- **M1–M3 remain Minor:** Vault read diagnostics, YAML binding presentation
  preservation and bulk field-application complexity are still unresolved.

The native v4 status-query integration is also still blocked. The correct state
is **not ready for integration, M9 incomplete**. Covered tests passing cannot
discharge these source-confirmed missing cases. The eventual corrected HEAD
still needs its complete ordered local gate and the separately required native,
platform and applicable migration checks.

The controller retained all source/evidence commits and stopped after the
agreed single combined fix/re-review wave. A new explicitly scoped corrective
batch is needed; no residual was waived as completed, and no push, merge or
release occurred. The reviewer also qualified one test-evidence claim: the
prepared-journal test inserts a journal before the readonly load, not between
its two checks, so mutation coverage of deleting the second check is not proven.

### Approved corrective batch — Task 10 closed (2026-09-06)

The user explicitly approved a new bounded batch for I1, N1, I5 and readonly
v4 status. Task 10 closes I1/N1 in `a4aab3b`, `bcd6378`, `508c4cb` after
independent task review and two scoped fix reviews. Carry now includes restored
live baselines, preserves authenticated historical scalar/list metadata, resolves
unique legacy entity aliases consistently in Go and TypeScript, and rejects
ambiguous aliases, semantic duplicates and mixed stored baseline/patch identities.

Actual RED tests reproduced restored-but-unpatched generation failure, historical
list rejection/generation rewrite, duplicate legacy aliases and mixed-ID linkage.
Final affected Go packages passed (`reviewv4`, `migrationv4`, `contextupdate`),
TypeScript codec passed 52 tests, and the publication-backed Gate B passed in
45.636s. Gate B checks original baseline value/hash and new live generation after
the final edit/sync/reopen. An authenticated migration output is also fed into
the real scan mapper with exact historical list metadata checks. Non-project
legacy migration/edit/restore/reopen uses production codec/ledger composition;
publication synchronization is separately covered by Gate B.

This is scoped completion only. I5 and native readonly status are still being
implemented; the final frozen-source whole-repository/race/plugin check, final
branch review and native UI gates have not yet run on the corrected branch.
The previously recorded three Minor findings, native CI and applicable real-data
migration prerequisites remain separate. No merge, push or release occurred.

### Corrective Task 11 closed; Task 12 in review (2026-09-06)

Task 11 closes I5 in `aec1430` after independent review. Migration now scans
both complete prospective Markdown documents after historical preservation,
using authenticated marker-aware sensitive-content extraction. JSON-escaped
sensitive text is rejected after decoding; machine identifiers do not become
false positives merely because they are high entropy. Actual RED cases showed
an unsafe preview and a coordinator publication; GREEN cases reject preview and
confirm with zero publisher calls and unchanged source/public/private snapshots.
Final affected `migrationv4` and `syncproject` package tests passed. The reviewer
approved the code; an intervening controller-owned documentation commit was
explicitly accounted for in review provenance.

Task 12's initial `5ddb0d2` adds typed readonly v4 status and distinguishes
runtime unavailability from status-command failure in the plugin. Its covering
Go tests and plugin check (217 tests) passed, and the frozen candidate CLI
returned `in_sync=1` on the existing experiment fixture. Independent review
nevertheless found two Important gaps: legacy malformed-v3 diagnostic dispatch,
and domain-invalid human fields masked by an otherwise ordinary conflict.
Both are in the scoped fix loop. The experiment plugin is backed up and disabled
pending the corrected candidate; this provisional CLI result is not final native
acceptance. The same-source full suite and whole-branch review are still pending.

Task 12 subsequently closes its task review in `3db7588` and `37c5e72`.
Both recorded Project/Vault draft pairs receive domain validation before conflict
reporting. A bounded status-specific discriminator preserves actual legacy
Engine diagnostics for malformed review, history and ledger while refusing
partial-v4 downgrade attempts. Actual RED/GREEN tests cover these cases and
ordinary legacy prose mentioning v4. Final covering Go packages passed:
CLI85.751s, syncproject55.351s, sync59.837s. Two scoped re-reviews closed all
Important findings with no new findings; no plugin source changed in those fixes.

Native Obsidian candidate3db7588 verified healthy status without misleading
CLI warning, a native goal edit followed by pending display and explicit sync
clearing, dual-sided conflict display/refused sync with both drafts preserved,
generated-region refusal/stale display, and loss of the chosen runtime while
native history remains readable. Its restored public file pairs match exactly.
The subsequent37c5e72 diff is legacy-only; the candidate wrapper is now rebound
to that frozen CLI for final checks. Earlier native observations remain labeled
with their actual source version, not silently promoted to final-version proof.

The corrected batch now enters whole-branch review and the exact ordered local
gate. Those results are pending. The three pre-existing Minor items, platform CI,
real old-project migration prerequisites and cold-start all-candidates-absent
runtime discovery remain separately open. This branch is not merged or published.

### Frozen corrected batch `f841907` — whole-branch gate remains red

The complete ordered local sequence ran without source changes at
`f8419070e587bc6ef48254361155c5e70f3ceaf3` (source37c5e72): full Go exit1;
vet0; tidy-diff0; Gate A0 (33.138s); full race exit1; diff-check0; plugin check0
(19 files/217 tests, lint/typecheck/build). Ordinary Go included the large-Session
test; race used only the exact pre-approved exclusion. Race ran to completion
(scan887.241s) and emitted no race-detector finding, but is **not a passing suite**.
Both failed `TestOldV4MissingIndexBindingBuildsAndPublishesAuthenticatedSuccessor`
because authenticated migration-generation identity was scanned as sensitive
human text. Prior focused results do not override this failure.

Independent whole-branch review of c7fd0bb..f841907 identifies four Important
items: that false positive; four-file cancellation advancing a pointer then
terminalizing inconsistent rollback; genuine old manifests lacking measurements
failing successor confirmation; and live plugin refresh retaining transient
status failure. The three previously recorded Minor findings remain deferred.

The final source37c5e72 native replay reproduced the last issue: native edit and
explicit fixture sync succeeded, direct status was clean, but the already-open
view retained a status-failure warning. A new view cleared it. This is a defect,
not automatic-settling acceptance. All controlled conflict/machine edits and the
experiment runtime path were restored; current accepted experiment revision7
has the same Project/Vault goal and generation.

One combined final fix wave is authorized for these four Important findings,
followed by one scoped re-review and a new frozen-source full gate. Implementation
has begun only after all seven preceding commands completed. No integration,
release, platform or real-data acceptance is implied.

### Combined final fix candidate `6ad0c05`

All four Important corrections are committed in
`6ad0c05a56eb097a27c1c044f5b0f43bc49e8c7e`. Exact authenticated identity scalar
values are excluded at the shared structural scanner boundary; human/custom
lookalikes remain scanned. New-generation cancellation retains post-pointer
journal authority for forward recovery. Genuine old manifests derive only
supported coverage with unknown raw counts left absent, and candidate graph
validation remains read-only. Plugin refresh discards obsolete complete results,
limits settling retries and offers an explicit read-only status refresh.

Actual RED/GREEN tests reproduced each defect. Final covering eight Go packages,
affected vet, exact Gate A and plugin check passed; plugin has223 tests. The
original real missing-index migration failure now passes through preview,
confirmation, private graph reload and subsequent edit/sync/status. Prior Minor
M1–M3 remain deferred. The single scoped final re-review, final full ordered
sequence and native same-open-view acceptance are pending; this is not a final
green gate or integration claim.

### Final reviewed-source gate — all local checks passed

The single scoped re-review of f841907..6ad0c05 found I1–I4 all addressed,
with no new Critical/Important/Minor finding. Original three Minor items remain.
The exact ordered sequence then completed on clean, unchanged da3d2df:

| Command | Result |
|---|---|
| `go test ./...` | Exit0; includes ordinary large-Session test; publication274.995s, scan284.201s |
| `go vet ./...` | Exit0 |
| `go mod tidy -diff` | Exit0 |
| `go test ./test/zerotoken -run '^TestGateAZeroTokenCore$' -count=1` | Exit0,32.520s; baseline not changed |
| `go test -race -timeout 30m ./... -skip '^TestFoundationLargeSessionReachesBoundedPacketAfterStreamingPast20MiB$'` | Exit0; no race report; scan1009.167s, zerotoken573.792s |
| `git diff --check` | Exit0 |
| Plugin `npm run check` | Exit0;19 files/223 tests, lint, typecheck and build |

Only the exact named test is excluded under race; its ordinary run is included
above. Previous failed frozen runs remain recorded rather than rewritten.

Final native Obsidian1.13.7 used only the authorized synthetic Vault and CLI
source6ad0c05 (SHA256 `3d3c52b08669049ed1231f900a68c7f65291a8664216187366a828cb054218fa`),
with plugin bundle SHA256
`36a0c94b87736fcee84932e4f3f46780bb7a83ef4bf0fb1ed0ccddcb8d18b8f1`.
The previous experiment files were backed up before replacement. The same open
project view showed a native goal edit, then automatically cleared pending/error
after explicit fixture-bound sync. No new-view workaround was used. It stayed
public-valid/pending-private and read-only, never claiming private acceptance.

Controlled dual-sided conflict displayed correctly and refused sync while
preserving both drafts and accepted ledger. A generated evidence modification
refused sync and remained visibly stale. Losing only the already-selected
experiment runtime preserved native history access; restoring it and using
`刷新同步状态` recovered the same view. All controlled edits and runtime paths
were restored. This is runtime-loss recovery, not all-candidates-absent cold start.

The final explicit scan returned154 source/154 indexed/0 issues/0 review tokens,
same generation, and identical Project/Vault four-file hashes. Native history
still showed the sixteenth accepted synthetic milestone. After close/reopen,
reading mode showed the preserved human conclusion and separate source/evidence
section. Final accepted experiment revision8 has goal `Native settled status goal`.
Its matched pair hashes are review
`057e3d7ac1279f310396512ce744cfa4af6fffc31e1ad72e68ae0eba63431483`,
history `92f1ba1cce60a6f118154b33d47de96e3d531fd081d45ddac0d3cb37b43c02a9`,
ledger `a72aa55b01c7855d5eaf3e135191f1e4e9e1e734ac1fbbdbae1e54ad1d6334d3`,
index `a94ee8eb1b4a018bb73f38720abce2188490ce375609e27bf41d333cba67cd1d`.

Original Session Index Task3 may now be recorded as implementation/local regression
complete, with these explicit limits: M9 platform/real-data acceptance remains
open; real legacy migration may need classification/chain prerequisites; native
cold-start no-runtime discovery remains untested; Minor M1(read diagnostics),
M2(YAML binding presentation), M3(bulk-edit scaling) remain. Subsequent Session
Index Tasks4–7 are separate unfinished work. No integration or release occurred.
