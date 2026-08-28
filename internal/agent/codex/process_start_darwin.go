//go:build darwin

package codex

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func readProcessStartToken(pid int) (string, error) {
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.EIO) {
			return "", syscall.ESRCH
		}
		return "", err
	}
	if process == nil || int(process.Proc.P_pid) != pid {
		return "", syscall.ESRCH
	}
	started := process.Proc.P_starttime
	return fmt.Sprintf("%d:%d", started.Sec, started.Usec), nil
}
