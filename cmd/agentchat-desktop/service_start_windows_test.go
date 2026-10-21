//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type startProbeGuard struct {
	closed bool
	calls  int
	failAt int
	path   string
}

func (g *startProbeGuard) Close() { g.closed = true }
func (g *startProbeGuard) VerifyExecutable(path string) error {
	if g.closed {
		return errors.New("verification used a closed guard")
	}
	g.calls++
	g.path = path
	if g.calls == g.failAt {
		return errors.New("injected FileId mismatch")
	}
	return nil
}

type startProbeService struct {
	binary, changedBinary                  string
	state                                  svc.State
	configs, starts, stops                 int
	configErr, queryErr, stopErr, startErr error
	guard                                  *startProbeGuard
}

func (s *startProbeService) Config() (mgr.Config, error) {
	s.configs++
	if s.configErr != nil {
		return mgr.Config{}, s.configErr
	}
	if s.configs > 1 && s.changedBinary != "" {
		return mgr.Config{BinaryPathName: s.changedBinary}, nil
	}
	return mgr.Config{BinaryPathName: s.binary}, nil
}
func (s *startProbeService) Query() (svc.Status, error) {
	return svc.Status{State: s.state}, s.queryErr
}
func (s *startProbeService) Control(svc.Cmd) (svc.Status, error) {
	s.stops++
	if s.guard == nil || s.guard.closed {
		return svc.Status{}, errors.New("stop without a live guard")
	}
	if s.stopErr != nil {
		return svc.Status{}, s.stopErr
	}
	s.state = svc.Stopped
	return svc.Status{State: s.state}, nil
}
func (s *startProbeService) Start(...string) error {
	s.starts++
	if s.guard == nil || s.guard.closed || s.guard.calls < 2 {
		return errors.New("start without a live reverified guard")
	}
	return s.startErr
}

func TestRegisteredServiceRejectsLegacyUserWritableExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-user-writable.exe")
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "restart"} {
		t.Run(action, func(t *testing.T) {
			s := &startProbeService{binary: windows.ComposeCommandLine([]string{path, "service", "run"}), state: svc.Running}
			err := startRegisteredService(s, action, nativeExecutableVerifier)
			if err == nil || !strings.Contains(err.Error(), "refuse untrusted") || s.starts != 0 || s.stops != 0 {
				t.Fatalf("legacy unsafe service was controlled: start=%d stop=%d err=%v", s.starts, s.stops, err)
			}
		})
	}
}

func TestRegisteredServicePinsSCMExecutableThroughStartAndRestart(t *testing.T) {
	for _, tc := range []struct {
		action string
		state  svc.State
		stops  int
	}{
		{"start", svc.Stopped, 0}, {"restart", svc.Running, 1}, {"restart", svc.Stopped, 0},
	} {
		t.Run(tc.action+"-"+serviceState(tc.state), func(t *testing.T) {
			g := &startProbeGuard{}
			path := `C:\Windows\verified service.exe`
			s := &startProbeService{binary: windows.ComposeCommandLine([]string{path, "service", "run"}), state: tc.state, guard: g}
			err := startRegisteredService(s, tc.action, func(got string) (executableVerifier, error) {
				if got != path {
					return nil, errors.New("did not use registered SCM executable")
				}
				return g, nil
			})
			if err != nil || s.starts != 1 || s.stops != tc.stops || g.calls != 2 || g.path != path || !g.closed {
				t.Fatalf("bad guarded lifecycle: start=%d stop=%d verify=%d closed=%v err=%v", s.starts, s.stops, g.calls, g.closed, err)
			}
		})
	}
}

func TestRegisteredServiceFailsClosedBeforeStart(t *testing.T) {
	for _, tc := range []string{"missing", "malformed", "relative", "identity-before-stop", "identity-before-start", "configuration-changed", "query-error", "stop-error", "start-error"} {
		t.Run(tc, func(t *testing.T) {
			g := &startProbeGuard{}
			s := &startProbeService{binary: `C:\Windows\verified.exe --service`, state: svc.Stopped, guard: g}
			switch tc {
			case "missing":
				s.configErr = windows.ERROR_SERVICE_DOES_NOT_EXIST
			case "malformed":
				s.binary = "\x00"
			case "relative":
				s.binary = "relative.exe --service"
			case "identity-before-stop":
				g.failAt = 1
				s.state = svc.Running
			case "identity-before-start":
				g.failAt = 2
			case "configuration-changed":
				s.changedBinary = `C:\Users\fixture\untrusted.exe`
			case "query-error":
				s.queryErr = errors.New("injected query error")
			case "stop-error":
				s.state = svc.Running
				s.stopErr = errors.New("injected stop error")
			case "start-error":
				s.startErr = errors.New("injected start error")
			}
			opened := false
			err := startRegisteredService(s, "restart", func(string) (executableVerifier, error) { opened = true; return g, nil })
			if err == nil {
				t.Fatal("failure was accepted")
			}
			wantStarts := 0
			if tc == "start-error" {
				wantStarts = 1
			}
			if s.starts != wantStarts {
				t.Fatalf("unexpected Start call count %d", s.starts)
			}
			if opened && !g.closed {
				t.Fatal("guard leaked on failed service control")
			}
			if tc == "identity-before-stop" && s.stops != 0 {
				t.Fatal("unsafe restart stopped the service before verifying its executable")
			}
		})
	}
}
