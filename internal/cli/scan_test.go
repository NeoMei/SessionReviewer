package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanCLIDispatchAndHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	// Root help includes scan
	code := Run([]string{"--help"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "scan") {
		t.Fatalf("root help missing scan: %s", stdout.String())
	}

	// Invalid flags fail with code 2
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"scan", "--unknown-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected code 2 for unknown flag, got %d", code)
	}
}

func TestScanHelpIsAvailableWithoutStartingAScan(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"scan", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("scan help code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "scan start") || !strings.Contains(stdout.String(), "scan status") {
		t.Fatalf("scan help is incomplete: %q", stdout.String())
	}
}

func TestScanCommandsRejectAmbiguousFlags(t *testing.T) {
	dataDir := filepath.Clean(t.TempDir())
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "duplicate public flag",
			args: []string{"scan", "status", "--project-id", "project-1", "--data-dir", dataDir, "--json", "--json"},
		},
		{
			name: "missing public value",
			args: []string{"scan", "start", "--project-id", "project-1", "--data-dir"},
		},
		{
			name: "unknown private worker flag",
			args: []string{"scan", "worker", "--job-id", "scan-1", "--data-dir", dataDir, "--project-id", "project-1", "--unexpected"},
		},
		{
			name: "duplicate private worker flag",
			args: []string{"scan", "worker", "--job-id", "scan-1", "--job-id", "scan-1", "--data-dir", dataDir, "--project-id", "project-1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := Run(test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestScanRejectsUnknownSubcommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"scan", "frobnicate"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "unknown scan command") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
