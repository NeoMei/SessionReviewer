//go:build windows

package decisions

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/windows"
)

func terminateExtractionProcess(process *os.Process) error {
	if process.Pid <= 0 {
		return errors.New("invalid extraction process")
	}
	// Detached extraction workers may have a Codex descendant. taskkill /T is
	// the Windows process-tree primitive available to this CLI surface.
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	return exec.Command(filepath.Join(systemDirectory, "taskkill.exe"), "/PID", strconv.Itoa(process.Pid), "/T", "/F").Run()
}
