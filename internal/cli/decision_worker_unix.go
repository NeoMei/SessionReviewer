//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/neomei/SessionReviewer/internal/decisions"
)

func launchDecisionExtractionWorker(job decisions.ExtractionJob, dataRoot string) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	command := exec.Command(executable, "decisions", "worker", "--job-id", job.JobID, "--project-id", job.ProjectID, "--data-dir", dataRoot, "--json")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	go func() { _ = command.Wait() }()
	return command.Process.Pid, nil
}
