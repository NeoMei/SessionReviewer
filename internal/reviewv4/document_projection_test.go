package reviewv4

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/sessionindex"
	"github.com/neomei/SessionReviewer/internal/strictjson"
)

type markdownCorpus struct {
	SchemaVersion int                  `json:"schema_version" required:"true"`
	Format        string               `json:"format" required:"true"`
	Cases         []markdownCorpusCase `json:"cases" required:"true"`
}

type markdownCorpusCase struct {
	Name                   string            `json:"name" required:"true"`
	Review                 string            `json:"review" required:"true"`
	History                string            `json:"history" required:"true"`
	Ledger                 string            `json:"ledger" required:"true"`
	Index                  string            `json:"index" required:"true"`
	ExpectedCode           string            `json:"expected_code" required:"true"`
	ExpectedPrivateBinding *string           `json:"expected_private_binding,omitempty"`
	ExpectedDocumentCode   *string           `json:"expected_document_code,omitempty"`
	ExpectedMarkdownCode   *string           `json:"expected_markdown_code,omitempty"`
	ExpectedFields         map[string]string `json:"expected_fields" required:"true"`
}

func TestDocumentProjectionVersionAndIdentityContract(t *testing.T) {
	valid := projectedLedger(t)
	if err := ValidateLedger(valid); err != nil {
		t.Fatalf("valid document projection rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*MachineLedger)
	}{
		{"old outer reader", func(l *MachineLedger) { l.MinimumReaderVersion = "0.4.0" }},
		{"old outer writer", func(l *MachineLedger) { l.MinimumWriterVersion = "0.4.0" }},
		{"wrong projection schema", func(l *MachineLedger) { l.DocumentProjection.SchemaVersion = 2 }},
		{"wrong projection format", func(l *MachineLedger) { l.DocumentProjection.Format = "review-json-v4" }},
		{"inner reader splice", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.MinimumReaderVersion = "0.4.1" }},
		{"inner writer splice", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.MinimumWriterVersion = "0.4.1" }},
		{"project mismatch", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.ProjectID = "other" }},
		{"generation mismatch", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.GenerationID = "other" }},
		{"project digest mismatch", func(l *MachineLedger) {
			l.DocumentProjection.PresentationBase.ProjectViewDigest = "sha256:" + strings.Repeat("9", 64)
		}},
		{"revision mismatch", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.Revision++ }},
		{"human patches mismatch", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.HumanPatches = []Patch{minimumPatch()} }},
		{"orphan patches mismatch", func(l *MachineLedger) { l.DocumentProjection.PresentationBase.OrphanPatches = []Patch{minimumPatch()} }},
		{"generated baselines mismatch", func(l *MachineLedger) {
			l.DocumentProjection.PresentationBase.GeneratedBaselines = []Baseline{minimumBaseline()}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := projectedLedger(t)
			tc.mutate(&ledger)
			if err := ValidateLedger(ledger); err == nil {
				t.Fatal("accepted invalid document projection")
			}
		})
	}
}

func TestDocumentProjectionIsCoveredByCanonicalSelfDigest(t *testing.T) {
	legacy := frozenLedger(t)
	const oldCanonical = "2649fac1e8df09ee7857f3c337bee613d5f48bad3faa3e1e14bf75ef4651b9b7"
	if got := CanonicalLedgerSHA256(legacy); got != oldCanonical {
		t.Fatalf("legacy canonical hash changed: %s", got)
	}
	projected := projectedLedger(t)
	first := CanonicalLedgerSHA256(projected)
	projected.DocumentProjection.PresentationBase.CurrentState.Goal = "changed"
	second := CanonicalLedgerSHA256(projected)
	if first == second {
		t.Fatal("document projection was omitted from canonical self digest")
	}
}

func TestDocumentProjectionStrictDecodeAndSelfDigest(t *testing.T) {
	ledger := projectedLedger(t)
	body, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLedger(body); err != nil {
		t.Fatalf("rendered projection rejected: %v", err)
	}

	var row map[string]any
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatal(err)
	}
	projection := row["document_projection"].(map[string]any)
	projection["format"] = "changed"
	tampered, _ := json.Marshal(row)
	if _, err := DecodeLedger(tampered); err == nil || strictjson.CodeOf(err) != "wire_contract_invalid" {
		t.Fatalf("tampered projection rejection = %v", err)
	}

	nullBody := bytes.Replace(body, mustJSON(t, ledger.DocumentProjection), []byte("null"), 1)
	if _, err := DecodeLedger(nullBody); err == nil || strictjson.CodeOf(err) != "wire_shape_invalid" {
		t.Fatalf("null projection rejection = %v", err)
	}
	unknownBody := bytes.Replace(body, []byte(`"schema_version":1`), []byte(`"schema_version":1,"unknown":true`), 1)
	if _, err := DecodeLedger(unknownBody); err == nil || strictjson.CodeOf(err) != "wire_shape_invalid" {
		t.Fatalf("unknown projection key rejection = %v", err)
	}
}

