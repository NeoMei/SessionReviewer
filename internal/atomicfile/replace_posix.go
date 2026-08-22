//go:build !windows

package atomicfile

import "os"

func replaceFile(temporary, destination string) error {
	return os.Rename(temporary, destination)
}

func replaceRootFile(root *os.Root, temporary, destination string) error {
	return root.Rename(temporary, destination)
}
