//go:build windows

package apply

import "io/fs"

func normalizeApplyMode(mode fs.FileMode) uint32 {
	return normalizeWindowsApplyMode(mode)
}

func applyModeEqual(actual fs.FileMode, recorded uint32) bool {
	return normalizeApplyMode(actual) == normalizeWindowsApplyMode(fs.FileMode(recorded))
}

func validApplyMode(recorded uint32) bool {
	return recorded != 0 && normalizeWindowsApplyMode(fs.FileMode(recorded)) == recorded
}

func receiptPrivacyModeEqual(actual, desired fs.FileMode) bool {
	return normalizeWindowsApplyMode(actual) == normalizeWindowsApplyMode(desired)
}
