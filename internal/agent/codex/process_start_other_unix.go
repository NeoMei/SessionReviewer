//go:build !windows && !darwin && !linux

package codex

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func readProcessStartToken(pid int) (string, error) {
	output, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", syscall.ESRCH
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("empty process start token")
	}
	return token, nil
}

func processGroupAlive(pgid int) bool {
	return unixProcessGroupAliveBySignal(pgid)
}