func TestDocumentProjectionSharedLedgerHasPinnedCanonicalDigest(t *testing.T) {
	body := mustRead(t, "../../testdata/contracts/v4/markdown/ledger.json")
	ledger, err := DecodeLedger(body)
	if err != nil {
		t.Fatal(err)
	}
	const want = "7f88cfc54caf84e0e58224916815530ed6ed18f156e57f1c9ea7102bd67888c5"
	if got := CanonicalLedgerSHA256(ledger); got != want {
		t.Fatalf("canonical digest = %s, want %s", got, want)
	}
}

func TestDocumentProjectionPubliclyResignedFixtureHasPinnedCanonicalDigest(t *testing.T) {
	body := mustRead(t, "../../testdata/contracts/v4/markdown/ledger-rehashed-public.json")
	var ledger MachineLedger
	if err := strictjson.Decode(body, &ledger); err != nil {
		t.Fatal(err)
	}
	const want = "d3ea7b0eba5e77fccd2b2766b260b43d165b71bf8d720243c612d8263a112bee"
	if got := CanonicalLedgerSHA256(ledger); got != want {
		t.Fatalf("canonical digest = %s, want %s", got, want)
	}
}

func TestDocumentProjectionSharedCorpusLedgerOutcomes(t *testing.T) {
	var corpus markdownCorpus
	if err := strictjson.Decode(mustRead(t, "../../testdata/contracts/v4/markdown/cases.json"), &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != 1 || corpus.Format != "review-markdown-v1" || len(corpus.Cases) != 23 {
		t.Fatalf("invalid corpus identity: %+v", corpus)
	}
	foundRevisionMismatch := false
	for _, testCase := range corpus.Cases {
		if testCase.ExpectedDocumentCode != nil {
			if *testCase.ExpectedDocumentCode == "" {
				t.Fatalf("%s has an empty raw-document diagnostic", testCase.Name)
			}
			if _, ok := markdownCodes[*testCase.ExpectedDocumentCode]; !ok {
				t.Fatalf("%s has an unknown raw-document diagnostic %q", testCase.Name, *testCase.ExpectedDocumentCode)
			}
		}
		if testCase.ExpectedMarkdownCode != nil {
			if *testCase.ExpectedMarkdownCode == "" {
				t.Fatalf("%s has an empty parser-specific diagnostic", testCase.Name)
			}
			if _, ok := markdownCodes[*testCase.ExpectedMarkdownCode]; !ok {
				t.Fatalf("%s has an unknown parser-specific diagnostic %q", testCase.Name, *testCase.ExpectedMarkdownCode)
			}
		}
		if testCase.Name == "outer-inner-revision-mismatch" {
			foundRevisionMismatch = true
			if testCase.ExpectedCode != "wire_contract_invalid" {
				t.Fatalf("revision mismatch code = %q", testCase.ExpectedCode)
			}
		}
		t.Run(testCase.Name, func(t *testing.T) {
			body := mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Ledger)
			ledger, err := DecodeLedger(body)
			if got := strictjson.CodeOf(err); got != testCase.ExpectedCode {
				t.Fatalf("ledger rejection = %q, want %q: %v", got, testCase.ExpectedCode, err)
			}
			if err != nil {
				return
			}
			review := mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Review)
			history := mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.History)
			if testCase.ExpectedMarkdownCode == nil && (sha256Hex(review) != ledger.ReviewSHA256 || sha256Hex(history) != ledger.HistorySHA256) {
				t.Fatal("successful corpus case has stale review or history hash")
			}
			index, indexErr := sessionindex.Parse(mustRead(t, "../../testdata/contracts/v4/markdown/"+testCase.Index))
			if indexErr != nil {
				t.Fatal(indexErr)
			}
			if index.ProjectID != ledger.ProjectID || index.GenerationID != ledger.GenerationID || index.ProjectViewDigest != ledger.ProjectViewDigest || index.Digest != ledger.SyncHashes.SessionIndexDigest {
				t.Fatal("successful corpus case has stale index binding")
			}
		})
	}
	if !foundRevisionMismatch {
		t.Fatal("shared corpus is missing outer-inner-revision-mismatch")
	}
}

func projectedLedger(t *testing.T) MachineLedger {
	t.Helper()
	ledger := frozenLedger(t)
	ledger.MinimumReaderVersion = "0.4.1"
	ledger.MinimumWriterVersion = "0.4.1"
	presentation := minimumPresentation()
	presentation.ProjectID = ledger.ProjectID
	presentation.GenerationID = ledger.GenerationID
	presentation.ProjectViewDigest = ledger.ProjectViewDigest
	presentation.Revision = ledger.AcceptedRevision
	presentation.HumanPatches = append([]Patch{}, ledger.HumanPatches...)
	presentation.OrphanPatches = append([]Patch{}, ledger.OrphanPatches...)
	presentation.GeneratedBaselines = append([]Baseline{}, ledger.GeneratedBaselines...)
	ledger.DocumentProjection = &DocumentProjection{SchemaVersion: 1, Format: "review-markdown-v1", PresentationBase: presentation}
	return ledger
}

func minimumPatch() Patch {
	value := "value"
	return Patch{EntityID: "decision:id", Field: "title", Operation: "set", Value: &value, BaseGeneratedHash: strings.Repeat("1", 64)}
}

func minimumBaseline() Baseline {
	value := "value"
	return Baseline{GenerationID: "g", EntityID: "decision:id", Field: "title", Kind: "text", Value: &value, GeneratedHash: strings.Repeat("1", 64)}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := strictjson.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
