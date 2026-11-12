//go:build windows

package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobOnce sync.Once
	job     windows.Handle
)

// attachToJob puts the child in a job object with KILL_ON_JOB_CLOSE, so children die with passage-server even if it is killed.
func attachToJob(cmd *exec.Cmd) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			slog.Warn("job object unavailable", "err", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			slog.Warn("job object limit not set", "err", err)
			_ = windows.CloseHandle(h)
			return
		}
		job = h
	})
	if job == 0 || cmd.Process == nil {
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		slog.Warn("open child for job failed", "err", err)
		return
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		slog.Warn("assign child to job failed", "err", err)
	}
}

// setProcAttr starts the child in its own process group, so a Ctrl+C aimed at a console running passage-server does not hit it twice.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
}

// interrupt reports that a graceful signal cannot be delivered; the caller kills the child.
func interrupt(*os.Process) error { return errors.New("no graceful stop signal on windows") }
