package contextupdate

import (
	"context"
	"os"
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
	t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "")
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
	if len(adapters) != 3 || adapters[0].Required || adapters[1].Required || adapters[2].Required {
		t.Fatalf("production adapters=%+v, want three independently optional providers", adapters)
	}
	_, diagnostics, err := source.DiscoverAll(context.Background(), adapters)
	if err != nil {
		t.Fatalf("missing providers blocked discovery: %v", err)
	}
	want := []source.ProviderDiagnostic{{Provider: "codex", Code: "provider_unavailable"}, {Provider: "opencode", Code: "provider_unavailable"}}
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

func TestProductionOpenCodeRegistrationIsExplicitAndFailsClosed(t *testing.T) {
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
	codexRoot, claudeRoot := t.TempDir(), t.TempDir()
	t.Run("unconfigured ignores default database", func(t *testing.T) {
		t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "")
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		defaultPath := filepath.Join(xdg, "opencode", "opencode.db")
		if err := os.MkdirAll(filepath.Dir(defaultPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(defaultPath, []byte("not a SQLite database"), 0600); err != nil {
			t.Fatal(err)
		}
		adapters, err := productionSourceAdapters(codexRoot, claudeRoot, []projectidentity.Binding{binding}, catalog, &r)
		if err != nil {
			t.Fatal(err)
		}
		discovery, diagnostics, err := source.DiscoverAll(context.Background(), adapters)
		if err != nil || len(discovery.Candidates) != 0 || !reflect.DeepEqual(diagnostics, []source.ProviderDiagnostic{{Provider: "opencode", Code: "provider_unavailable"}}) {
			t.Fatalf("unconfigured provider probed default source: discovery=%+v diagnostics=%+v err=%v", discovery, diagnostics, err)
		}
	})
	t.Run("relative configuration rejected", func(t *testing.T) {
		t.Setenv("SESSION_REVIEWER_OPENCODE_DB", "relative/opencode.db")
		if _, err := productionSourceAdapters(codexRoot, claudeRoot, []projectidentity.Binding{binding}, catalog, &r); err == nil {
			t.Fatal("invalid explicit OpenCode path accepted")
		}
	})
	t.Run("missing explicit source required", func(t *testing.T) {
		t.Setenv("SESSION_REVIEWER_OPENCODE_DB", filepath.Join(t.TempDir(), "missing.db"))
		adapters, err := productionSourceAdapters(codexRoot, claudeRoot, []projectidentity.Binding{binding}, catalog, &r)
		if err != nil {
			t.Fatal(err)
		}
		if len(adapters) != 3 || adapters[2].Provider != "opencode" || !adapters[2].Required {
			t.Fatalf("explicit OpenCode source not required: %+v", adapters)
		}
		if _, _, err := source.DiscoverAll(context.Background(), adapters); err == nil {
			t.Fatal("missing required OpenCode database silently ignored")
		}
	})
}
