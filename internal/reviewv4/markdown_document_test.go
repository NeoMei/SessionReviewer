package reviewv4

import (
	"bytes"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func validMarkdownDocument(body string) []byte {
	return []byte("---\n" +
		"id: review-project-p\n" +
		"entity_type: project-review\n" +
		"project_id: project-p\n" +
		"schema_version: 4\n" +
		"document_format: review-markdown-v1\n" +
		"revision: 1\n" +
		"generation_id: generation-1\n" +
		"minimum_reader_version: 0.4.1\n" +
		"minimum_writer_version: 0.4.1\n" +
		"custom_owner: preserved\n" +
		"---\n" + body)
}

func TestMarkdownDocumentPreservesAndDefensivelyCopiesBytesAndFields(t *testing.T) {
	raw := validMarkdownDocument("# Review\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n  中文\nline  \n\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\ncustom")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	got := document.Bytes()
	if !bytes.Equal(got, raw) {
		t.Fatal("unmodified bytes changed")
	}
	got[0] = 'x'
	if bytes.Equal(got, document.Bytes()) {
		t.Fatal("Bytes returned shared storage")
	}
	fields := document.Fields()
	key := FieldKey{Entity: "project-overview", Name: "goal"}
	if fields[key] != "  中文\nline  \n" {
		t.Fatalf("field=%q", fields[key])
	}
	fields[key] = "mutated"
	if document.Fields()[key] == "mutated" {
		t.Fatal("Fields returned shared storage")
	}
}

func TestMarkdownDocumentAgainstLedgerAllowsHumanDraftAndRejectsMachineChanges(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	document, err := ParseMarkdownDocument("项目回顾.md", pair.Review)
	if err != nil {
		t.Fatal(err)
	}
	goal := FieldKey{Entity: "project-overview", Name: "goal"}
	edited, err := document.ReplaceFields(map[FieldKey]string{goal: "human draft"})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ParseMarkdownDocumentAgainstLedger("项目回顾.md", edited, ledger)
	if err != nil || validated.Fields()[goal] != "human draft" {
		t.Fatalf("human draft rejected: fields=%v err=%v", validated.Fields(), err)
	}

	blocks := validated.Blocks()
	if len(blocks) == 0 {
		t.Fatal("validated document exposed no spans")
	}
	first := blocks[0]
	blocks[0].Key.Name = "mutated"
	if validated.Blocks()[0].Key != first.Key {
		t.Fatal("Blocks returned shared storage")
	}

	generated := document.Blocks()
	for _, block := range generated {
		if !block.Generated {
			continue
		}
		modified := append(bytes.Clone(pair.Review[:block.ValueStart]), bytes.Repeat([]byte("x"), block.ValueEnd-block.ValueStart)...)
		modified = append(modified, pair.Review[block.ValueEnd:]...)
		if _, err := ParseMarkdownDocumentAgainstLedger("项目回顾.md", modified, ledger); MarkdownCodeOf(err) != MarkdownGeneratedRegionModified {
			t.Fatalf("generated change err=%v", err)
		}
		return
	}
	t.Fatal("fixture has no generated region")
}

func TestMarkdownDocumentAgainstLedgerRequiresCompleteInventory(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	pair := MarkdownPair{Review: mustRead(t, "../../testdata/contracts/v4/markdown/review.md"), History: mustRead(t, "../../testdata/contracts/v4/markdown/history.md")}
	document, err := ParseMarkdownDocument("项目回顾.md", pair.Review)
	if err != nil {
		t.Fatal(err)
	}
	block := document.Blocks()[0]
	missing := append(bytes.Clone(pair.Review[:block.Start]), pair.Review[block.End:]...)
	if _, err := ParseMarkdownDocumentAgainstLedger("项目回顾.md", missing, ledger); MarkdownCodeOf(err) != MarkdownFieldMissing {
		t.Fatalf("missing field err=%v", err)
	}
}

func TestMarkdownDocumentSensitiveSourceKeepsKnownAnchorIDInHumanCode(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	review := mustRead(t, "../../testdata/contracts/v4/markdown/review.md")
	const known = "milestone-x6d696c6573746f6e653a616c706861"
	human := []byte("\n```markdown\n[human example](项目历史.md#" + known + ")\n```\n")
	review = append(bytes.Clone(review), human...)
	document, err := ParseMarkdownDocumentAgainstLedger("项目回顾.md", review, ledger)
	if err != nil {
		t.Fatal(err)
	}
	source, err := document.SensitiveScanSource()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, human) {
		t.Fatal("known anchor ID in human fenced code was masked")
	}
	if bytes.Contains(source, []byte("项目历史.md#"+known+"\n")) {
		t.Fatal("authenticated generated reference was not masked")
	}
}

