package modelpricewatch

import (
	"os"
	"strings"
	"testing"
)

func TestDecodeModelsPreservesUnknownAndFreeRatesAndFullEvidence(t *testing.T) {
	file, err := os.Open("testdata/models-min.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	catalog, err := DecodeModels(file, DefaultBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	row := catalog.Listings["provider-model-test"]
	if row.CachedInputPerMTok != nil || row.OutputPerMTok == nil || *row.OutputPerMTok != 0 {
		t.Fatalf("nullable/free rates lost: %#v", row)
	}
	if row.Evidence == nil || row.Evidence.Event != nil || row.Evidence.EvidenceURL == nil || len(row.Modality) != 1 || catalog.Count != 1 {
		t.Fatalf("full listing shape lost: %#v catalog=%#v", row, catalog)
	}
}

func TestDecodeModelsRejectsStructuralAndValueCorruption(t *testing.T) {
	base, err := os.ReadFile("testdata/models-min.json")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, body, want string }{
		{"duplicate key", strings.Replace(string(base), `"count":1`, `"count":1,"count":1`, 1), "duplicate"},
		{"unknown field", strings.Replace(string(base), `"count":1`, `"extra":true,"count":1`, 1), "unknown"},
		{"count mismatch", strings.Replace(string(base), `"count":1`, `"count":2`, 1), "count"},
		{"duplicate id", strings.Replace(string(base), `"data":[`, `"data":[`+strings.TrimSuffix(strings.TrimPrefix(string(base), `{"count":1,"updated":"2026-09-06","data":[`), `}`)+`,`, 1), "invalid"},
		{"negative price", strings.Replace(string(base), `"input_per_mtok":1.25`, `"input_per_mtok":-1`, 1), "price"},
		{"missing required nullable price", strings.Replace(string(base), `"cached_input_per_mtok":null,`, ``, 1), "required"},
		{"bad url", strings.Replace(string(base), `https://example.test/pricing`, `http://example.test/pricing`, 1), "URL"},
		{"trailing json", string(base) + ` {}`, "trailing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeModels(strings.NewReader(test.body), DefaultBodyLimit)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	_, err = DecodeModels(strings.NewReader(string(base)), int64(len(base)-1))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize err=%v", err)
	}
}

func TestDecodeModelsMarksEveryNonemptyPriceNoteConditional(t *testing.T) {
	body, err := os.ReadFile("testdata/models-min.json")
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"price_note":null`, `"price_note":"ordinary prose without known keywords"`, 1))
	catalog, err := DecodeModels(strings.NewReader(string(body)), DefaultBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.Listings["provider-model-test"].HasUnstructuredConditions {
		t.Fatal("nonempty price_note became executable pricing")
	}
}

func TestDecodeHistoryAcceptsFullAndMinimalEntriesAndRejectsMalformedNestedFields(t *testing.T) {
	file, err := os.Open("testdata/history-min.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	catalog, err := DecodeHistory(file, DefaultBodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	history := catalog.Models["provider-model-test"].History
	if len(history) != 2 || history[0].CachedInputPerMTok != nil || history[1].CorrectedFrom == nil || len(history[1].Changes) != 1 {
		t.Fatalf("history shape lost: %#v", history)
	}
	body, err := os.ReadFile("testdata/history-min.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []string{
		strings.Replace(string(body), `"direction":"down"`, `"direction":"down","bogus":1`, 1),
		strings.Replace(string(body), `"count":1`, `"count":2`, 1),
		strings.Replace(string(body), `"event":"baseline"`, `"event":"invented"`, 1),
	} {
		if _, err := DecodeHistory(strings.NewReader(mutate), DefaultBodyLimit); err == nil {
			t.Fatalf("accepted malformed history: %s", mutate)
		}
	}
}

func TestDecodeHistoryAcceptsObservedOptionalNullMetadata(t *testing.T) {
	body, err := os.ReadFile("testdata/history-min.json")
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), `"evidence_url":"https://example.test/evidence"`, `"evidence_url":null`, 1))
	body = []byte(strings.Replace(string(body), `"captured_at":"2026-09-06T12:00:00Z"`, `"captured_at":null`, 1))
	if _, err := DecodeHistory(strings.NewReader(string(body)), DefaultBodyLimit); err != nil {
		t.Fatal(err)
	}
}
