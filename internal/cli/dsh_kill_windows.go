//go:build windows

package cli

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createSuspended = 0x00000004
const processSuspendResume = 0x0800

func startDSHProcess(cmd *exec.Cmd) (func(), error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | createSuspended,
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// The process is suspended, so no child can run before it is assigned to
	// the kill-on-close job and resumed. This closes the Start/Assign race.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION|processSuspendResume, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	if err = windows.AssignProcessToJobObject(job, proc); err != nil {
		windows.CloseHandle(proc)
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	ntdll := windows.NewLazySystemDLL("ntdll.dll")
	resume := ntdll.NewProc("NtResumeProcess")
	status, _, callErr := resume.Call(uintptr(proc))
	windows.CloseHandle(proc)
	if status != 0 {
		windows.CloseHandle(job)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if callErr != nil {
			return nil, callErr
		}
		return nil, fmt.Errorf("NtResumeProcess status %#x", status)
	}
	return func() {
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
	}, nil
}
