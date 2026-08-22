//go:build windows

package atomicfile

import "os"

func replaceFile(temporary, destination string) error {
	return replaceWindowsFile(temporary, destination, windowsFileOps{
		stat:   os.Stat,
		rename: os.Rename,
		remove: os.Remove,
	})
}
