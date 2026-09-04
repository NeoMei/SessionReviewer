package reviewv4

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestRenderFrozenValidLedgerFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	if e != nil {
		t.Fatal(e)
	}
	l, e := DecodeLedger(b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = RenderLedger(l); e != nil {
		t.Fatal(e)
	}
}

func TestReviewParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"unknown":true}`), []byte(`{}`), []byte(`{}`)); err == nil {
		t.Fatal("accepted unknown review fields")
	}
}

func TestFrozenInvalidReviewAndLedgerFixturesAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		fn   func([]byte) error
	}{
		{name: "review", path: "../../testdata/contracts/v4/review-presentation-v4.invalid.json", fn: func(b []byte) error { _, err := DecodePresentation(b); return err }},
		{name: "ledger", path: "../../testdata/contracts/v4/machine-ledger-v4.invalid.json", fn: func(b []byte) error { _, err := DecodeLedger(b); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.fn(b); err == nil {
				t.Fatal("accepted frozen invalid fixture")
			}
		})
	}
}

func TestValidatePresentationRejectsDecisionCycleAndBrokenGraph(t *testing.T) {
	p := minimumPresentation()
	p.Decisions = []Decision{minimumDecision("a", []string{"b"}), minimumDecision("b", []string{"a"})}
	if err := ValidatePresentation(p); err == nil {
		t.Fatal("accepted supersession cycle")
	}
	p.Decisions = []Decision{minimumDecision("old", nil), minimumDecision("new", []string{"old"})}
	p.Decisions[0].Status = DecisionSuperseded
	if err := ValidatePresentation(p); err != nil {
		t.Fatal(err)
	}
	p.Decisions[1].Supersedes = nil
	if err := ValidatePresentation(p); err == nil {
		t.Fatal("accepted superseded decision without successor")
	}
}

func TestLoadProjectionEnforcesAllIdentityAndDigestBindings(t *testing.T) {
	reviewFixture := mustRead(t, "../../testdata/contracts/v4/review-presentation-v4.valid.json")
	indexFixture := mustRead(t, "../../testdata/contracts/v4/session-index-v1.valid.json")
	review, err := DecodePresentation(reviewFixture)
	if err != nil {
		t.Fatal(err)
	}
	index, err := sessionindex.Parse(indexFixture)
	if err != nil {
		t.Fatal(err)
	}
	indexBytes, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	index, err = sessionindex.Parse(indexBytes)
	if err != nil {
		t.Fatal(err)
	}
	ledgerFixture := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	ledger, err := DecodeLedger(ledgerFixture)
	if err != nil {
		t.Fatal(err)
	}
	ledger.AcceptedRevision = review.Revision
	history := reviewFixture
	ledger.ReviewSHA256 = sha256hex(reviewFixture)
	ledger.HistorySHA256 = sha256hex(history)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledger.SyncHashes.SessionIndexDigest = index.Digest
	ledger.SyncHashes.LedgerSHA256 = strings.Repeat("0", 64)
	ledgerBytes, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := LoadProjection(reviewFixture, history, ledgerBytes, indexBytes)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.SessionIndex.ProjectID != review.ProjectID {
		t.Fatalf("validated index missing from Accepted: %+v", accepted.SessionIndex)
	}
	if _, err := LoadProjection(reviewFixture, history, ledgerBytes, nil); err == nil {
		t.Fatal("accepted projection without required session index")
	}

	tests := []struct {
		name   string
		mutate func(*Presentation, *MachineLedger, *sessionindex.Document, *[]byte)
	}{
		{name: "project ID", mutate: func(_ *Presentation, l *MachineLedger, _ *sessionindex.Document, _ *[]byte) { l.ProjectID = "other" }},
		{name: "generation ID", mutate: func(_ *Presentation, _ *MachineLedger, i *sessionindex.Document, _ *[]byte) { i.GenerationID = "other" }},
		{name: "project view digest", mutate: func(p *Presentation, _ *MachineLedger, _ *sessionindex.Document, _ *[]byte) {
			p.ProjectViewDigest = "sha256:" + strings.Repeat("9", 64)
		}},
		{name: "review digest", mutate: func(_ *Presentation, l *MachineLedger, _ *sessionindex.Document, _ *[]byte) {
			l.ReviewSHA256 = strings.Repeat("9", 64)
			l.SyncHashes.ReviewSHA256 = l.ReviewSHA256
		}},
		{name: "history digest", mutate: func(_ *Presentation, l *MachineLedger, _ *sessionindex.Document, h *[]byte) {
			*h = append(*h, ' ')
			l.HistorySHA256 = strings.Repeat("9", 64)
			l.SyncHashes.HistorySHA256 = l.HistorySHA256
		}},
		{name: "index digest", mutate: func(_ *Presentation, l *MachineLedger, _ *sessionindex.Document, _ *[]byte) {
			l.SyncHashes.SessionIndexDigest = "sha256:" + strings.Repeat("9", 64)
		}},
		{name: "ledger top sync disagreement", mutate: func(_ *Presentation, l *MachineLedger, _ *sessionindex.Document, _ *[]byte) {
			l.SyncHashes.ReviewSHA256 = strings.Repeat("8", 64)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := review
			l := ledger
			i := index
			h := append([]byte(nil), history...)
			tc.mutate(&p, &l, &i, &h)
			rb, _ := jsonBytes(p)
			ib, _ := sessionindex.Render(i)
			lb, _ := RenderLedger(l)
			if _, err := LoadProjection(rb, h, lb, ib); err == nil {
				t.Fatal("accepted mismatched projection")
			}
		})
	}
}

func TestParseAcceptsRawMarkdownHistoryAndRejectsInvalidUTF8(t *testing.T) {
	review := mustRead(t, "../../testdata/contracts/v4/review-presentation-v4.valid.json")
	ledgerBytes := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	ledger, err := DecodeLedger(ledgerBytes)
	if err != nil {
		t.Fatal(err)
	}
	ledger.ReviewSHA256 = sha256hex(review)
	ledger.AcceptedRevision = 1
	history := []byte("# 项目历史\n\n- 保留人类可读的里程碑。\n")
	ledger.HistorySHA256 = sha256hex(history)
	ledger.SyncHashes.ReviewSHA256 = ledger.ReviewSHA256
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledger.SyncHashes.LedgerSHA256 = strings.Repeat("0", 64)
	ledgerBytes, _ = RenderLedger(ledger)
	if accepted, err := Parse(review, history, ledgerBytes); err != nil || !bytes.Equal(accepted.History, history) {
		t.Fatalf("raw markdown history rejected or changed: %v", err)
	}
	invalid := []byte{0xff}
	ledger.HistorySHA256 = sha256hex(invalid)
	ledger.SyncHashes.HistorySHA256 = ledger.HistorySHA256
	ledgerBytes, _ = RenderLedger(ledger)
	if _, err := Parse(review, invalid, ledgerBytes); err == nil {
		t.Fatal("accepted invalid UTF-8 history")
	}
}

func TestDecodeLedgerRejectsTamperedSelfDigest(t *testing.T) {
	fixture := mustRead(t, "../../testdata/contracts/v4/machine-ledger-v4.valid.json")
	ledger, err := DecodeLedger(fixture)
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	raw["sync_hashes"].(map[string]any)["ledger_sha256"] = strings.Repeat("9", 64)
	body, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeLedger(body); err == nil {
		t.Fatal("accepted tampered ledger self digest")
	}
}

func minimumPresentation() Presentation {
	return Presentation{SchemaVersion: 4, MinimumReaderVersion: "0.4.0", MinimumWriterVersion: "0.4.0", ProjectID: "p", GenerationID: "g", ProjectViewDigest: "sha256:" + strings.Repeat("1", 64), CurrentState: CurrentState{}, Timeline: []Timeline{}, Decisions: []Decision{}, Risks: []Risk{}, OpenLoops: []OpenLoop{}, HumanPatches: []Patch{}, OrphanPatches: []Patch{}, GeneratedBaselines: []Baseline{}}
}

func minimumDecision(id string, supersedes []string) Decision {
	return Decision{ID: id, Kind: "decision", Status: DecisionActive, Supersedes: supersedes, MilestoneIDs: []string{}, SessionRefs: []SessionRef{}, Provenance: "human_created", Revision: 1}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func jsonBytes(value any) ([]byte, error) { return json.Marshal(value) }
