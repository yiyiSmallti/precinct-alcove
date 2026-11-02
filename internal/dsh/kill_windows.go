package dsh

import (
	"os/exec"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// OwnProcessGroup is a no-op on Windows; Contain puts the started process
// in a job object instead.
func OwnProcessGroup(cmd *exec.Cmd) {}

var jobs sync.Map // *exec.Cmd -> windows.Handle

// Contain puts a started cmd in its own job object, so KillTree ends its
// descendants even after cmd itself has exited (taskkill /T can only walk
// the tree from a live parent). The job is set to kill on close, so the
// tree also goes if Alcove dies. Children spawned before Contain runs, in
// the moment after Start, are not in the job; taskkill still covers them
// while cmd lives.
func Contain(cmd *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		windows.CloseHandle(job)
		return
	}
	jobs.Store(cmd, job)
}

// KillTree kills a started cmd and its descendants: its job object if
// Contain made one, else taskkill /T. Safe to call again, and after cmd
// has exited.
func KillTree(cmd *exec.Cmd) error {
	if j, ok := jobs.LoadAndDelete(cmd); ok {
		job := j.(windows.Handle)
		err := windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		if err == nil {
			return nil
		}
	}
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
