//go:build windows

package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows/svc"

	"github.com/Dofu27/agentchat-lan/internal/logbroker"
)

func (a *App) serviceRun(args []string) int {
	fs := a.newFlagSet("service run")
	logging := logFlags(fs)
	name := fs.String("name", defaultServiceName, "Windows service name")
	logPath := fs.String("log", "", "legacy: use its directory for structured logs; raw stdout is not captured")
	if codeExit, ok := a.parse(fs, args); !ok {
		return codeExit
	}
	if fs.NArg() == 0 {
		return a.fail(fmt.Errorf("service run requires a child command"))
	}
	if *logging.dir == "" && *logPath != "" {
		*logging.dir = filepath.Dir(*logPath)
	}
	if !filepath.IsAbs(*logging.dir) {
		return a.fail(fmt.Errorf("service run requires an absolute --log-dir (or legacy --log path) prepared at install time"))
	}
	if info, err := os.Stat(*logging.dir); err != nil || !info.IsDir() {
		return a.fail(fmt.Errorf("prepare service log directory before service run"))
	}
	if err := runWindowsService(*name, logging.options("service", *logging.dir), fs.Args()); err != nil {
		return a.fail(err)
	}
	return 0
}

type childService struct {
	exe     string
	args    []string
	logging logbroker.Options
}

func runWindowsService(name string, logging logbroker.Options, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return svc.Run(name, &childService{exe: exe, args: args, logging: logging})
}

func (s *childService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	logs, err := logbroker.New(s.logging)
	if err != nil {
		return false, 1
	}
	defer logs.Write(logbroker.Record{Level: "info", Event: "service_stopped"})
	_ = logs.Write(logbroker.Record{Level: "info", Event: "service_started"})
	cmd := exec.Command(s.exe, s.args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Env = append(os.Environ(), "AGENTCHAT_LOG_DIR="+s.logging.Dir, "AGENTCHAT_LOG_MAX_BYTES="+fmt.Sprint(s.logging.MaxBytes), "AGENTCHAT_LOG_FILES="+fmt.Sprint(s.logging.Files), "AGENTCHAT_LOG_BODY_SUMMARY="+fmt.Sprint(s.logging.BodySummary))
	if err := cmd.Start(); err != nil {
		_ = logs.Write(logbroker.Record{Level: "error", Event: "child_failed"})
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
		case err := <-done:
			if err != nil {
				_ = logs.Write(logbroker.Record{Level: "error", Event: "child_failed"})
				return false, 1
			}
			return false, 0
		}
	}
}
