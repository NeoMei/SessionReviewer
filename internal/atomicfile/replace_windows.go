//go:build windows

package atomicfile

import (
	"errors"
	"os"
)

func replaceFile(temporary, destination string) error {
	backup := destination + ".session-reviewer-backup"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.Rename(temporary, destination)
		}
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Rename(backup, destination)
		return err
	}
	return os.Remove(backup)
}
