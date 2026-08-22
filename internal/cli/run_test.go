package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRequiresCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run(nil, &out, &errOut)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage: session-reviewer") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"version"}, &out, &errOut)
	if code != 0 || strings.TrimSpace(out.String()) != "dev" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Run([]string{"unknown"}, &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), `unknown command "unknown"`) {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}
