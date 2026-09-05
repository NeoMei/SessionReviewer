package inspect

import (
	"math"
	"os"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestRenderFrozenValidSummaryFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/v4/session-summary-v1.valid.json")
	if e != nil {
		t.Fatal(e)
	}
	var s SessionSummary
	if e = strictjson.Decode(b, &s); e != nil {
		t.Fatal(e)
	}
	if _, e = RenderSummary(s); e != nil {
		t.Fatal(e)
	}
}

func TestValidateRejectsCoverageAdditionOverflow(t *testing.T) {
	summary := minimumSummary()
	summary.Coverage = Coverage{Seen: 0, Indexed: math.MaxUint64, Collapsed: 1}
	if err := ValidateSummary(summary); err == nil {
		t.Fatal("accepted wrapped summary coverage")
	}
}

func TestInspectionContractsRejectIntegersAboveJavaScriptSafeMaximum(t *testing.T) {
	unsafe := uint64(1 << 53)

	t.Run("summary coverage", func(t *testing.T) {
		summary := minimumSummary()
		summary.Coverage = Coverage{Seen: unsafe, Indexed: unsafe}
		if err := ValidateSummary(summary); err == nil {
			t.Fatal("accepted unsafe summary coverage")
		}
	})
	t.Run("summary sequence", func(t *testing.T) {
		summary := minimumSummary()
		summary.PhaseBoundaries = Block{
			Total: 1, Shown: 1, Coverage: Coverage{Seen: 1, Indexed: 1},
			Items: []Entry{{OccurredAt: "2026-09-04T00:00:00Z", Sequence: unsafe, RevisionID: "revision-1", SourceRevisionIDs: []string{}}},
		}
		if err := ValidateSummary(summary); err == nil {
			t.Fatal("accepted unsafe summary sequence")
		}
	})
	t.Run("event page range and coverage", func(t *testing.T) {
		page := minimumEventPage()
		page.Total, page.RangeStart, page.RangeEnd = unsafe, unsafe, unsafe
		page.Coverage = Coverage{Seen: unsafe, Indexed: unsafe}
		if err := ValidateEventPage(page); err == nil {
			t.Fatal("accepted unsafe event-page range and coverage")
		}
	})
	t.Run("event sequence", func(t *testing.T) {
		page := minimumEventPage()
		page.Total, page.RangeEnd, page.Coverage = 1, 1, Coverage{Seen: 1, Indexed: 1}
		page.Items = []EventItem{{Kind: "message", RevisionID: "revision-1", Sequence: unsafe}}
		if err := ValidateEventPage(page); err == nil {
			t.Fatal("accepted unsafe event sequence")
		}
	})
}

func TestParsersRejectFrozenInvalidFixtures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		wantCode string
		parse    func([]byte) error
	}{
		{name: "summary", path: "../../testdata/contracts/v4/session-summary-v1.invalid.json", wantCode: "wire_shape_invalid", parse: func(b []byte) error { _, err := ParseSummary(b); return err }},
		{name: "event page", path: "../../testdata/contracts/v4/session-event-page-v1.invalid.json", wantCode: "wire_contract_invalid", parse: func(b []byte) error { _, err := ParseEventPage(b); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.parse(b); err == nil {
				t.Fatal("accepted frozen invalid fixture")
			} else if got := strictjson.CodeOf(err); got != tc.wantCode {
				t.Fatalf("rejection code = %q, want %s: %v", got, tc.wantCode, err)
			}
		})
	}
}

func TestValidateSummaryRejectsInvalidItemsRulesAndSort(t *testing.T) {
	s := minimumSummary()
	s.PhaseBoundaries = Block{Total: 1, Shown: 1, Items: []Entry{{OccurredAt: "2026-09-04T00:00:00Z", Sequence: 1, RevisionID: "revision-1", Text: "ok", SourceRevisionIDs: []string{"bad revision"}}}, Coverage: Coverage{Seen: 1, Indexed: 1}}
	if err := ValidateSummary(s); err == nil {
		t.Fatal("accepted invalid source revision ID")
	}
	s = minimumSummary()
	s.Rules.DependencyDigests = []string{"bad"}
	if err := ValidateSummary(s); err == nil {
		t.Fatal("accepted invalid rule dependency digest")
	}
	s = minimumSummary()
	s.PhaseBoundaries = Block{Total: 2, Shown: 2, Items: []Entry{{OccurredAt: "z", Sequence: 2, RevisionID: "revision-2", SourceRevisionIDs: []string{}}, {OccurredAt: "a", Sequence: 1, RevisionID: "revision-1", SourceRevisionIDs: []string{}}}, Coverage: Coverage{Seen: 2, Indexed: 2}}
	if err := ValidateSummary(s); err == nil {
		t.Fatal("accepted unstable summary item order")
	}
}

func TestValidateEventPageRejectsUnknownKindAndTooManyItems(t *testing.T) {
	p := minimumEventPage()
	p.Total, p.RangeEnd, p.Coverage = 1, 1, Coverage{Seen: 1, Indexed: 1}
	p.Items = []EventItem{{Kind: "unknown", RevisionID: "revision-1", Sequence: 1}}
	if err := ValidateEventPage(p); err == nil {
		t.Fatal("accepted unknown event kind")
	}
	p = minimumEventPage()
	p.Total, p.RangeEnd, p.Coverage = 101, 101, Coverage{Seen: 101, Indexed: 101}
	p.Items = make([]EventItem, 101)
	if err := ValidateEventPage(p); err == nil {
		t.Fatal("accepted event page above 100 items")
	}
}

func minimumSummary() SessionSummary {
	empty := Block{Items: []Entry{}}
	return SessionSummary{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "p", Provider: "codex", SessionID: "s", GenerationID: "g", SessionViewDigest: "sha256:" + ones, PhaseBoundaries: empty, KeyOperations: empty, VerificationResults: empty, Errors: ErrorBlock{Items: []ErrorEntry{}}, UnresolvedQuestions: empty, Rules: Rules{RuleID: "rule", RuleVersion: "v1", DependencyDigests: []string{}}}
}

func minimumEventPage() SessionEventPage {
	return SessionEventPage{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "p", Provider: "codex", SessionID: "s", GenerationID: "g", SessionViewDigest: "sha256:" + ones, Items: []EventItem{}}
}

func TestValidateEventPageRejectsCursorWhenTotalZero(t *testing.T) {
	cursor := "cursor"
	p := SessionEventPage{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", ProjectID: "p", Provider: "codex", SessionID: "s", GenerationID: "g", SessionViewDigest: "sha256:" + ones, PreviousCursor: &cursor}
	if err := ValidateEventPage(p); err == nil {
		t.Fatal("accepted cursor for empty page")
	}
}

const ones = "1111111111111111111111111111111111111111111111111111111111111111"
