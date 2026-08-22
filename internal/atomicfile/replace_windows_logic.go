package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type windowsFileOps struct {
	stat   func(string) (fs.FileInfo, error)
	rename func(string, string) error
	remove func(string) error
}

func replaceWindowsFile(temporary, destination string, ops windowsFileOps) error {
	backup := BackupPath(destination)
	destinationExists, err := windowsPathExists(destination, ops.stat)
	if err != nil {
		return fmt.Errorf("inspect replacement destination: %w", err)
	}
	backupExists, err := windowsPathExists(backup, ops.stat)
	if err != nil {
		return fmt.Errorf("inspect replacement backup: %w", err)
	}

	if !destinationExists && backupExists {
		if err := ops.rename(backup, destination); err != nil {
			return fmt.Errorf("recover interrupted replacement: %w", err)
		}
		destinationExists = true
		backupExists = false
	}
	if destinationExists && backupExists {
		if err := ops.remove(backup); err != nil {
			return fmt.Errorf("remove stale replacement backup: %w", err)
		}
	}

	if !destinationExists {
		if err := ops.rename(temporary, destination); err != nil {
			return fmt.Errorf("install replacement: %w", err)
		}
		return nil
	}
	if err := ops.rename(destination, backup); err != nil {
		return fmt.Errorf("preserve replacement destination: %w", err)
	}
	if installErr := ops.rename(temporary, destination); installErr != nil {
		if rollbackErr := ops.rename(backup, destination); rollbackErr != nil {
			return errors.Join(
				fmt.Errorf("install replacement: %w", installErr),
				fmt.Errorf("restore replacement backup: %w", rollbackErr),
			)
		}
		return fmt.Errorf("install replacement: %w", installErr)
	}
	if err := ops.remove(backup); err != nil {
		return fmt.Errorf("remove replacement backup: %w", err)
	}
	return nil
}

func windowsPathExists(name string, stat func(string) (fs.FileInfo, error)) (bool, error) {
	_, err := stat(name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
