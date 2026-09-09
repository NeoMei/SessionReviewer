//go:build !windows

package decisions

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func terminateExtractionProcess(process *os.Process) error {
	pid := process.Pid
	if pid <= 0 {
		return errors.New("invalid extraction process")
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !extractionProcessGroupAlive(pid) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}