func TestMarkdownSensitiveSourceMasksOnlyAuthenticatedIdentityValues(t *testing.T) {
	const identity = "migration-0123456789abcdef0123456789abcdef"
	ledger := sharedMarkdownLedger(t)
	ledger.GenerationID = identity
	ledger.DocumentProjection.PresentationBase.GenerationID = identity
	for i := range ledger.DocumentProjection.PresentationBase.Timeline {
		ledger.DocumentProjection.PresentationBase.Timeline[i].GenerationID = identity
	}
	body, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = DecodeLedger(body)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{identity, "'" + identity + "'", "\"" + identity + "\"", "|-\n  " + identity, ">-\n  " + identity} {
		for _, newline := range []string{"\n", "\r\n"} {
			raw := bytes.Replace(pair.Review, []byte("generation_id: "+identity), []byte("\"generation_id\": "+spelling), 1)
			raw = bytes.ReplaceAll(raw, []byte("\n"), []byte(newline))
			document, err := ParseMarkdownDocumentAgainstLedger("项目回顾.md", raw, ledger)
			if err != nil {
				t.Fatalf("parse %q: %v", spelling, err)
			}
			source, err := document.SensitiveScanSource()
			if err != nil || len(redact.Default().Text(string(source)).Findings) != 0 {
				t.Fatalf("authenticated identity scanned: spelling=%q err=%v", spelling, err)
			}
		}
	}
	for name, add := range map[string]func([]byte) []byte{
		"comment": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte("generation_id: "+identity), []byte("generation_id: "+identity+" # "+identity), 1)
		},
		"custom": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte("---\n"), []byte("---\ncustom: "+identity+"\n"), 1)
		},
		"human": func(raw []byte) []byte { return append(raw, []byte("\n"+identity+"\n")...) },
	} {
		t.Run(name, func(t *testing.T) {
			document, err := ParseMarkdownDocumentAgainstLedger("项目回顾.md", add(bytes.Clone(pair.Review)), ledger)
			if err != nil {
				t.Fatal(err)
			}
			source, err := document.SensitiveScanSource()
			if err != nil || !bytes.Contains(source, []byte(identity)) || len(redact.Default().Text(string(source)).Findings) == 0 {
				t.Fatalf("human identity lookalike escaped scan: %v", err)
			}
		})
	}
}

