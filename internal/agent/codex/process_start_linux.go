//go:build linux

package codex

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

func readProcessStartToken(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + itoa(pid) + "/stat")
	if err != nil {
		if os.IsNotExist(err) {
			return "", syscall.ESRCH
		}
		return "", err
	}
	closing := strings.LastIndexByte(string(data), ')')
	if closing < 0 || closing+2 >= len(data) {
		return "", errors.New("malformed process stat")
	}
	fields := strings.Fields(string(data[closing+2:]))
	// The suffix starts with field 3 (state); starttime is field 22.
	if len(fields) <= 19 || fields[19] == "" {
		return "", errors.New("malformed process start token")
	}
	return fields[19], nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [32]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

func processGroupAlive(pgid int) bool {
	return unixProcessGroupAliveBySignal(pgid)
}
