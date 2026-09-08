package reviewv4

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestResolveSourceTurnDependencySelectsExactHistoricalSnapshot(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	newView := "sha256:" + strings.Repeat("2", 64)
	dependencies := []ChainDependency{
		{Provider: "codex", SessionID: "session-s", SessionViewDigest: oldView, DependencyDigest: "sha256:" + strings.Repeat("3", 64), TurnUnitIDs: []string{"turn-a"}},
		{Provider: "codex", SessionID: "session-s", SessionViewDigest: newView, DependencyDigest: "sha256:" + strings.Repeat("4", 64), TurnUnitIDs: []string{"turn-a"}},
		{Provider: "claude", SessionID: "session-s", SessionViewDigest: oldView, DependencyDigest: "sha256:" + strings.Repeat("5", 64), TurnUnitIDs: []string{"turn-a"}},
	}

	got, err := ResolveSourceTurnDependency(SourceTurnRef{
		Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: oldView,
	}, dependencies)
	if err != nil || got.SessionViewDigest != oldView || got.DependencyDigest != dependencies[0].DependencyDigest {
		t.Fatalf("historical turn was rebound: got=%+v err=%v", got, err)
	}
	got, err = ResolveSourceTurnDependency(SourceTurnRef{Provider: "claude", SessionID: "session-s", TurnUnitID: "turn-a"}, dependencies)
	if err != nil || got.Provider != "claude" {
		t.Fatalf("same native ID across providers was not kept separate: got=%+v err=%v", got, err)
	}
}