func TestMarkdownSensitiveSourceMasksAuthenticatedGeneratedSourceRefs(t *testing.T) {
	ledger := sharedMarkdownLedger(t)
	ledger.MinimumReaderVersion, ledger.MinimumWriterVersion = "0.4.3", "0.4.3"
	ledger.DocumentProjection.PresentationBase.MinimumReaderVersion, ledger.DocumentProjection.PresentationBase.MinimumWriterVersion = "0.4.3", "0.4.3"
	ledger.DocumentProjection.PresentationBase.ChainDependencies = []ChainDependency{{Provider: "codex", SessionID: "session-s", SessionViewDigest: "sha256:173ca58e035b5053b7c359e0605ede4574febb3fb8881db61d6bfc934ecd87ed", DependencyDigest: "sha256:" + strings.Repeat("8", 64), TurnUnitIDs: []string{"turn-cfe718b0307b34f85dd577467fe15a7d15c4cd6d32659d37c43dc3396a4202c0"}}}
	dependency := &ledger.DocumentProjection.PresentationBase.ChainDependencies[0]
	ref := SourceTurnRef{Provider: dependency.Provider, SessionID: dependency.SessionID, SessionViewDigest: dependency.SessionViewDigest, TurnUnitID: dependency.TurnUnitIDs[0]}
	loop := ledger.DocumentProjection.PresentationBase.Timeline[0].ClosedLoop
	loop.SourceTurnRefs = []SourceTurnRef{ref}
	loop.Coverage = ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1}
	loop.Verification = ClosedLoopSegment{State: "present", Text: "passed", SourceTurnRefs: []SourceTurnRef{ref}}
	ledger.DocumentProjection.PresentationBase.Timeline[0].ClosedLoop = loop
	body, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err = DecodeLedger(body)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := RenderMarkdown(ledger.DocumentProjection.PresentationBase, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(pair.History, []byte(dependency.SessionViewDigest)) || !bytes.Contains(pair.History, []byte(dependency.TurnUnitIDs[0])) {
		t.Fatal("fixture did not render authenticated source references")
	}
	document, err := ParseMarkdownDocumentAgainstLedger("项目历史.md", pair.History, ledger)
	if err != nil {
		t.Fatal(err)
	}
	source, err := document.SensitiveScanSource()
	if err != nil || len(redact.Default().Text(string(source)).Findings) != 0 {
		t.Fatalf("authenticated source refs reached human-content scanner: err=%v source=%s", err, source)
	}
	human := append(bytes.Clone(pair.History), []byte("\n"+dependency.Provider+"/"+dependency.SessionID+"@"+dependency.SessionViewDigest+"#"+dependency.TurnUnitIDs[0]+"\n")...)
	document, err = ParseMarkdownDocumentAgainstLedger("项目历史.md", human, ledger)
	if err != nil {
		t.Fatal(err)
	}
	source, err = document.SensitiveScanSource()
	if err != nil || len(redact.Default().Text(string(source)).Findings) == 0 {
		t.Fatalf("human source-ref lookalike escaped scan: err=%v source=%s", err, source)
	}
	tampered := bytes.Replace(pair.History, []byte(dependency.SessionViewDigest), []byte("sha256:"+strings.Repeat("f", 64)), 1)
	if _, err := ParseMarkdownDocumentAgainstLedger("项目历史.md", tampered, ledger); MarkdownCodeOf(err) != MarkdownGeneratedRegionModified {
		t.Fatalf("tampered generated source ref was accepted: %v", err)
	}
}

