# Task 4 (M4) implementation report: semantic units and common-ancestor merge

## Status

DONE

M4 now parses either authenticated physical Markdown document into human-only semantic units, preserves exact physical bytes while applying field and shell edits, and exposes a common-ancestor merge whose v4 comparison normalizes only CRLF/LF in values. Machine frontmatter and generated regions never enter the writable set.

## Implemented

- Added `syncdoc.ParseV4(relative, content, ledger)`. It uses the reviewv4 codec for grammar and validates one actual physical document against the ledger projection before exposing spans.
- Added the narrowly approved reviewv4 adapter `ParseMarkdownDocumentAgainstLedger` and defensive-copy `MarkdownDocument.Blocks`. The adapter verifies ledger self-digest/projection, document identity, complete registered inventory, top-level anchors, and generated old-base bytes while allowing human draft differences.
- Added `MarkdownDocument.SensitiveScanSource`. It masks only authenticated marker boundaries, authenticated generated anchor references, and validated top-level anchor spans. Human fields, custom prose, fenced/list/quote marker-looking text, and even the same known ID in fenced human content remain visible to the existing scanner.
- Added v4 `Document` dispatch for `Render`, `SemanticUnits`, `WithSemanticUnits`, cloning, and `SensitiveScanSource`; legacy parsing and the 4 MiB v2/v3 limit remain unchanged.
- Formal human fields use `session-reviewer/v4/<entity>/<field>`. Adding/deleting a formal field, exposing machine frontmatter, or changing generated content fails closed.
- Custom frontmatter and Markdown shell units reuse the existing `UnitSet` semantics. Exact source spans preserve untouched YAML, CRLF/mixed line endings, custom paragraphs, links, and code. Dirty v4 documents are not passed through whole-document YAML serialization.
- Split reconstruction/frontmatter-span logic into the controller-approved `v4_shell.go`; parse/state/unit behavior remains in `v4_units.go`.
- Placeholder collision detection scans the source once per nonce attempt. Physical-to-semantic token conversion builds one lookup and scans unit bytes sequentially. Reconstruction scans placeholders once and preflights final size before allocating block replacements. Semantic-unit input is also aggregate-preflighted before cloning or generic shell rendering.
- Added `sync.MergeV4Units`. With a Base it reuses the existing three-way unit algorithm through an injected comparator; without a Base it accepts only identical presence/value/presentation. Value equality normalizes CRLF/LF, while the selected side's physical bytes are retained. Legacy merges keep exact equality.

## Public API and trust boundary

- New public Go APIs:
  - `reviewv4.ParseMarkdownDocumentAgainstLedger(relative string, raw []byte, ledger MachineLedger) (MarkdownDocument, error)`
  - `MarkdownDocument.Blocks() []MarkdownBlock`
  - `MarkdownDocument.SensitiveScanSource() ([]byte, error)`
  - `syncdoc.ParseV4(relative string, content []byte, ledger reviewv4.MachineLedger) (Document, error)`
  - `sync.V4MergeInput`, `sync.V4MergeResult`, and `sync.MergeV4Units`
- The reviewv4 adapter is structural/public validation only. It does not authenticate a pending edit hash, synthesize a counterpart document, accept a draft privately, or replace M5 receipt/base-store proof.
- `scan.go` and generic `Scan`/`BuildInventory` are intentionally unchanged by controller ruling: those entry points have no authenticated ledger/private baseline input and must not infer v4 trust from marker-looking bytes. M5 must explicitly call `ParseV4` after private proof and then scan `Document.SensitiveScanSource`; M6 owns ordinary scan routing.
- `internal/syncdoc/v2_units.go` has the small v4 `SensitiveScanSource` dispatch because that method already lives there. This was explicitly approved; v2/v3 behavior is unchanged.

## TDD evidence

### Setup failures (not behavioral RED)

The initial new merge API test could not compile before declarations:

```text
$ go test ./internal/syncdoc ./internal/sync -run 'V4.*(Merge|Units|Shell)' -count=1
internal/sync/v4_merge_test.go: undefined: MergeV4Units
internal/sync/v4_merge_test.go: undefined: V4MergeInput
FAIL
```

The bounded semantic preflight test likewise first established only API setup:

```text
$ go test ./internal/syncdoc -run 'SemanticUnitPreflightUsesInjectedLimit' -count=1
internal/syncdoc/v4_units_test.go:197:12: undefined: preflightV4UnitSet
FAIL github.com/neomei/SessionReviewer/internal/syncdoc [build failed]
```

Neither compiler failure is claimed as behavioral evidence.

### Behavioral RED

After compile-enabling merge/shell stubs, the required focused command failed on real behavior: common-ancestor conflicts were empty, no-Base differences produced neither units nor conflicts, and the CRLF physical result was empty.

```text
$ go test ./internal/syncdoc ./internal/sync -run 'V4.*(Merge|Units|Shell)' -count=1
--- FAIL: TestV4MergeUsesCommonAncestor
    conflicts=[]
--- FAIL: TestV4MergeWithoutBaseRequiresBothSidesToAgree
    conflicts=[]
--- FAIL: TestV4MergeNormalizesOnlyValueLineEndings
    physical value=""
FAIL
```