func TestValidatePresentationRequiresExactHistoricalBindingsAndCapability(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	newView := "sha256:" + strings.Repeat("2", 64)
	presentation := historicalPresentation(oldView, newView)
	if err := ValidatePresentation(presentation); err != nil {
		t.Fatalf("qualified historical presentation rejected: %v", err)
	}

	legacyFloor := presentation
	legacyFloor.MinimumReaderVersion = "0.4.0"
	legacyFloor.MinimumWriterVersion = "0.4.0"
	if err := ValidatePresentation(legacyFloor); err == nil {
		t.Fatal("qualified historical presentation used the legacy capability floor")
	}

	future := presentation
	future.MinimumReaderVersion = "0.4.4"
	future.MinimumWriterVersion = "0.4.4"
	if err := ValidatePresentation(future); err == nil {
		t.Fatal("accepted unsupported future presentation capability")
	}

	ambiguous := presentation
	ambiguous.ProblemNodes = append([]ProblemNode(nil), presentation.ProblemNodes...)
	ambiguous.ProblemNodes[0].SourceTurnRefs = []SourceTurnRef{{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a"}}
	if err := ValidatePresentation(ambiguous); err == nil {
		t.Fatal("accepted ambiguous unqualified historical source turn")
	}

	wrongSnapshot := presentation
	wrongSnapshot.ProblemNodes = append([]ProblemNode(nil), presentation.ProblemNodes...)
	wrongSnapshot.ProblemNodes[0].SourceTurnRefs = []SourceTurnRef{{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: "sha256:" + strings.Repeat("9", 64)}}
	if err := ValidatePresentation(wrongSnapshot); err == nil {
		t.Fatal("accepted a source turn bound to an absent snapshot")
	}
}

func TestValidatePresentationRejectsDuplicateCanonicalBindingAndSegmentRebinding(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	newView := "sha256:" + strings.Repeat("2", 64)

	single := historicalPresentation(oldView)
	single.ProblemNodes[0].SourceTurnRefs = []SourceTurnRef{
		{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a"},
		{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: oldView},
	}
	if err := ValidatePresentation(single); err == nil {
		t.Fatal("qualified spelling bypassed duplicate canonical binding rejection")
	}

	closed := historicalPresentation(oldView, newView)
	qualifiedOld := SourceTurnRef{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: oldView}
	qualifiedNew := SourceTurnRef{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: newView}
	loop := NeutralClosedLoop()
	loop.SourceTurnRefs = []SourceTurnRef{qualifiedOld}
	loop.Coverage = ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1}
	loop.TriggerQuestion = ClosedLoopSegment{State: "present", Text: "question", SourceTurnRefs: []SourceTurnRef{qualifiedNew}}
	closed.Timeline = []Timeline{{ID: "milestone-1", GenerationID: closed.GenerationID, Kind: "milestone", DecisionIDs: []string{}, ClosedLoop: loop}}
	if err := ValidatePresentation(closed); err == nil {
		t.Fatal("accepted segment rebound to a snapshot absent from aggregate refs")
	}
}

func TestValidatePresentationDependencyTupleAndConditionalLegacyCapability(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	legacy := historicalPresentation(oldView)
	legacy.MinimumReaderVersion = "0.4.0"
	legacy.MinimumWriterVersion = "0.4.0"
	legacy.ProblemNodes[0].SourceTurnRefs[0].SessionViewDigest = ""
	if err := ValidatePresentation(legacy); err != nil {
		t.Fatalf("legacy single-snapshot presentation rejected: %v", err)
	}

	legacyBytes, err := json.Marshal(legacy.ProblemNodes[0].SourceTurnRefs[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(legacyBytes, []byte("session_view_digest")) {
		t.Fatalf("legacy source ref emitted optional field: %s", legacyBytes)
	}

	multi := historicalPresentation(oldView, "sha256:"+strings.Repeat("2", 64))
	multi.ProblemNodes[0].SourceTurnRefs = []SourceTurnRef{}
	multi.MinimumReaderVersion = "0.4.0"
	multi.MinimumWriterVersion = "0.4.0"
	if err := ValidatePresentation(multi); err == nil {
		t.Fatal("multi-snapshot presentation used the legacy capability floor")
	}
	multi.MinimumReaderVersion = "0.4.3"
	multi.MinimumWriterVersion = "0.4.3"
	if err := ValidatePresentation(multi); err != nil {
		t.Fatalf("0.4.3 multi-snapshot presentation rejected: %v", err)
	}

	duplicate := historicalPresentation(oldView)
	duplicate.ChainDependencies = append(duplicate.ChainDependencies, duplicate.ChainDependencies[0])
	if err := ValidatePresentation(duplicate); err == nil {
		t.Fatal("accepted duplicate identical provider/session/view dependency tuple")
	}
}

func TestDecodePresentationStrictlyAcceptsOnlyTheOptionalQualifierOnce(t *testing.T) {
	presentation := historicalPresentation("sha256:" + strings.Repeat("1", 64))
	body, err := json.Marshal(presentation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePresentation(body); err != nil {
		t.Fatalf("qualified source ref failed strict decode: %v", err)
	}
	unknown := bytes.Replace(body, []byte(`"session_view_digest":"`), []byte(`"unknown":true,"session_view_digest":"`), 1)
	if _, err := DecodePresentation(unknown); err == nil || strictjson.CodeOf(err) != "wire_shape_invalid" {
		t.Fatalf("unknown source ref field rejection = %v", err)
	}
	duplicate := bytes.Replace(body, []byte(`"session_view_digest":"`), []byte(`"session_view_digest":"sha256:`+strings.Repeat("1", 64)+`","session_view_digest":"`), 1)
	if _, err := DecodePresentation(duplicate); err == nil || strictjson.CodeOf(err) != "wire_json_invalid" {
		t.Fatalf("duplicate source ref field rejection = %v", err)
	}
	empty := bytes.Replace(body, []byte(`"session_view_digest":"sha256:`+strings.Repeat("1", 64)+`"`), []byte(`"session_view_digest":""`), 1)
	if _, err := DecodePresentation(empty); err == nil || strictjson.CodeOf(err) != "wire_shape_invalid" {
		t.Fatalf("explicit empty qualifier rejection = %v", err)
	}
}

func TestHistoricalBindingsRequireMatchingLedgerAndMarkdownCapabilities(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	ledger := projectedLedger(t)
	presentation := historicalPresentation(oldView)
	loop := NeutralClosedLoop()
	loop.SourceTurnRefs = []SourceTurnRef{{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: oldView}}
	loop.Coverage = ClosedLoopCoverage{SourceTurns: 1, CapturedTurns: 1}
	presentation.Timeline = []Timeline{{ID: "milestone-1", GenerationID: presentation.GenerationID, Kind: "milestone", DecisionIDs: []string{}, ClosedLoop: loop}}
	presentation.ProjectID = ledger.ProjectID
	presentation.GenerationID = ledger.GenerationID
	presentation.Timeline[0].GenerationID = ledger.GenerationID
	presentation.ProjectViewDigest = ledger.ProjectViewDigest
	presentation.Revision = ledger.AcceptedRevision
	if presentation.Revision < 1 {
		presentation.Revision = 1
		ledger.AcceptedRevision = 1
	}
	presentation.HumanPatches = append([]Patch{}, ledger.HumanPatches...)
	presentation.OrphanPatches = append([]Patch{}, ledger.OrphanPatches...)
	presentation.GeneratedBaselines = append([]Baseline{}, ledger.GeneratedBaselines...)
	ledger.DocumentProjection.PresentationBase = presentation
	ledger.MinimumReaderVersion = "0.4.3"
	ledger.MinimumWriterVersion = "0.4.3"
	if err := ValidateLedger(ledger); err != nil {
		t.Fatalf("qualified historical ledger rejected: %v", err)
	}

	oldFloor := ledger
	oldFloor.MinimumReaderVersion = "0.4.1"
	oldFloor.MinimumWriterVersion = "0.4.1"
	if err := ValidateLedger(oldFloor); err == nil {
		t.Fatal("qualified historical projection used the old Markdown capability floor")
	}
	ledgerBody, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	emptyQualifier := bytes.Replace(ledgerBody, []byte(`"turn_unit_id":"turn-a","session_view_digest":"`+oldView+`"`), []byte(`"turn_unit_id":"turn-a","session_view_digest":""`), 1)
	if _, err := DecodeLedger(emptyQualifier); err == nil || strictjson.CodeOf(err) != "wire_shape_invalid" {
		t.Fatalf("nested ledger empty qualifier rejection = %v", err)
	}
	ledger, err = DecodeLedger(ledgerBody)
	if err != nil {
		t.Fatal(err)
	}

	pair, err := RenderMarkdown(presentation, ledger, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"review": pair.Review, "history": pair.History} {
		if !bytes.Contains(body, []byte("minimum_reader_version: 0.4.3\nminimum_writer_version: 0.4.3")) {
			t.Fatalf("%s Markdown omitted 0.4.3 capability frontmatter:\n%s", name, body)
		}
	}
	qualifiedLine := "codex/session-s@" + oldView + "#turn-a"
	if !bytes.Contains(pair.History, []byte(qualifiedLine)) {
		t.Fatalf("Markdown omitted exact historical source binding %q:\n%s", qualifiedLine, pair.History)
	}

	ledger = bindMarkdownPair(t, ledger, pair)
	draft, err := ParseMarkdownDraft(pair, ledger)
	if err != nil || !bytes.Equal(pair.Review, draft.Documents.Review) || !bytes.Equal(pair.History, draft.Documents.History) || draft.Presentation.ProblemNodes[0].SourceTurnRefs[0].SessionViewDigest != oldView {
		t.Fatalf("qualified Markdown round trip lost binding or bytes: err=%v draft=%+v", err, draft.Presentation.ProblemNodes)
	}
}

func historicalPresentation(views ...string) Presentation {
	presentation := minimumPresentation()
	presentation.MinimumReaderVersion = "0.4.3"
	presentation.MinimumWriterVersion = "0.4.3"
	for index, view := range views {
		presentation.ChainDependencies = append(presentation.ChainDependencies, ChainDependency{
			Provider: "codex", SessionID: "session-s", SessionViewDigest: view,
			DependencyDigest: "sha256:" + strings.Repeat(string(rune('3'+index)), 64), TurnUnitIDs: []string{"turn-a"},
		})
	}
	presentation.ProblemMapRevision = 1
	presentation.ProblemRootIDs = []string{"problem-1"}
	presentation.ProblemNodes = []ProblemNode{{
		ID: "problem-1", Question: "Why?", RelatedNodeIDs: []string{}, WorkflowState: "not_started", AnswerState: "no_answer",
		SourceTurnRefs: []SourceTurnRef{{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: views[0]}},
		Provenance:     "human_created", FirstProposedAt: "2026-09-09T00:00:00Z", Revision: 1,
	}}
	return presentation
}

func TestResolveSourceTurnDependencyRequiresExactlyOneMatch(t *testing.T) {
	oldView := "sha256:" + strings.Repeat("1", 64)
	newView := "sha256:" + strings.Repeat("2", 64)
	dependency := ChainDependency{Provider: "codex", SessionID: "session-s", SessionViewDigest: oldView, DependencyDigest: "sha256:" + strings.Repeat("3", 64), TurnUnitIDs: []string{"turn-a"}}
	dependencies := []ChainDependency{
		dependency,
		{Provider: "codex", SessionID: "session-s", SessionViewDigest: newView, DependencyDigest: "sha256:" + strings.Repeat("4", 64), TurnUnitIDs: []string{"turn-a"}},
	}

	for name, ref := range map[string]SourceTurnRef{
		"ambiguous shorthand": {Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a"},
		"wrong snapshot":      {Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: "sha256:" + strings.Repeat("9", 64)},
		"missing turn":        {Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-missing", SessionViewDigest: oldView},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveSourceTurnDependency(ref, dependencies); err == nil {
				t.Fatal("reference did not require exactly one dependency")
			}
		})
	}

	if _, err := ResolveSourceTurnDependency(SourceTurnRef{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a", SessionViewDigest: oldView}, []ChainDependency{dependency, dependency}); err == nil {
		t.Fatal("duplicate identical dependency tuple resolved as one match")
	}
}

func TestResolveSourceTurnDependencyRejectsInvalidPublicReferenceShape(t *testing.T) {
	invalid := SourceTurnRef{Provider: "bad provider", SessionID: "session-s", TurnUnitID: "turn-a"}
	matchingInvalidDependency := []ChainDependency{{
		Provider: "bad provider", SessionID: "session-s", SessionViewDigest: "sha256:" + strings.Repeat("1", 64),
		DependencyDigest: "sha256:" + strings.Repeat("2", 64), TurnUnitIDs: []string{"turn-a"},
	}}
	if _, err := ResolveSourceTurnDependency(invalid, matchingInvalidDependency); err == nil {
		t.Fatal("resolver accepted an invalid source-turn reference because an invalid dependency happened to match")
	}
}

func TestSourceTurnBindingIndexUsesExactLookupAcrossManySnapshots(t *testing.T) {
	dependencies := make([]ChainDependency, 512)
	for index := range dependencies {
		view := fmt.Sprintf("sha256:%064x", index+1)
		dependencies[index] = ChainDependency{
			Provider: "codex", SessionID: "session-s", SessionViewDigest: view,
			DependencyDigest: fmt.Sprintf("sha256:%064x", index+513), TurnUnitIDs: []string{"turn-a"},
		}
	}
	bindings, err := newSourceTurnBindingIndex(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	last := dependencies[len(dependencies)-1]
	got, err := bindings.resolve(SourceTurnRef{
		Provider: last.Provider, SessionID: last.SessionID, TurnUnitID: "turn-a", SessionViewDigest: last.SessionViewDigest,
	})
	if err != nil || got.DependencyDigest != last.DependencyDigest {
		t.Fatalf("exact lookup missed retained snapshot: got=%+v err=%v", got, err)
	}
	if _, err := bindings.resolve(SourceTurnRef{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a"}); err == nil {
		t.Fatal("many-snapshot shorthand did not remain ambiguous")
	}
}

func TestLegacySourceTurnReferenceKeepsCanonicalBytesWhenQualifierOmitted(t *testing.T) {
	ref := SourceTurnRef{Provider: "codex", SessionID: "session-s", TurnUnitID: "turn-a"}
	got, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"provider":"codex","session_id":"session-s","turn_unit_id":"turn-a"}`
	if string(got) != want {
		t.Fatalf("legacy canonical bytes changed:\n got %s\nwant %s", got, want)
	}

	ref.SessionViewDigest = "sha256:" + strings.Repeat("1", 64)
	got, err = json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	wantQualified := `{"provider":"codex","session_id":"session-s","turn_unit_id":"turn-a","session_view_digest":"sha256:` + strings.Repeat("1", 64) + `"}`
	if string(got) != wantQualified {
		t.Fatalf("qualified canonical field order changed:\n got %s\nwant %s", got, wantQualified)
	}
}
