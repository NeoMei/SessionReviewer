//go:build !windows

package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type platformProcess struct {
	pid  int
	pgid int
}

func executableAllowed(_ string, info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}

func startPlatformProcess(command *exec.Cmd, startCheck func() error) (platformProcess, string, error) {
	if startCheck != nil {
		if err := startCheck(); err != nil {
			return platformProcess{}, "", err
		}
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return platformProcess{}, "", err
	}
	process := platformProcess{pid: command.Process.Pid, pgid: command.Process.Pid}
	token, err := readProcessStartToken(process.pid)
	if err != nil && !processMissing(err) {
		_ = syscall.Kill(-process.pgid, syscall.SIGKILL)
		_ = command.Wait()
		return platformProcess{}, "", err
	}
	// An extremely short-lived probe can disappear before its token is read.
	// Empty is safe only while the PID remains absent; any live PID later fails
	// the comparison instead of being signalled.
	return process, token, nil
}

func terminatePlatformProcess(process *platformProcess, expectedToken string, grace time.Duration) error {
	if process == nil || process.pgid <= 0 {
		return nil
	}
	if err := verifyUnixStartToken(process.pid, expectedToken); err != nil {
		return fmt.Errorf("verify process identity before TERM: %w", err)
	}
	if err := syscall.Kill(-process.pgid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("signal process group with TERM: %w", err)
	}
	deadline := time.Now().Add(grace)
	for grace > 0 && time.Now().Before(deadline) {
		if !processGroupAlive(process.pgid) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !processGroupAlive(process.pgid) {
		return nil
	}
	if err := verifyUnixStartToken(process.pid, expectedToken); err != nil {
		return fmt.Errorf("verify process identity before KILL: %w", err)
	}
	if err := syscall.Kill(-process.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		// Darwin can report EPERM after delivering a group signal to every
		// executable member while an already-dead zombie is still awaiting
		// collection.  The platform liveness check distinguishes that safe
		// state from a live member that really could not be signalled.
		if !processGroupAlive(process.pgid) {
			return nil
		}
		return fmt.Errorf("signal process group with KILL: %w", err)
	}
	return nil
}

func releasePlatformProcess(_ *platformProcess) error { return nil }

func verifyUnixStartToken(pid int, expected string) error {
	current, err := readProcessStartToken(pid)
	if err != nil {
		if processMissing(err) {
			// If the leader exited while descendants keep the group alive, that
			// group cannot be reused until it is empty. Signalling the original
			// PGID remains safe; a reused live PID is caught below.
			return nil
		}
		return err
	}
	if expected == "" || current != expected {
		return errProcessIdentityChanged
	}
	return nil
}

func processMissing(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrNotExist)
}

func unixProcessGroupAliveBySignal(pgid int) bool {
	err := syscall.Kill(-pgid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
