//go:build windows
// +build windows

package scanjob

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func defaultWorkerRunner(jobID, dataRoot, projectID, sessionsRoot string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, "scan", "worker", "--job-id", jobID, "--data-dir", dataRoot, "--project-id", projectID)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	return windows.GetExitCodeProcess(handle, &exitCode) == nil && exitCode == windowsStillActive
}
