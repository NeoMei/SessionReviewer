package sourcecatalog

import (
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/memory"
	"github.com/neomei/SessionReviewer/internal/source"
)

func TestCanonicalCatalogAppendReplaceAndReadback(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "direct"
		if batch {
			name = "batch"
		}
		t.Run(name, func(t *testing.T) {
			catalog := openCatalog(t, t.TempDir())
			record := sourceRecord("canonical-session", []string{"project-a"}, 10)
			record.Provider = "opencode"
			record.FrozenBoundary.Location = memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: 9}}
			digest, err := catalog.UpsertSource(record)
			if err != nil {
				t.Fatal(err)
			}
			appended := record
			appended.FrozenBoundary.Location.Canonical = &memory.CanonicalSourceLocation{Record: 10}
			appended.FrozenBoundary.SourceHash = strings.Repeat("b", 64)
			if batch {
				results, err := catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryAppend, ExpectedDigest: digest, Desired: appended}})
				if err != nil {
					t.Fatal(err)
				}
				digest = results[0].Digest
			} else {
				digest, err = catalog.UpsertSource(appended)
				if err != nil {
					t.Fatal(err)
				}
			}
			replacement := record
			replacement.FrozenBoundary.SourceHash = strings.Repeat("c", 64)
			if batch {
				_, err = catalog.ApplyBatch([]BatchMutation{{Relation: source.BoundaryReplacement, ExpectedDigest: digest, Desired: replacement}})
			} else {
				_, err = catalog.ReplaceSource(digest, replacement)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := catalog.GetSource("opencode", "canonical-session")
			if err != nil || !found || got.FrozenBoundary.Location.RecordOrdinal() != 9 || got.FrozenBoundary.SourceHash != strings.Repeat("c", 64) || got.FrozenBoundary.Location.JSONL != nil {
				t.Fatalf("canonical readback=%+v found=%v err=%v", got, found, err)
			}
		})
	}
}

func TestCanonicalBoundaryComparisonRejectsChangedHashAndMixedKinds(t *testing.T) {
	canonical := memory.FrozenBoundary{Location: memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: 9}}, SourceHash: strings.Repeat("a", 64)}
	if got, err := compareBoundary(canonical, canonical); got != 0 || err != nil {
		t.Fatalf("equal boundary=%d err=%v", got, err)
	}
	changed := canonical
	changed.SourceHash = strings.Repeat("b", 64)
	if _, err := compareBoundary(canonical, changed); err == nil {
		t.Fatal("same canonical ordinal with changed hash accepted")
	}
	changed.Location = memory.SourceLocation{Kind: memory.SourceLocationJSONL, JSONL: &memory.JSONLSourceLocation{Line: 9, ByteOffset: 128}}
	if _, err := compareBoundary(canonical, changed); err == nil {
		t.Fatal("incomparable coordinate kinds accepted")
	}
}

func TestCaseSensitiveSessionCatalogKeysStayDistinct(t *testing.T) {
	catalog := openCatalog(t, t.TempDir())
	for _, id := range []string{"ses_nativeRoot", "ses_nativeroot"} {
		record := sourceRecord(id, []string{"project-a"}, 10)
		record.Provider = "opencode"
		record.SourceIdentity = "source-case"
		record.FrozenBoundary.Location = memory.SourceLocation{Kind: memory.SourceLocationCanonical, Canonical: &memory.CanonicalSourceLocation{Record: 9}}
		if _, err := catalog.UpsertSource(record); err != nil {
			t.Fatalf("catalog rejected case-sensitive ID %q: %v", id, err)
		}
	}
	for _, id := range []string{"ses_nativeRoot", "ses_nativeroot"} {
		record, found, err := catalog.GetSource("opencode", id)
		if err != nil || !found || record.SessionID != id {
			t.Fatalf("catalog case collision for %q: record=%+v found=%v err=%v", id, record, found, err)
		}
	}
}
