package cli

import (
	"bytes"
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
