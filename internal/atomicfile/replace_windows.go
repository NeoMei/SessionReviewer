//go:build windows

package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceFile(temporary, destination string) error {
	temporaryDir := filepath.Clean(filepath.Dir(temporary))
	destinationDir := filepath.Clean(filepath.Dir(destination))
	if temporaryDir != destinationDir {
		return fmt.Errorf("temporary and destination must share a directory")
	}
	root, err := os.OpenRoot(destinationDir)
	if err != nil {
		return fmt.Errorf("open replacement root: %w", err)
	}
	defer root.Close()
	// Use the same handle-relative operations as WriteRoot so namespace
	// replacement cannot redirect installation after the directory is opened.
	return replaceWindowsFile(filepath.Base(temporary), filepath.Base(destination), windowsRootFileOps(root))
}

func replaceRootFile(root *os.Root, temporary, destination string) error {
	return replaceWindowsFile(temporary, destination, windowsRootFileOps(root))
}
