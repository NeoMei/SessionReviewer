//go:build !windows
// +build !windows

package scanjob

import (
	"os"
	"os/exec"
	"syscall"
)

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
		Setsid: true,
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
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
