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

// processGroupAlive asks the Darwin process table rather than relying on
// killpg(..., 0).  The latter reports EPERM for a group containing only a
// zombie in some races, even though zombies cannot execute or retain pipes.
func processGroupAlive(pgid int) bool {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		// A failed authoritative inspection must fail closed.
		return true
	}
	const zombieStatus = 5 // SZOMB from <sys/proc.h>.
	for _, process := range processes {
		if int(process.Eproc.Pgid) == pgid && process.Proc.P_stat != zombieStatus {
			return true
		}
	}
	return false
}
