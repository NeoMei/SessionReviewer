//go:build !windows

package decisions

import (
	"os"
	"syscall"
)

func terminateExtractionProcess(process *os.Process) error { return process.Signal(syscall.SIGTERM) }
