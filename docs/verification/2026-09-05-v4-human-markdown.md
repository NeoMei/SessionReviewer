# V4 human Markdown codec verification

Date: 2026-09-06

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
