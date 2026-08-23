//go:build !windows

package apply

import "testing"

func TestNativeApplyModePolicyUsesExactUnixPermissions(t *testing.T) {
	if got := normalizeApplyMode(0o644); got != 0o644 {
		t.Fatalf("normalized mode=%#o", got)
	}
	if applyModeEqual(0o600, 0o644) {
		t.Fatal("Unix mode comparison accepted permission drift")
	}
}
