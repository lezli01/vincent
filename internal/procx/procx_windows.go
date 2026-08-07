//go:build windows

package procx

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setSysProcAttr needs no flags on Windows; tree containment comes from the
// Job object assigned in attach.
func setSysProcAttr(*exec.Cmd) {}

// signalProcess has no graceful equivalent on Windows for a console-less
// child, so the fallback path kills outright.
func signalProcess(cmd *exec.Cmd) error { return cmd.Process.Kill() }

// attach creates a Job object with kill-on-close and assigns the child to
// it. Every process the child spawns joins the job, so TerminateJobObject
// reaps the whole tree — and so does handle cleanup if the daemon dies.
func attach(cmd *exec.Cmd) (platformProc, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("open process %d: %w", cmd.Process.Pid, err)
	}
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(proc)
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process to job: %w", err)
	}
	_ = windows.CloseHandle(proc)
	return &jobKill{job: job}, nil
}

// jobKill terminates every process in the Job object.
type jobKill struct{ job windows.Handle }

// terminate has no gentler form here: a Job object exposes only
// TerminateJobObject, which is what spec §6's `taskkill /T /F` does too.
// The caller's grace period still elapses; the tree simply gets no warning.
func (j *jobKill) terminate() error { return j.kill() }

func (j *jobKill) kill() error {
	if err := windows.TerminateJobObject(j.job, 1); err != nil {
		return fmt.Errorf("terminate job object: %w", err)
	}
	return nil
}

func (j *jobKill) release() {
	if j.job != 0 {
		_ = windows.CloseHandle(j.job)
		j.job = 0
	}
}
