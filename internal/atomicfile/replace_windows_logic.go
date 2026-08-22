package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

type windowsFileOps struct {
	stat            func(string) (fs.FileInfo, error)
	replaceExisting func(destination, temporary string) error
	moveNew         func(temporary, destination string) error
}

func replaceWindowsFile(temporary, destination string, ops windowsFileOps) error {
	_, err := ops.stat(destination)
	switch {
	case err == nil:
		if err := ops.replaceExisting(destination, temporary); err != nil {
			return fmt.Errorf("replace existing destination: %w", err)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := ops.moveNew(temporary, destination); err != nil {
			return fmt.Errorf("install new destination: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("inspect replacement destination: %w", err)
	}
}
