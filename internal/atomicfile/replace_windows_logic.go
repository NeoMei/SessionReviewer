package atomicfile

import (
	"fmt"
)

type windowsFileOps struct {
	rename func(temporary, destination string) error
}

func replaceWindowsFile(temporary, destination string, ops windowsFileOps) error {
	if err := ops.rename(temporary, destination); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	return nil
}