The single-document adapter stubs then failed actual inventory and human-draft behavior rather than only compilation: the accepted draft returned empty fields, and removing a required block was not rejected.

The exact sensitive-boundary regression was observed before replacing global anchor substitution:

```text
$ go test ./internal/reviewv4 -run 'SensitiveSourceKeepsKnown' -count=1
--- FAIL: TestMarkdownDocumentSensitiveSourceKeepsKnownAnchorIDInHumanCode (0.00s)
    markdown_document_test.go:122: known anchor ID in human fenced code was masked
FAIL
FAIL github.com/neomei/SessionReviewer/internal/reviewv4 0.391s
```

After adding a compile-enabling preflight stub, the injected small limit produced a behavioral RED:

```text
$ go test ./internal/syncdoc -run 'SemanticUnitPreflightUsesInjectedLimit' -count=1
--- FAIL: TestV4SemanticUnitPreflightUsesInjectedLimit (0.00s)
    v4_units_test.go:198: aggregate-over-limit semantic units were accepted
FAIL
FAIL github.com/neomei/SessionReviewer/internal/syncdoc 0.537s
```

### Focused GREEN

The targeted v4/reviewv4 behaviors passed after span-scoped masking and bounded sequential reconstruction:

```text
$ go test ./internal/reviewv4 ./internal/syncdoc ./internal/sync -run 'V4|MarkdownDocumentAgainstLedger|MarkdownDocumentSensitiveSource' -count=1
ok  github.com/neomei/SessionReviewer/internal/reviewv4  0.170s
ok  github.com/neomei/SessionReviewer/internal/syncdoc   0.359s
ok  github.com/neomei/SessionReviewer/internal/sync      0.468s
```

Coverage includes common-ancestor same/different-field merges; no-Base equal/different/project-only/vault-only cases; CRLF semantic equality with physical-byte retention; formal field add/delete rejection; field-only CRLF preservation; mixed-line-ending paragraph rearrangement; arbitrary custom frontmatter add/remove; unchanged exact bytes; 5 MiB v4 versus the legacy 4 MiB cap; defensive spans; generated/inventory rejection; authenticated marker exemption; fake marker content; same-known-ID human code; and injected small assembly/unit limits.

## Final verification

Per the latest controller instruction, the bounded affected packages were run once instead of repeating the repository-wide suite:

```text
$ go test ./internal/reviewv4 ./internal/syncdoc ./internal/sync -count=1
ok  github.com/neomei/SessionReviewer/internal/reviewv4  0.521s
ok  github.com/neomei/SessionReviewer/internal/syncdoc   0.284s
ok  github.com/neomei/SessionReviewer/internal/sync      25.797s
```

```text
$ git diff --check
exit 0; no output
```

## Files

Created:

- `internal/sync/v4_merge.go`
- `internal/sync/v4_merge_test.go`
- `internal/syncdoc/v4_units.go`
- `internal/syncdoc/v4_shell.go` (controller-approved split)
- `internal/syncdoc/v4_units_test.go`
- `.superpowers/sdd/2026-09-05-v4-human-markdown-codec/task-4-report.md`

Modified:

- `internal/reviewv4/markdown_document.go`
- `internal/reviewv4/markdown_document_test.go`
- `internal/reviewv4/markdown_draft.go`
- `internal/sync/merge.go`
- `internal/syncdoc/document.go`
- `internal/syncdoc/v2_units.go`

`internal/reviewv4/markdown_draft.go` only extends the existing document index with validated top-level anchor spans so sensitive masking cannot trust code-block lookalikes; it does not add another grammar.

## Self-review

- Checked the approved spec section 10 risks explicitly: no per-entity full-document scans, no per-block whole-source replacement, no per-unit map rebuild, no whole-document v4 YAML serialization, and no final block growth before aggregate preflight.
- Marker/anchor masking is restricted to authenticated physical spans. The caller-level test proves both that the clean accepted fixture is exempt and that fake or same-known-ID human code remains scanned.
- `ReplaceFields` is called once per v4 edit operation, not once per field. Unchanged field-only output reuses that bounded result.
- All returned unit sets, blocks, bytes, merge units, and conflicts retain existing defensive-copy behavior; the common Base maps are never mutated.
- No model call, dependency change, push, merge, release, installed-plugin change, real Vault write, plan edit, ledger edit, or broad legacy refactor was performed.

## Commit and range bookkeeping

- Required subject: `feat: merge v4 human fields against the sync ancestor`
- Original reviewed M3 BASE: `72819c1`.
- Controller-only bookkeeping commit inherited before this implementation: `7b5afe3 docs: record reviewed markdown rendering milestone`.
- The clean M4 review range is therefore `7b5afe3..<M4 commit>`; `72819c1..<M4 commit>` additionally contains only that controller bookkeeping commit.
- This report is included in the M4 commit; the final hash is reported to the controller after creation to avoid an impossible self-referential commit hash.

## Concerns / deferred scope

- No M4 blocker. M5 still owns private accepted-receipt/common-Base authentication and transaction IO; M6 owns generic scan routing. This implementation must not be treated as proof that a public-valid draft is privately accepted.
