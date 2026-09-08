package syncproject

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/neomei/SessionReviewer/internal/reviewv2"
	"github.com/neomei/SessionReviewer/internal/reviewv4"
	"github.com/neomei/SessionReviewer/internal/sessionindex"
	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

func TestDetectFormatAcceptsCurrentMarkdownPendingDraft(t *testing.T) {
	fixture, _ := newMarkdownLockFixture(t)
	reviewPath := filepath.Join(fixture.project, filepath.FromSlash(reviewv2.ReviewRelativePath))
	review, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := reviewv4.ParseMarkdownDocument(reviewv2.ReviewRelativePath, review)
	if err != nil {
		t.Fatal(err)
	}
	edited, err := document.ReplaceFields(map[reviewv4.FieldKey]string{
		{Entity: "project-overview", Name: "goal"}: "pending format draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewPath, edited, 0o600); err != nil {
		t.Fatal(err)
	}

	format, err := DetectFormat(t.Context(), Options{
		ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
		GOOS: runtime.GOOS, Trigger: syncengine.TriggerCLI,
	})
	if err != nil || format != ProjectionMarkdown {
		t.Fatalf("format=%q err=%v", format, err)
	}
}

func TestDetectFormatRejectsCurrentMarkdownMalformedOrMismatchedIndex(t *testing.T) {
	for _, test := range []struct {
		name string
		body func([]byte) []byte
	}{
		{name: "malformed", body: func([]byte) []byte { return []byte("{malformed-index") }},
		{name: "mismatched digest", body: func(body []byte) []byte {
			index, err := sessionindex.Parse(body)
			if err != nil {
				t.Fatal(err)
			}
			index.GeneratedAt = "2026-09-05T00:00:00Z"
			body, err = sessionindex.Render(index)
			if err != nil {
				t.Fatal(err)
			}
			return body
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, _ := newMarkdownLockFixture(t)
			indexPath := filepath.Join(fixture.project, "docs/session-review/.session-reviewer/session-index.json")
			index, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(indexPath, test.body(index), 0o600); err != nil {
				t.Fatal(err)
			}

			format, err := DetectFormat(t.Context(), Options{
				ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
				GOOS: runtime.GOOS, Trigger: syncengine.TriggerCLI,
			})
			if err == nil || format != "" {
				t.Fatalf("format=%q err=%v", format, err)
			}
		})
	}
}

// Treating every ledger that is not v4 as corrupt prevents a valid v3
// project from reaching the explicit migration path.
func TestDetectFormatAcceptsValidatedV3Projection(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	seedMigrationPreparedGeneration(t, fixture)

	format, err := DetectFormat(t.Context(), Options{
		ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data,
		GOOS: runtime.GOOS, Trigger: syncengine.TriggerCLI,
	})
	if err != nil || format != ProjectionV3 {
		t.Fatalf("format=%q err=%v", format, err)
	}
}

// Falling back to the frontmatter-only legacy detector after a present v4
// ledger fails decoding would misroute corruption as an explicit migration.
func TestDetectFormatRejectsPresentMalformedV4LedgerWithoutLegacyFallback(t *testing.T) {
	fixture := newMigrationServiceFixture(t)
	root := filepath.Join(fixture.project, "docs", "session-review")
	if err := os.MkdirAll(filepath.Join(root, ".session-reviewer"), 0o755); err != nil {
		t.Fatal(err)
	}
	review, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contracts", "v4", "markdown", "review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "项目回顾.md"), review, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".session-reviewer", "ledger.json"), []byte("{malformed-v4-ledger"), 0o600); err != nil {
		t.Fatal(err)
	}
	format, err := DetectFormat(t.Context(), Options{ProjectID: fixture.projectID, CWD: fixture.project, DataDir: fixture.data, GOOS: runtime.GOOS, Trigger: syncengine.TriggerCLI})
	if err == nil || format != "" {
		t.Fatalf("malformed v4 ledger format=%q err=%v", format, err)
	}
}
