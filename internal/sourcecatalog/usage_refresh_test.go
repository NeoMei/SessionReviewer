package sourcecatalog

import (
	"errors"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/accounting"
	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/source"
)

func TestUsageRefreshUpdatesOnlyUsageWithExactCAS(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	original := sourceRecord("ses_usageRefresh", []string{"project-a"}, 10)
	original.Provider = "opencode"
	original.SourceIdentity = "source-refresh"
	original.FrozenBoundary.Location = memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: 2}}
	digest, err := catalog.UpsertSource(original)
	if err != nil {
		t.Fatal(err)
	}
	unknown := original
	unknown.Usage.Models = []accounting.ModelUsage{}
	unknown.Usage.TotalTokens = 0
	relation := source.BoundaryRelation("usage_refresh")
	results, err := catalog.ApplyBatch([]BatchMutation{{Relation: relation, ExpectedDigest: digest, Desired: unknown}})
	if err != nil {
		t.Fatalf("known-to-unknown refresh failed: %v", err)
	}
	for name, mutate := range map[string]func(*memory.SourceRecord){
		"source hash": func(r *memory.SourceRecord) { r.FrozenBoundary.SourceHash = strings.Repeat("b", 64) },
		"ordinal": func(r *memory.SourceRecord) {
			r.FrozenBoundary.Location.Canonical = &memory.CanonicalSourceLocation{Record: 3}
		},
		"end": func(r *memory.SourceRecord) {
			r.EndedAt = "2026-08-31T10:00:02Z"
			r.Usage.EndedAt = r.EndedAt
			r.Usage.DurationMS = 2000
		},
		"availability":        func(r *memory.SourceRecord) { r.Availability = memory.SourceUnavailable },
		"add association":     func(r *memory.SourceRecord) { r.ProjectIDs = []string{"project-a", "project-b"} },
		"replace association": func(r *memory.SourceRecord) { r.ProjectIDs = []string{"project-b"} },
		"identity":            func(r *memory.SourceRecord) { r.SourceIdentity = "source-changed" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			mutate(&changed)
			if _, err := catalog.ApplyBatch([]BatchMutation{{Relation: relation, ExpectedDigest: results[0].Digest, Desired: changed}}); err == nil {
				t.Fatal("usage refresh changed non-usage authority")
			}
		})
	}
	if _, err := catalog.ApplyBatch([]BatchMutation{{Relation: relation, ExpectedDigest: digest, Desired: original}}); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("stale refresh bypassed CAS: %v", err)
	}
	if _, err := catalog.ApplyBatch([]BatchMutation{{Relation: relation, ExpectedDigest: results[0].Digest, Desired: original}}); err != nil {
		t.Fatalf("unknown-to-known refresh failed: %v", err)
	}
	got, found, err := catalog.GetSource("opencode", "ses_usageRefresh")
	if err != nil || !found || got.Usage.TotalTokens != 10 || got.FrozenBoundary.SourceHash != original.FrozenBoundary.SourceHash || got.FrozenBoundary.Location.RecordOrdinal() != 2 || len(got.ProjectIDs) != 1 || got.ProjectIDs[0] != "project-a" {
		t.Fatalf("refresh lost exact source authority: %+v found=%v err=%v", got, found, err)
	}
}
