package sessionindex

import (
	"math"
	"os"
	"testing"

	"github.com/neomei/SessionReviewer/internal/strictjson"
)

func TestParseFrozenValidFixture(t *testing.T) {
	b, e := os.ReadFile("../../testdata/contracts/v4/session-index-v1.valid.json")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Parse(b); e != nil {
		t.Fatal(e)
	}
}

func TestValidateRejectsCoverageAdditionOverflow(t *testing.T) {
	document := oneSessionDocument()
	document.Sessions[0].Coverage = Coverage{Seen: 0, Indexed: math.MaxUint64, Collapsed: 1}
	document.Sessions[0].IndexedEventCount = math.MaxUint64
	if err := Validate(document); err == nil {
		t.Fatal("accepted wrapped session coverage")
	}
}

func TestValidateIdentityUsesProviderAndSessionIDPair(t *testing.T) {
	d := minimumDocument()
	d.Sessions = []Entry{{Provider: "claude", SessionID: "same", ProcessingState: ProcessingComplete, SourceAvailability: "available", StartedAt: "now", EndedAt: "now"}, {Provider: "codex", SessionID: "same", ProcessingState: ProcessingComplete, SourceAvailability: "available", StartedAt: "now", EndedAt: "now"}}
	d.Coverage.Total = 2
	d.Coverage.Complete = 2
	d.Coverage.SourceAvailable = 2
	d.Coverage.StartedAtKnown = 2
	d.Coverage.EndedAtKnown = 2
	if err := Validate(d); err != nil {
		t.Fatal(err)
	}
	d.Sessions[0].Provider = "codex"
	if err := Validate(d); err == nil {
		t.Fatal("accepted duplicate provider/session identity")
	}
}

func TestValidateCoverageAndDigest(t *testing.T) {
	d := minimumDocument()
	d.Coverage.Total = 1
	if err := Validate(d); err == nil {
		t.Fatal("accepted coverage mismatch")
	}
}

func TestParseRejectsFrozenInvalidFixture(t *testing.T) {
	b, err := os.ReadFile("../../testdata/contracts/v4/session-index-v1.invalid.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(b); err == nil {
		t.Fatal("accepted frozen invalid fixture")
	} else if got := strictjson.CodeOf(err); got != "wire_contract_invalid" {
		t.Fatalf("rejection code = %q, want wire_contract_invalid: %v", got, err)
	}
}

func TestSessionIndexRejectsInvalidStateReasonAndDigestFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Entry)
	}{
		{name: "unknown state reason", mutate: func(e *Entry) { e.StateReasonCodes = []string{"not-in-contract"} }},
		{name: "usage digest", mutate: func(e *Entry) { e.UsageRecordDigest = strptr("bad") }},
		{name: "summary digest", mutate: func(e *Entry) { e.SummaryDigest = strptr("bad") }},
		{name: "last seen generation too long", mutate: func(e *Entry) { e.LastSeenGenerationID = strptr(string(make([]byte, 257))) }},
		{name: "last successful generation too long", mutate: func(e *Entry) { e.LastSuccessfulGenerationID = strptr(string(make([]byte, 257))) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := oneSessionDocument()
			tc.mutate(&d.Sessions[0])
			if err := Validate(d); err == nil {
				t.Fatal("accepted invalid session field")
			}
		})
	}
}

func TestRenderCalculatesDigestAndIsDeterministic(t *testing.T) {
	d := minimumDocument()
	one, err := Render(d)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Render(d)
	if err != nil || string(one) != string(two) {
		t.Fatalf("non-deterministic render: %v", err)
	}
	parsed, err := Parse(one)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Digest == "sha256:"+zeros || parsed.Digest != CanonicalDigest(parsed) {
		t.Fatalf("digest=%q", parsed.Digest)
	}
}

func oneSessionDocument() Document {
	d := minimumDocument()
	d.Sessions = []Entry{{Provider: "codex", SessionID: "same", ProcessingState: ProcessingComplete, StateReasonCodes: []string{}, SourceAvailability: "available", StartedAt: "now", EndedAt: "now", Coverage: Coverage{}}}
	d.Coverage = IndexCoverage{Total: 1, Complete: 1, SourceAvailable: 1, StartedAtKnown: 1, EndedAtKnown: 1}
	return d
}

func strptr(value string) *string { return &value }

func minimumDocument() Document {
	return Document{SchemaVersion: 1, MinimumReaderVersion: "0.4.0", Digest: "sha256:" + zeros, ProjectID: "project-p", GenerationID: "generation-1", ProjectViewDigest: "sha256:" + ones, GeneratedAt: "2026-09-04T00:00:00Z", SortVersion: SortVersion, Sessions: []Entry{}, Coverage: IndexCoverage{}}
}

const zeros = "0000000000000000000000000000000000000000000000000000000000000000"
const ones = "1111111111111111111111111111111111111111111111111111111111111111"
