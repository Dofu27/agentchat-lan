//go:build windows

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
)

func (a *App) serviceRun(args []string) int {
	fs := a.newFlagSet("service run")
	name := fs.String("name", defaultServiceName, "Windows service name")
	logPath := fs.String("log", "", "optional service log file")
	if codeExit, ok := a.parse(fs, args); !ok {
		return codeExit
	}
	if fs.NArg() == 0 {
		return a.fail(fmt.Errorf("service run requires a child command"))
	}
	if err := runWindowsService(*name, *logPath, fs.Args()); err != nil {
		return a.fail(err)
	}
	return 0
}

type childService struct {
	exe     string
	args    []string
	logPath string
}

func runWindowsService(name, logPath string, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if logPath != "" {
		logPath = filepath.Clean(logPath)
	}
	return svc.Run(name, &childService{exe: exe, args: args, logPath: logPath})
}

func (s *childService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	var logFile *os.File
	if s.logPath != "" {
		if f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			logFile = f
			defer logFile.Close()
			fmt.Fprintf(logFile, "service start: %s\n", time.Now().UTC().Format(time.RFC3339))
		}
	}
	cmd := exec.Command(s.exe, s.args...)
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		return false, 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				_ = cmd.Process.Kill()
				<-done
				return false, 0
			}
		case <-done:
			return false, 0
		}
	}
}
