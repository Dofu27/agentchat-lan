//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestSecurityProcessFixture(t *testing.T) {
	if os.Getenv("AGENTCHAT_SECURITY_FIXTURE") != "1" {
		return
	}
	marker := os.Getenv("AGENTCHAT_SECURITY_MARKER")
	switch os.Getenv("AGENTCHAT_SECURITY_MODE") {
	case "orphan":
		exe, _ := os.Executable()
		c := exec.Command(exe, "-test.run=^TestSecurityProcessFixture$")
		c.Env = append(os.Environ(), "AGENTCHAT_SECURITY_MODE=descendant")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Start(); err != nil {
			os.Exit(2)
		}
		_ = c.Process.Release()
	case "descendant":
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker + ".release"); err == nil {
				_ = os.WriteFile(marker, []byte("unexpected descendant execution"), 0600)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "wait":
		time.Sleep(5 * time.Second)
	default:
		_ = os.WriteFile(marker, []byte("resumed"), 0600)
	}
	os.Exit(0)
}

func processFixture(t *testing.T, mode string) (string, []string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "marker")
	t.Setenv("AGENTCHAT_SECURITY_FIXTURE", "1")
	t.Setenv("AGENTCHAT_SECURITY_MODE", mode)
	t.Setenv("AGENTCHAT_SECURITY_MARKER", marker)
	return exe, []string{"-test.run=^TestSecurityProcessFixture$"}, marker
}

func TestProcessJobFailuresNeverResume(t *testing.T) {
	for _, stage := range []string{"create", "configure", "open", "assign", "resume"} {
		t.Run(stage, func(t *testing.T) {
			exe, args, marker := processFixture(t, "marker")
			api := nativeJobAPI()
			injected := errors.New("injected job API failure")
			resumed := false
			nativeResume := api.resume
			api.resume = func(pid uint32) error { resumed = true; return nativeResume(pid) }
			switch stage {
			case "create":
				api.create = func() (windows.Handle, error) { return 0, injected }
			case "configure":
				api.configure = func(windows.Handle) error { return injected }
			case "open":
				api.open = func(uint32) (windows.Handle, error) { return 0, injected }
			case "assign":
				api.assign = func(windows.Handle, windows.Handle) error { return injected }
			case "resume":
				api.resume = func(uint32) error { return injected }
			}
			// Retain a separate synchronization handle before the executor can
			// terminate/reap the child. PID lookup after exit is subject to reuse.
			var probe windows.Handle
			open := api.open
			api.open = func(pid uint32) (windows.Handle, error) {
				var err error
				probe, err = windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
				if err != nil {
					return 0, err
				}
				t.Cleanup(func() { windows.CloseHandle(probe) })
				return open(pid)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := runBoundedWithJob(ctx, exe, args, 1024, api)
			if !errors.Is(err, injected) {
				t.Fatalf("lost injected failure: %v", err)
			}
			if resumed {
				t.Fatal("resumed after containment failure")
			}
			if stage == "create" || stage == "configure" {
				if probe != 0 {
					t.Fatal("process reached open stage before job configuration")
				}
			} else {
				if probe == 0 {
					t.Fatal("missing suspended-process exit probe")
				}
				state, err := windows.WaitForSingleObject(probe, 0)
				if err != nil || state != windows.WAIT_OBJECT_0 {
					t.Fatalf("suspended process was not reaped before return: state=%d err=%v", state, err)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("child code executed on failing path")
			}
		})
	}
}

func TestProcessJobKillsNaturalExitDescendant(t *testing.T) {
	exe, args, marker := processFixture(t, "orphan")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, _ = runBoundedCommand(ctx, exe, args, 1024)
	// Release only after the executor returned (and its deferred Job close ran).
	// This avoids assuming a particular scheduling delay in a race-enabled child.
	if err := os.WriteFile(marker+".release", []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("descendant survived job close")
	}
}

func TestProcessJobCancellationBounded(t *testing.T) {
	exe, args, _ := processFixture(t, "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runBoundedCommand(ctx, exe, args, 1024)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation not bounded: %v", err)
	}
}

func TestServiceRejectsAncestorJunction(t *testing.T) {
	dir, err := os.MkdirTemp("", "agentchat-junction-")
	if err != nil {
		t.Fatal(err)
	}
	var link string
	t.Cleanup(func() {
		if link != "" {
			_ = exec.Command("cmd.exe", "/d", "/c", "rmdir", link).Run()
		}
		_ = os.RemoveAll(dir)
	})
	protected, outside := filepath.Join(dir, "protected"), filepath.Join(dir, "outside")
	for _, p := range []string{protected, outside} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "fixture.exe"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(protected, "link")
	if out, err := exec.Command(filepath.Join(systemDir, "cmd.exe"), "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("temporary junction: %v %s", err, out)
	}
	t.Setenv("ProgramFiles", protected)
	err = trustedServiceBinary(filepath.Join(link, "fixture.exe"))
	if err == nil || !strings.Contains(err.Error(), "reparse") {
		t.Fatalf("ancestor junction not rejected: %v", err)
	}
}

func TestServiceAccessCheckOrdinaryUser(t *testing.T) {
	token, err := ordinaryUserToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	groups, err := token.GetTokenGroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups.AllGroups() {
		if group.Sid.String() == "S-1-5-32-544" && (group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 || group.Attributes&windows.SE_GROUP_ENABLED != 0) {
			t.Fatal("restricted token retains an enabled/non-deny-only Administrators group")
		}
	}
	for _, tt := range []struct {
		sddl     string
		writable bool
	}{
		{"O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)", false},
		{"O:SYG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FW;;;BU)", true},
		{"O:SYG:SYD:P(D;;FW;;;BU)(A;;FA;;;SY)(A;;FW;;;BU)", false},
	} {
		sd, err := windows.SecurityDescriptorFromString(tt.sddl)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ordinaryUserCanMutate(sd, token, true)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.writable {
			t.Fatalf("AccessCheck writable=%v, want %v", got, tt.writable)
		}
	}
}

func TestPinProgramFilesChain(t *testing.T) {
	p, err := knownProgramFiles()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := pinDirectoryChain(p)
	if err != nil {
		t.Fatal(err)
	}
	chain.Close()
}

func TestServicePinnedIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := fileIdentity(h)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	// CI can expose TEMP through an 8.3 alias. The guard compares canonical final
	// paths; construct its fixture with that form rather than an unrelated alias.
	canonical, err := finalHandlePath(h)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	g := &serviceBinaryGuard{objects: []servicePathObject{{h, canonical, id, false}}}
	defer g.Close()
	if err := g.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err == nil {
		t.Fatal("pinned executable was replaced")
	}
}

