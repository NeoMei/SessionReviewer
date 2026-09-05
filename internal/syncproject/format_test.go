package syncproject

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	syncengine "github.com/neomei/SessionReviewer/internal/sync"
)

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
