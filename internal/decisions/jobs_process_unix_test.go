//go:build !windows

package decisions

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestTerminateExtractionProcessKillsDetachedProcessGroup(t *testing.T) {
	command := exec.Command("sh", "-c", `trap '' TERM; (trap '' TERM; sleep 60) & wait`)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL); _, _ = command.Process.Wait() })
	if err := terminateExtractionProcess(command.Process); err != nil {
		t.Fatal(err)
	}
	_, _ = command.Process.Wait()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !extractionProcessGroupAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached extraction process group remained alive after cancellation")
}
