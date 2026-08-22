//go:build !windows

package atomicfile

import "os"

func replaceFile(temporary, destination string) error {
	return os.Rename(temporary, destination)
}
