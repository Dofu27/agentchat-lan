//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Per-call dependencies permit fault injection without process-wide mutable hooks.
type jobAPI struct {
	create    func() (windows.Handle, error)
	configure func(windows.Handle) error
	open      func(uint32) (windows.Handle, error)
	assign    func(windows.Handle, windows.Handle) error
	resume    func(uint32) error
	terminate func(windows.Handle) error
	close     func(windows.Handle) error
}

func nativeJobAPI() jobAPI {
	return jobAPI{
		create: func() (windows.Handle, error) { return windows.CreateJobObject(nil, nil) },
		configure: func(h windows.Handle) error {
			info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
			info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
			_, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
			return err
		},
		open: func(pid uint32) (windows.Handle, error) {
			return windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION, false, pid)
		},
		assign:    windows.AssignProcessToJobObject,
		resume:    resumeSuspendedProcess,
		terminate: func(h windows.Handle) error { return windows.TerminateJobObject(h, 1) },
		close:     windows.CloseHandle,
	}
}

func resumeSuspendedProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	entry := windows.ThreadEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		previous, err := windows.ResumeThread(h)
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
		if previous != 1 {
			return errors.New("unexpected primary thread suspension count")
		}
		return nil
	}
	return fmt.Errorf("cannot locate suspended primary thread: %w", err)
}

func runBoundedCommand(ctx context.Context, name string, args []string, limit int) (string, error) {
	return runBoundedWithJob(ctx, name, args, limit, nativeJobAPI())
}

func runBoundedWithJob(ctx context.Context, name string, args []string, limit int, api jobAPI) (string, error) {
	job, err := api.create()
	if err != nil {
		return "", fmt.Errorf("create process job: %w", err)
	}
	if job == 0 {
		return "", errors.New("create process job returned no handle")
	}
	defer api.close(job) // Every return, including natural parent exit, reaps descendants.
	if err := api.configure(job); err != nil {
		return "", fmt.Errorf("configure process job: %w", err)
	}
	out := &cappedBuffer{limit: limit}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = 2 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	// Set before Start: watchCtx must never race a subsequent Cancel assignment.
	cmd.Cancel = func() error {
		jobErr := api.terminate(job)
		// Covers cancellation between Start and Assign while the child is suspended.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return jobErr
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	fail := func(stage string, err error) (string, error) {
		_ = cmd.Process.Kill()
		_ = api.terminate(job)
		_ = cmd.Wait()
		return redactOutput(out.String()), fmt.Errorf("%s: %w", stage, err)
	}
	proc, err := api.open(uint32(cmd.Process.Pid))
	if err != nil {
		return fail("open suspended process", err)
	}
	err = api.assign(job, proc)
	_ = api.close(proc)
	if err != nil {
		return fail("assign process job", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("process cancelled before resume", err)
	}
	if err := api.resume(uint32(cmd.Process.Pid)); err != nil {
		return fail("resume contained process", err)
	}
	err = cmd.Wait()
	return redactOutput(out.String()), err
}
