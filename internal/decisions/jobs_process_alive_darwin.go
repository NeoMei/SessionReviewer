//go:build darwin

package decisions

import "golang.org/x/sys/unix"

func extractionProcessGroupAlive(pgid int) bool {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return true
	}
	const zombieStatus = 5
	for _, process := range processes {
		if int(process.Eproc.Pgid) == pgid && process.Proc.P_stat != zombieStatus {
			return true
		}
	}
	return false
}
