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
	// Use the same handle-relative operation as WriteRoot so path replacement
	// cannot redirect the rename after the directory has been opened.
	return replaceWindowsFile(filepath.Base(temporary), filepath.Base(destination), windowsFileOps{
		rename: root.Rename,
	})
}

func replaceRootFile(root *os.Root, temporary, destination string) error {
	// On Windows, os.Root.Rename uses directory handles and
	// FileRenameInformationEx with replacement semantics.
	return replaceWindowsFile(temporary, destination, windowsFileOps{
		rename: root.Rename,
	})
}
