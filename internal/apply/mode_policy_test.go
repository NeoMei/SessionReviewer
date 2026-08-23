package apply

import (
	"io/fs"
	"testing"
)

func TestWindowsReceiptModePolicyUsesRepresentableWritableBit(t *testing.T) {
	for _, tc := range []struct {
		input fs.FileMode
		want  uint32
	}{
		{input: 0o644, want: 0o666},
		{input: 0o600, want: 0o666},
		{input: 0o666, want: 0o666},
		{input: 0o444, want: 0o444},
		{input: 0o400, want: 0o444},
	} {
		if got := normalizeWindowsApplyMode(tc.input); got != tc.want {
			t.Fatalf("normalizeWindowsApplyMode(%#o)=%#o want=%#o", tc.input, got, tc.want)
		}
	}
}
