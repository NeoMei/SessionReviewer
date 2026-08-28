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

func startPlatformProcess(command *exec.Cmd, startCheck func() error) (platformProcess, string, error) {
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
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED}
	if err := command.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		_ = terminateAndWaitStartedCommand(command, job, 0)
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		_ = terminateAndWaitStartedCommand(command, job, processHandle)
		_ = windows.CloseHandle(processHandle)
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	process := platformProcess{pid: command.Process.Pid, job: job, process: processHandle}
	token, err := windowsProcessStartToken(processHandle)
	if err != nil {
		_ = terminateAndWaitStartedCommand(command, job, processHandle)
		_ = windows.CloseHandle(processHandle)
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	if startCheck != nil {
		if err := startCheck(); err != nil {
			_ = terminateAndWaitStartedCommand(command, job, processHandle)
			_ = windows.CloseHandle(processHandle)
			_ = windows.CloseHandle(job)
			return platformProcess{}, "", err
		}
	}
	if err := resumeSuspendedProcessThreads(uint32(process.pid)); err != nil {
		_ = terminateAndWaitStartedCommand(command, job, processHandle)
		_ = windows.CloseHandle(processHandle)
		_ = windows.CloseHandle(job)
		return platformProcess{}, "", err
	}
	return process, token, nil
}

func terminateAndWaitStartedCommand(command *exec.Cmd, job, process windows.Handle) error {
	var result error
	if process != 0 {
		result = errors.Join(result, windows.TerminateProcess(process, 1))
	} else if command.Process != nil {
		result = errors.Join(result, command.Process.Kill())
	}
	if job != 0 {
		result = errors.Join(result, windows.TerminateJobObject(job, 1))
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		return errors.Join(result, err)
	case <-time.After(processExitWait):
		return errors.Join(result, errProcessExitTimeout)
	}
}

func resumeSuspendedProcessThreads(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	resumed := 0
	for {
		if entry.OwnerProcessID == pid {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				return openErr
			}
			previous, resumeErr := windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if resumeErr != nil || previous != 1 || closeErr != nil {
				return errors.Join(resumeErr, closeErr, errors.New("unexpected suspended thread state"))
			}
			resumed++
		}
		nextErr := windows.Thread32Next(snapshot, &entry)
		if errors.Is(nextErr, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
	}
	if resumed == 0 {
		return errors.New("suspended process had no resumable thread")
	}
	return nil
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
