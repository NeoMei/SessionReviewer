package apply

import "io/fs"

func normalizeWindowsApplyMode(mode fs.FileMode) uint32 {
	if mode.Perm()&0o200 == 0 {
		return 0o444
	}
	return 0o666
}
