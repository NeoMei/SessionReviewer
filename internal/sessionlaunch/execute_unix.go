//go:build !windows

package sessionlaunch

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

func Execute(plan ExecPlan) error {
	if !cleanAbsolute(plan.Executable) || !cleanAbsolute(plan.WorkingDirectory) || len(plan.Arguments) == 0 || !plan.ProjectIdentity.Valid() || !plan.ExecutableIdentity.Valid() || len(plan.ExecutableSHA256) != 64 {
		return errors.New("native Session execution plan is invalid")
	}
	if !isInteractiveTerminal() {
		return errors.New("native Session launch requires an interactive terminal")
	}
	directory, err := os.Open(plan.WorkingDirectory)
	if err != nil {
		return err
	}
	defer directory.Close()
	identity, err := pathguardPhysical(directory)
	if err != nil || identity != plan.ProjectIdentity {
		return errors.New("Project root changed before native Session launch")
	}
	if err := syscall.Fchdir(int(directory.Fd())); err != nil {
		return err
	}
	executable, err := os.Open(plan.Executable)
	if err != nil {
		return err
	}
	defer executable.Close()
	current, err := pathguardPhysical(executable)
	if err != nil || current != plan.ExecutableIdentity {
		return errors.New("provider executable changed before native Session launch")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, executable); err != nil || fmt.Sprintf("%x", hash.Sum(nil)) != plan.ExecutableSHA256 {
		return errors.New("provider executable changed before native Session launch")
	}
	if _, err := executable.Seek(0, io.SeekStart); err != nil {
		return err
	}
	argv := append([]string{plan.Executable}, plan.Arguments...)
	return syscall.Exec(plan.Executable, argv, sanitizedEnvironment(os.Environ()))
}

func sanitizedEnvironment(values []string) []string {
	blocked := map[string]bool{"CODEX_THREAD_ID": true, "CODEX_SESSION_ID": true, "SESSION_REVIEWER_SESSIONS_ROOT": true}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			result = append(result, value)
		}
	}
	return result
}