func TestServiceRejectsUnrelatedAccountWriter(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString("O:SYG:SYD:P(A;;FA;;;SY)(A;;FR;;;BU)(A;;FW;;;S-1-5-21-111-222-333-1001)")
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectForeignServiceWriters(sd); err == nil {
		t.Fatal("unrelated writer escaped current-user AccessCheck")
	}
}

func TestCappedWriterContract(t *testing.T) {
	b := &cappedBuffer{limit: 16 << 10}
	p := make([]byte, 20<<10)
	n, err := b.Write(p)
	if err != nil || n != len(p) || b.buf.Len() != 16<<10 {
		t.Fatalf("invalid writer result: %d %v", n, err)
	}
}

func TestTruncatedJSONEscapeCredentialIsRedacted(t *testing.T) {
	prefix := `{"token":"fixtureCredentialPrefix`
	payload := prefix + strings.Repeat("x", 16383-len(prefix)) + `\\\\"}`
	if !json.Valid([]byte(payload)) {
		t.Fatal("fixture is not valid JSON")
	}
	b := &cappedBuffer{limit: 16 << 10}
	if _, err := b.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "fixtureCredentialPrefix") {
		t.Fatal("truncated JSON escape credential leaked")
	}
}

func TestTruncatedJSONCredentialIsRedacted(t *testing.T) {
	b := &cappedBuffer{limit: 16 << 10}
	if _, err := b.Write([]byte(`{"token":"fixtureCredentialPrefix` + strings.Repeat("x", 300000))); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(out, "fixtureCredentialPrefix") {
		t.Fatal("truncated JSON credential leaked")
	}
}
