package atomicfile

import "testing"

func TestRootDirectoryLockNamesUsePortableComponentIdentity(t *testing.T) {
	tests := []struct {
		name      string
		canonical bool
		lockLike  bool
	}{
		{name: ".session-reviewer-directory.lock", canonical: true, lockLike: true},
		{name: ".SESSION-REVIEWER-DIRECTORY.LOCK", lockLike: true},
		{name: ".session-reviewer-directory.lock.extra", lockLike: true},
		{name: ".SESSION-REVIEWER-DIRECTORY.LOCK.EXTRA", lockLike: true},
		{name: ".SESSION-REVIEWER-DIRECTORY.LOCK.e\u0301xtra", lockLike: true},
		{name: ".session-reviewer-directory.other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRootDirectoryLockName(test.name); got != test.canonical {
				t.Fatalf("canonical=%v want=%v", got, test.canonical)
			}
			if got := IsRootDirectoryLockLikeName(test.name); got != test.lockLike {
				t.Fatalf("lockLike=%v want=%v", got, test.lockLike)
			}
		})
	}
	composed, ok := rootDirectoryPortableComponentKey(".SESSION-REVIEWER-DIRECTORY.LOCK.\u00e9xtra")
	if !ok {
		t.Fatal("composed portable key rejected")
	}
	decomposed, ok := rootDirectoryPortableComponentKey(".session-reviewer-directory.lock.e\u0301xtra")
	if !ok || decomposed != composed {
		t.Fatalf("decomposed key=%q ok=%v want=%q", decomposed, ok, composed)
	}
}
