package contextupdate

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/neomei/SessionReviewer/internal/config"
	"github.com/neomei/SessionReviewer/internal/projectidentity"
	"github.com/neomei/SessionReviewer/internal/redact"
	"github.com/neomei/SessionReviewer/internal/source"
	"github.com/neomei/SessionReviewer/internal/sourcecatalog"
)

func TestProductionSourceAdaptersKeepClaudeAvailableWhenCodexIsMissing(t *testing.T) {
	projectRoot := t.TempDir()
	binding, err := projectidentity.Resolve(config.ProjectMapping{ID: "project-p", Root: projectRoot}, projectRoot, runtime.GOOS)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := sourcecatalog.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	r := redact.Default()
	missing := t.TempDir()
	claudeRoot := t.TempDir()
	adapters, err := productionSourceAdapters(
		filepath.Join(missing, "codex"), claudeRoot,
		[]projectidentity.Binding{binding}, catalog, &r,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 2 || adapters[0].Required || adapters[1].Required {
		t.Fatalf("production adapters=%+v, want two independently optional providers", adapters)
	}
	_, diagnostics, err := source.DiscoverAll(context.Background(), adapters)
	if err != nil {
		t.Fatalf("missing providers blocked discovery: %v", err)
	}
	want := []source.ProviderDiagnostic{{Provider: "codex", Code: "provider_unavailable"}}
	if !reflect.DeepEqual(diagnostics, want) {
		t.Fatalf("provider diagnostics=%+v want=%+v", diagnostics, want)
	}
}

func TestResolveClaudeSessionsRootRejectsInvalidOverride(t *testing.T) {
	t.Setenv("SESSION_REVIEWER_CLAUDE_SESSIONS_ROOT", "relative/root")
	if _, err := resolveClaudeSessionsRoot(""); err == nil || !strings.Contains(err.Error(), "Claude sessions root") {
		t.Fatalf("invalid Claude sessions root override error=%v", err)
	}
}
