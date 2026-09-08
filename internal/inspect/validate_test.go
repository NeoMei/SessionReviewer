package inspect

import (
	"fmt"
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
	s.PhaseBoundaries = Block{Total: 1, Shown: 1, Items: []Entry{{OccurredAt: "a", Sequence: 1, RevisionID: "revision-1", SourceRevisionIDs: []string{"source-1", "source-1"}}}, Coverage: Coverage{Seen: 1, Indexed: 1}}
	if err := ValidateSummary(s); err == nil {
		t.Fatal("accepted duplicate source revision IDs")
	}
}

func TestValidateSummaryRejectsNonCanonicalOrderAfterValidEntryChecks(t *testing.T) {
	ordered := Block{
		Total: 2, Shown: 2, Coverage: Coverage{Seen: 2, Indexed: 2},
		Items: []Entry{
			{OccurredAt: "a", Sequence: 1, RevisionID: "revision-1", Text: "first", SourceRevisionIDs: []string{"source-1"}},
			{OccurredAt: "z", Sequence: 2, RevisionID: "revision-2", Text: "second", SourceRevisionIDs: []string{"source-2"}},
		},
	}
	for _, test := range []struct {
		name      string
		blockName string
		wantError string
	}{
		{name: "normal block", blockName: "phase_boundaries", wantError: "summary items are not in canonical order"},
		{name: "error block", blockName: "errors", wantError: "error items are not in canonical order"},
	} {
		t.Run(test.name, func(t *testing.T) {
			valid := minimumSummary()
			setSummaryTestBlock(&valid, test.blockName, ordered)
			if err := ValidateSummary(valid); err != nil {
				t.Fatalf("ordered positive control rejected before order check: %v", err)
			}

			reversed := ordered
			reversed.Items = []Entry{ordered.Items[1], ordered.Items[0]}
			invalid := minimumSummary()
			setSummaryTestBlock(&invalid, test.blockName, reversed)
			if err := ValidateSummary(invalid); err == nil || err.Error() != test.wantError {
				t.Fatalf("non-canonical order error=%v, want %q", err, test.wantError)
			}
		})
	}
}

func TestValidateSummaryRequiresSourcesInEveryDisplayedBlock(t *testing.T) {
	entry := Entry{OccurredAt: "2026-09-04T00:00:00Z", Sequence: 1, RevisionID: "revision-1", Text: "ok", SourceRevisionIDs: []string{}}
	for _, name := range []string{"phase_boundaries", "key_operations", "verification_results", "errors", "unresolved_questions"} {
		t.Run(name, func(t *testing.T) {
			summary := minimumSummary()
			setSummaryTestBlock(&summary, name, Block{Total: 1, Shown: 1, Coverage: Coverage{Seen: 1, Indexed: 1}, Items: []Entry{entry}})
			if err := ValidateSummary(summary); err == nil {
				t.Fatal("accepted displayed summary item without source revisions")
			}
		})
	}
}

func TestValidateSummaryRequiresBlockProjectionCoverage(t *testing.T) {
	valid := Block{
		Total: 40, Shown: 32, Omitted: 8,
		Coverage: Coverage{Seen: 40, Indexed: 32, Unprojected: 8},
		Items:    make([]Entry, 32),
	}
	for index := range valid.Items {
		valid.Items[index] = Entry{OccurredAt: "2026-09-04T00:00:00Z", Sequence: uint64(index + 1), RevisionID: fmt.Sprintf("revision-%02d", index+1), Text: "ok", SourceRevisionIDs: []string{fmt.Sprintf("source-%02d", index+1)}}
	}
	invalid := []struct {
		name     string
		coverage Coverage
	}{
		{name: "zero coverage", coverage: Coverage{}},
		{name: "shifted buckets", coverage: Coverage{Seen: 40, Indexed: 31, Collapsed: 1, Unprojected: 8}},
		{name: "collapsed omission", coverage: Coverage{Seen: 40, Indexed: 32, Collapsed: 8}},
		{name: "undecodable omission", coverage: Coverage{Seen: 40, Indexed: 32, Undecodable: 8}},
		{name: "truncated omission", coverage: Coverage{Seen: 40, Indexed: 32, Truncated: 8}},
	}
	for _, name := range []string{"phase_boundaries", "key_operations", "verification_results", "errors", "unresolved_questions"} {
		t.Run(name, func(t *testing.T) {
			for _, test := range invalid {
				t.Run(test.name, func(t *testing.T) {
					summary := minimumSummary()
					block := valid
					block.Coverage = test.coverage
					setSummaryTestBlock(&summary, name, block)
					if err := ValidateSummary(summary); err == nil {
						t.Fatal("accepted block coverage that contradicts projection counts")
					}
				})
			}
			summary := minimumSummary()
			setSummaryTestBlock(&summary, name, valid)
			summary.Coverage = Coverage{Seen: 9, Indexed: 3, Unprojected: 2, Undecodable: 4}
			if err := ValidateSummary(summary); err != nil {
				t.Fatalf("valid capped block or independent top-level source gaps rejected: %v", err)
			}
		})
	}
	if err := ValidateSummary(minimumSummary()); err != nil {
		t.Fatalf("valid zero blocks rejected: %v", err)
	}
}

func setSummaryTestBlock(summary *SessionSummary, name string, block Block) {
	switch name {
	case "phase_boundaries":
		summary.PhaseBoundaries = block
	case "key_operations":
		summary.KeyOperations = block
	case "verification_results":
		summary.VerificationResults = block
	case "errors":
		items := make([]ErrorEntry, len(block.Items))
		for index, entry := range block.Items {
			items[index] = ErrorEntry{Code: "observed_error", OccurredAt: entry.OccurredAt, Sequence: entry.Sequence, RevisionID: entry.RevisionID, Text: entry.Text, SourceRevisionIDs: entry.SourceRevisionIDs}
		}
		summary.Errors = ErrorBlock{Total: block.Total, Shown: block.Shown, Omitted: block.Omitted, Coverage: block.Coverage, Items: items}
	case "unresolved_questions":
		summary.UnresolvedQuestions = block
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
