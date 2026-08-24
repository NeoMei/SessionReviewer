//go:build windows

package atomicfile

import "testing"

func TestSafeAtomicWriterModeAcceptsNativeWindowsWritableMode(t *testing.T) {
	if !safeAtomicWriterMode(0o666) {
		t.Fatal("normal writable Windows file mode was rejected")
	}
	if safeAtomicWriterMode(0o444) {
		t.Fatal("read-only Windows file mode was accepted")
	}
}
