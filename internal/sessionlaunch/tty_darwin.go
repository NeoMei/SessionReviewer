//go:build darwin

package sessionlaunch

import (
	"os"

	"golang.org/x/sys/unix"
)

func isInteractiveTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	return err == nil
}