func TestMarkdownDocumentReplaceFieldsPreservesShellAndStructure(t *testing.T) {
	raw := validMarkdownDocument("before\r\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\nold\r\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\r\nafter\r\n")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	key := FieldKey{Entity: "project-overview", Name: "goal"}
	result, err := document.ReplaceFields(map[FieldKey]string{key: "  new\r\nline\r\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(raw, []byte("old"), []byte("  new\r\nline\r\n"), 1)
	if !bytes.Equal(result, want) {
		t.Fatalf("replacement changed shell\ngot: %q\nwant:%q", result, want)
	}
	result[0] = 'x'
	if document.Bytes()[0] == 'x' {
		t.Fatal("replacement shares document storage")
	}
	if _, err := document.ReplaceFields(map[FieldKey]string{{Entity: "project-overview", Name: "stage"}: "x"}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("missing replacement key err=%v", err)
	}
	if _, err := document.ReplaceFields(map[FieldKey]string{key: strings.Repeat("x", maxMarkdownFieldBytes+1)}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("overlong replacement err=%v", err)
	}
	if _, err := document.ReplaceFields(map[FieldKey]string{key: "x\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n"}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("structural replacement err=%v", err)
	}
	for _, value := range []string{
		"x\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"stage\" -->\n",
		"```markdown\n",
		"bad\rline",
		"bad\x00line",
		string([]byte{0xff}),
	} {
		if _, err := document.ReplaceFields(map[FieldKey]string{key: value}); MarkdownCodeOf(err) != MarkdownFormatInvalid {
			t.Fatalf("unsafe replacement %q err=%v", value, err)
		}
	}
	unchanged, err := document.ReplaceFields(nil)
	if err != nil || !bytes.Equal(unchanged, raw) {
		t.Fatalf("nil replacement changed bytes: err=%v", err)
	}
}

func replacementPreflightDocument(t *testing.T) (MarkdownDocument, []byte) {
	t.Helper()
	raw := validMarkdownDocument("<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n0123456789\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"goal\" -->\n<!-- session-reviewer:v4-field entity=\"project-overview\" name=\"stage\" -->\nx\n<!-- /session-reviewer:v4-field entity=\"project-overview\" name=\"stage\" -->\n")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	return document, raw
}

func TestMarkdownDocumentReplacementSizePreflightIsOrderIndependent(t *testing.T) {
	document, raw := replacementPreflightDocument(t)
	goal := FieldKey{Entity: "project-overview", Name: "goal"}
	stage := FieldKey{Entity: "project-overview", Name: "stage"}

	shrinkingFinal := map[FieldKey]string{
		stage: "12345",
		goal:  "",
	}
	result, err := document.replaceFields(shrinkingFinal, len(raw)-5)
	if err != nil {
		t.Fatalf("aggregate-valid shrink/grow replacement rejected: %v", err)
	}
	if got, want := len(result), len(raw)-6; got != want {
		t.Fatalf("result length=%d want=%d", got, want)
	}
	if fields, err := ParseMarkdownDocument("项目回顾.md", result); err != nil || fields.Fields()[stage] != "12345" || fields.Fields()[goal] != "" {
		t.Fatalf("replacement fields were not preserved: err=%v fields=%v", err, fields.Fields())
	}
}

func TestMarkdownDocumentReplacementSizePreflightRejectsAggregateOverflow(t *testing.T) {
	document, raw := replacementPreflightDocument(t)
	goal := FieldKey{Entity: "project-overview", Name: "goal"}
	stage := FieldKey{Entity: "project-overview", Name: "stage"}
	overflowingFinal := map[FieldKey]string{
		goal:  strings.Repeat("g", 20),
		stage: strings.Repeat("s", 20),
	}
	if _, err := document.replaceFields(overflowingFinal, len(raw)+1); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("aggregate-over-limit replacement err=%v", err)
	}
}

func TestMarkdownDocumentRejectsGeneratedReplacement(t *testing.T) {
	raw := validMarkdownDocument("<!-- session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\nold\n<!-- /session-reviewer:v4-generated entity=\"project-overview\" name=\"problem-tree\" -->\n")
	document, err := ParseMarkdownDocument("项目回顾.md", raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = document.ReplaceFields(map[FieldKey]string{{Entity: "project-overview", Name: "problem-tree"}: "new"})
	if MarkdownCodeOf(err) != MarkdownGeneratedRegionModified {
		t.Fatalf("err=%v", err)
	}
}

func TestMarkdownDocumentRejectsInvalidFrontmatter(t *testing.T) {
	tests := []struct{ name, old, replacement string }{
		{"duplicate key", "id: review-project-p\n", "id: one\nid: two\n"},
		{"alias", "id: review-project-p\n", "id: &id review-project-p\ncopy: *id\n"},
		{"merge", "id: review-project-p\n", "id: review-project-p\nbase: &base {x: y}\ncopy: {<<: *base}\n"},
		{"unknown reserved", "custom_owner: preserved\n", "custom_owner: preserved\nsession_reviewer_future: true\n"},
		{"wrong format", "document_format: review-markdown-v1\n", "document_format: review-json-v4\n"},
		{"wrong reader", "minimum_reader_version: 0.4.1\n", "minimum_reader_version: 0.4.0\n"},
		{"missing id", "id: review-project-p\n", "custom_replacement: value\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := bytes.Replace(validMarkdownDocument(""), []byte(tc.old), []byte(tc.replacement), 1)
			if _, err := ParseMarkdownDocument("项目回顾.md", raw); MarkdownCodeOf(err) != MarkdownFormatInvalid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	deep := validMarkdownDocument("x")
	insert := "extra: " + strings.Repeat("[", maxMarkdownYAMLDepth+1) + "x" + strings.Repeat("]", maxMarkdownYAMLDepth+1) + "\n"
	deep = bytes.Replace(deep, []byte("custom_owner: preserved\n"), []byte(insert), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", deep); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("deep YAML err=%v", err)
	}
	multiple := bytes.Replace(validMarkdownDocument("x"), []byte("custom_owner: preserved\n"), []byte("--- # second document\nother: value\n"), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", multiple); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("multiple YAML documents err=%v", err)
	}
	oversize := bytes.Replace(validMarkdownDocument("x"), []byte("custom_owner: preserved"), []byte("custom_owner: "+strings.Repeat("x", maxMarkdownFrontmatterBytes)), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", oversize); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("oversize frontmatter err=%v", err)
	}
	nodes := validMarkdownDocument("x")
	var many strings.Builder
	many.WriteString("extra:\n")
	for index := 0; index < maxMarkdownYAMLNodes; index++ {
		many.WriteString("  - x\n")
	}
	nodes = bytes.Replace(nodes, []byte("custom_owner: preserved\n"), []byte(many.String()), 1)
	if _, err := ParseMarkdownDocument("项目回顾.md", nodes); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("excessive YAML nodes err=%v", err)
	}
}

func TestMarkdownDocumentRejectsWrongDocumentOwnershipAndUnsafePath(t *testing.T) {
	historyField := "<!-- session-reviewer:v4-field entity=\"milestone:m\" name=\"title\" -->\ntitle\n<!-- /session-reviewer:v4-field entity=\"milestone:m\" name=\"title\" -->\n"
	if _, err := ParseMarkdownDocument("项目回顾.md", validMarkdownDocument(historyField)); MarkdownCodeOf(err) != MarkdownFormatInvalid {
		t.Fatalf("history field in review err=%v", err)
	}
	for _, relative := range []string{"", "/absolute.md", "../escape.md", "nested/../escape.md", `nested\escape.md`} {
		if _, err := ParseMarkdownDocument(relative, validMarkdownDocument("body")); MarkdownCodeOf(err) != MarkdownFormatInvalid {
			t.Fatalf("unsafe relative %q err=%v", relative, err)
		}
	}
}

func TestMarkdownDocumentSharedCorpusParserOutcomes(t *testing.T) {
	var corpus markdownCorpus
	if err := strictjson.Decode(mustRead(t, "../../testdata/contracts/v4/markdown/cases.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range corpus.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			review, reviewErr := ParseMarkdownDocument(testCase.Review, mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Review))
			history, historyErr := ParseMarkdownDocument(testCase.History, mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.History))
			gotCode := MarkdownCodeOf(reviewErr)
			if gotCode == "" {
				gotCode = MarkdownCodeOf(historyErr)
			}
			wantCode := ""
			if testCase.ExpectedDocumentCode != nil {
				wantCode = *testCase.ExpectedDocumentCode
			}
			if gotCode != wantCode {
				t.Fatalf("Markdown rejection=%q want=%q reviewErr=%v historyErr=%v", gotCode, wantCode, reviewErr, historyErr)
			}
			if gotCode != "" {
				return
			}
			fields := review.Fields()
			for key, value := range history.Fields() {
				fields[key] = value
			}
			for encoded, want := range testCase.ExpectedFields {
				entity, name, ok := strings.Cut(encoded, "/")
				if !ok {
					t.Fatalf("invalid expected field key %q", encoded)
				}
				if got, exists := fields[FieldKey{Entity: entity, Name: name}]; !exists || got != want {
					t.Fatalf("field %s=%q exists=%v want=%q", encoded, got, exists, want)
				}
			}
		})
	}
}

func TestMarkdownDocumentContainerFixtureHasPinnedBindings(t *testing.T) {
	review := mustRead(t, "../../testdata/contracts/v4/markdown/review-container-markers.md")
	ledger, err := DecodeLedger(mustRead(t, "../../testdata/contracts/v4/markdown/ledger-container-markers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sha256Hex(review), "8048725cb9d978d428dc913008a55f9202e3736369a119782bfb205fc0496713"; got != want || ledger.ReviewSHA256 != want {
		t.Fatalf("review hash=%s ledger=%s want=%s", got, ledger.ReviewSHA256, want)
	}
	if got, want := CanonicalLedgerSHA256(ledger), "1982bd93997dd7429dc05fa9d7d96af74f2013d53ba6015893f9db8fa2922c23"; got != want || ledger.SyncHashes.LedgerSHA256 != want {
		t.Fatalf("ledger hash=%s embedded=%s want=%s", got, ledger.SyncHashes.LedgerSHA256, want)
	}
}
