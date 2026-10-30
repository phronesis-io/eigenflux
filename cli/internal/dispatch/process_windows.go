//go:build windows

package dispatch

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

type processGuard struct{ job windows.Handle }

func validateCommandLine(cmd *exec.Cmd) error {
	args := make([]string, len(cmd.Args))
	for i, arg := range cmd.Args {
		args[i] = syscall.EscapeArg(arg)
	}
	// CreateProcessW allows 32767 UTF-16 units including the terminating NUL.
	if len(utf16.Encode([]rune(strings.Join(args, " ")))) >= 32767 {
		return fmt.Errorf("%w: %w; use ACP or a command entry with stdin", ErrNeedsUser, ErrCommandLineTooLong)
	}
	return nil
}

func startManaged(cmd *exec.Cmd) (*processGuard, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err == nil {
		defer windows.CloseHandle(process)
		err = windows.AssignProcessToJobObject(job, process)
	}
	if err == nil {
		status, _, _ := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess").Call(uintptr(process))
		if status != 0 {
			err = errors.New("resume_process_failed")
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		windows.CloseHandle(job)
		return nil, err
	}
	return &processGuard{job: job}, nil
}
func (g *processGuard) kill()  { _ = windows.TerminateJobObject(g.job, 1) }
func (g *processGuard) close() { _ = windows.CloseHandle(g.job) }
