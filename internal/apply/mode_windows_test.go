//go:build windows

package apply

import "testing"

func TestNativeApplyModePolicyUsesWindowsWritableApproximation(t *testing.T) {
	if got := normalizeApplyMode(0o644); got != 0o666 {
		t.Fatalf("normalized mode=%#o", got)
	}
	if !applyModeEqual(0o666, 0o644) {
		t.Fatal("Windows mode comparison rejected an equivalent writable file")
	}
	if applyModeEqual(0o444, 0o644) {
		t.Fatal("Windows mode comparison accepted read-only drift")
	}
}
