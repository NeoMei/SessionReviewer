//go:build windows

package codex

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type platformProcess struct {
	pid     int
	job     windows.Handle
	process windows.Handle
}

func executableAllowed(path string, _ os.FileInfo) bool {
	return strings.EqualFold(filepath.Ext(path), ".exe")
}

func directoryPrivate(_ os.FileInfo) bool { return true }

func startPlatformProcess(command *exec.Cmd) (platformProcess, string, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return platformProcess{}, "", err
	}
	limit := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limit.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limit)),
		uint32(unsafe.Sizeof(limit)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := command.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = windows.CloseHandle(processHandle)
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	process := platformProcess{pid: command.Process.Pid, job: job, process: processHandle}
	token, err := windowsProcessStartToken(processHandle)
	if err != nil {
		_ = windows.CloseHandle(job)
		_ = command.Wait()
		_ = windows.CloseHandle(processHandle)
		return platformProcess{}, "", err
	}
	return process, token, nil
}

func terminatePlatformProcess(process *platformProcess, expectedToken string, _ time.Duration) error {
	if process == nil || process.job == 0 {
		return nil
	}
	current, err := windowsProcessStartToken(process.process)
	if err != nil {
		return err
	}
	if expectedToken == "" || current != expectedToken {
		return errProcessIdentityChanged
	}
	if err := windows.CloseHandle(process.job); err != nil {
		return err
	}
	process.job = 0
	return nil
}

func releasePlatformProcess(process *platformProcess) error {
	if process == nil {
		return nil
	}
	var result error
	if process.job != 0 {
		result = errors.Join(result, windows.CloseHandle(process.job))
		process.job = 0
	}
	if process.process != 0 {
		result = errors.Join(result, windows.CloseHandle(process.process))
		process.process = 0
	}
	return result
}

func windowsProcessStartToken(handle windows.Handle) (string, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	value := creation.Nanoseconds()
	if value <= 0 {
		return "", errors.New("invalid Windows process start token")
	}
	return fmt.Sprintf("%d", value), nil
}
