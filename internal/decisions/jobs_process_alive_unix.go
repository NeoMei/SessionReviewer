//go:build !windows && !darwin

package decisions

import (
	"errors"
	"syscall"
)

func extractionProcessGroupAlive(pgid int) bool {
	return !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}
