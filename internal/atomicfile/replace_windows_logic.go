package atomicfile

import (
	"errors"
	"fmt"
	"os"
)

type windowsFileOps struct {
	destinationExists func(destination string) (bool, error)
	rename            func(temporary, destination string) error
	link              func(temporary, destination string) error
	remove            func(path string) error
}

func replaceWindowsFile(temporary, destination string, ops windowsFileOps) error {
	exists, err := ops.destinationExists(destination)
	if err != nil {
		return fmt.Errorf("inspect destination: %w", err)
	}
	if !exists {
		// A hard link publishes the fully written temporary file without
		// replacing a creator that races the preceding existence check. If
		// the filesystem cannot create hard links, fail safely instead of
		// falling back to a clobbering rename.
		if err := ops.link(temporary, destination); err != nil {
			return fmt.Errorf("install absent destination: %w", err)
		}
		if err := ops.remove(temporary); err != nil {
			return fmt.Errorf("remove linked temporary: %w", err)
		}
		return nil
	}
	if err := ops.rename(temporary, destination); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	return nil
}

func windowsRootFileOps(root *os.Root) windowsFileOps {
	return windowsFileOps{
		destinationExists: func(destination string) (bool, error) {
			_, err := root.Lstat(destination)
			if err == nil {
				return true, nil
			}
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		},
		rename: root.Rename,
		link:   root.Link,
		remove: root.Remove,
	}
}
