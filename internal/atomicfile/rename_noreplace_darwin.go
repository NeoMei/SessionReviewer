//go:build darwin

package atomicfile

import (
	"golang.org/x/sys/unix"
	"os"
)

func renameRootNoReplace(oldParent *os.Root, oldName string, newParent *os.Root, newName string) error {
	oldDir, err := oldParent.Open(".")
	if err != nil {
		return err
	}
	defer oldDir.Close()
	newDir, err := newParent.Open(".")
	if err != nil {
		return err
	}
	defer newDir.Close()
	return unix.RenameatxNp(int(oldDir.Fd()), oldName, int(newDir.Fd()), newName, unix.RENAME_EXCL)
}
