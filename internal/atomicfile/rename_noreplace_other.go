//go:build !darwin && !linux && !windows

package atomicfile

import (
	"errors"
	"os"
)

func renameRootNoReplace(*os.Root, string, *os.Root, string) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
