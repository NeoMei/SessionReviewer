package contextupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
)

func TestV4ScanMapsCanonicalIndexAndPreservesHumanStructures(t *testing.T) {
	accepted := v4AcceptedFixture(t)
	for len(accepted.Review.Timeline) < 17 {
		item := accepted.Review.Timeline[0]
		item.ID = fmt.Sprintf("milestone-extra-%d", len(accepted.Review.Timeline))
		accepted.Review.Timeline = append(accepted.Review.Timeline, item)
	}
	original := accepted.Review
	originalPricing := accepted.Ledger.PricingSnapshots
	index := accepted.SessionIndex
	seed := index.Sessions[0]
	index.Sessions = make([]sessionindex.Entry, 154)
	for i := range index.Sessions {
		entry := seed
		entry.SessionID = fmt.Sprintf("session-%03d", i)
		index.Sessions[i] = entry
	}
	index.Coverage.Total, index.Coverage.Complete = 154, 154
	index.Coverage.SourceAvailable, index.Coverage.StartedAtKnown, index.Coverage.EndedAtKnown = 154, 154, 154
	index.GenerationID = "generation-2"
	index.ProjectViewDigest = "sha256:" + strings.Repeat("2", 64)
	indexBody, err := sessionindex.Render(index)
	if err != nil {
		t.Fatal(err)
	}
	index, err = sessionindex.Parse(indexBody)
	if err != nil {
		t.Fatal(err)
	}
	nextPresentation, nextLedger, err := mapV4Scan(v4MapInput{Accepted: accepted, Index: index, Accounting: accounting.ProjectSummary{TotalDurationMS: 12, TotalTokens: 34}})
	if err != nil {
		t.Fatal(err)
	}
	if len(nextPresentation.Timeline) != 17 || !reflect.DeepEqual(nextPresentation.Decisions, original.Decisions) || !reflect.DeepEqual(nextPresentation.Risks, original.Risks) || !reflect.DeepEqual(nextPresentation.OpenLoops, original.OpenLoops) || !reflect.DeepEqual(nextPresentation.ProblemRootIDs, original.ProblemRootIDs) || !reflect.DeepEqual(nextPresentation.ProblemNodes, original.ProblemNodes) || !reflect.DeepEqual(nextPresentation.ChainDependencies, original.ChainDependencies) || !reflect.DeepEqual(nextPresentation.HumanPatches, original.HumanPatches) || !reflect.DeepEqual(nextPresentation.OrphanPatches, original.OrphanPatches) || !reflect.DeepEqual(nextPresentation.GeneratedBaselines, original.GeneratedBaselines) || !reflect.DeepEqual(nextLedger.PricingSnapshots, originalPricing) {
		t.Fatal("scan mapping dropped accepted human structure or timeline")
	}
	for i := range original.Timeline {
		got := nextPresentation.Timeline[i]
		got.GenerationID = original.Timeline[i].GenerationID
		if !reflect.DeepEqual(got, original.Timeline[i]) {
			t.Fatalf("timeline[%d] conclusion, verification, refs, or coverage changed: got=%+v want=%+v", i, got, original.Timeline[i])
		}
	}
	if nextPresentation.GenerationID != index.GenerationID || nextPresentation.ProjectViewDigest != index.ProjectViewDigest || nextPresentation.Revision != original.Revision+1 || nextLedger.SyncHashes.SessionIndexDigest != index.Digest || len(nextLedger.Sessions) != 154 || len(nextLedger.PricingSnapshots) != len(accepted.Ledger.PricingSnapshots) {
		t.Fatalf("mapped identities/facts=%+v ledger=%+v", nextPresentation, nextLedger)
	}
}

func v4AcceptedFixture(t *testing.T) reviewv4.Accepted {
	t.Helper()
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "markdown", name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	accepted, err := reviewv4.LoadProjection(read("review.md"), read("history.md"), read("ledger.json"), read("index.json"))
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
