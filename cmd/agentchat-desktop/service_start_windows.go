//go:build windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type registeredService interface {
	Config() (mgr.Config, error)
	Query() (svc.Status, error)
	Control(svc.Cmd) (svc.Status, error)
	Start(...string) error
}

type executableVerifier interface {
	VerifyExecutable(string) error
	Close()
}

type openExecutableVerifier func(string) (executableVerifier, error)

func nativeExecutableVerifier(path string) (executableVerifier, error) {
	return openTrustedServiceBinary(path)
}

// The service handle identifies the registered SCM entry, independently of the
// desktop's configured CLI. Pin its actual executable through stop/wait/start.
// SCM configuration is administrator-owned; requery before Start also catches
// ordinary administrative changes during a restart. This does not isolate a
// malicious administrator racing StartService itself.
func startRegisteredService(s registeredService, action string, open openExecutableVerifier) error {
	if action != "start" && action != "restart" {
		return errors.New("unsupported verified service action")
	}
	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("query registered service configuration: %w", err)
	}
	args, err := windows.DecomposeCommandLine(cfg.BinaryPathName)
	if err != nil || len(args) == 0 || !filepath.IsAbs(args[0]) {
		return errors.New("registered service binPath has no valid absolute executable")
	}
	guard, err := open(args[0])
	if err != nil {
		return fmt.Errorf("refuse untrusted registered service executable: %w", err)
	}
	if guard == nil {
		return errors.New("registered service executable guard unavailable")
	}
	defer guard.Close()
	if err := guard.VerifyExecutable(args[0]); err != nil {
		return fmt.Errorf("verify registered executable identity: %w", err)
	}
	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query registered service state: %w", err)
	}
	if action == "start" && (st.State == svc.Running || st.State == svc.StartPending) {
		return nil
	}
	if action == "restart" && st.State != svc.Stopped {
		if st.State != svc.StopPending {
			if _, err := s.Control(svc.Stop); err != nil {
				return fmt.Errorf("stop registered service before restart: %w", err)
			}
		}
		deadline := time.Now().Add(15 * time.Second)
		for {
			st, err = s.Query()
			if err != nil {
				return fmt.Errorf("wait for registered service stop: %w", err)
			}
			if st.State == svc.Stopped {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("registered service did not stop before restart deadline")
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	latest, err := s.Config()
	if err != nil {
		return fmt.Errorf("requery registered service configuration: %w", err)
	}
	if latest.BinaryPathName != cfg.BinaryPathName {
		return errors.New("registered service binPath changed before start; retry after review")
	}
	if err := guard.VerifyExecutable(args[0]); err != nil {
		return fmt.Errorf("reverify registered executable identity: %w", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start verified registered service: %w", err)
	}
	return nil
}
