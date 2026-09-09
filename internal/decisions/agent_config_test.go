package decisions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neomei/SessionReviewer/internal/pathguard"
)

func TestAgentConfigurationPersistsExactVerifiedIdentityAndRechecksExecutable(t *testing.T) {
	dataRoot, executable := t.TempDir(), filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("verified-agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := fileIdentity(t, executable)
	configured, err := MeasureAgentConfiguration("codex", "fixture", executable)
	if err != nil || configured.Identity != identity {
		t.Fatalf("configuration=%+v err=%v", configured, err)
	}
	if err := SaveAgentConfiguration(dataRoot, configured); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAgentConfiguration(dataRoot)
	if err != nil || got != configured {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if err := os.WriteFile(executable, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentConfiguration(dataRoot); err == nil {
		t.Fatal("replaced configured Agent accepted")
	}
}

func TestAgentConfigurationRequiresPrivateAbsoluteDataRoot(t *testing.T) {
	if err := SaveAgentConfiguration("relative", AgentConfiguration{}); err == nil {
		t.Fatal("relative Data root accepted")
	}
}

func fileIdentity(t *testing.T, path string) pathguard.IdentityToken {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	identity, err := pathguard.PhysicalFileIdentity(file)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
