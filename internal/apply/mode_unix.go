//go:build !windows

package apply

import "io/fs"

func normalizeApplyMode(mode fs.FileMode) uint32 {
	return uint32(mode.Perm())
}

func applyModeEqual(actual fs.FileMode, recorded uint32) bool {
	return normalizeApplyMode(actual) == recorded
}

func validApplyMode(recorded uint32) bool {
	return recorded != 0 && uint32(fs.FileMode(recorded).Perm()) == recorded
}

func receiptPrivacyModeEqual(actual, desired fs.FileMode) bool {
	return actual.Perm() == desired.Perm()
}
