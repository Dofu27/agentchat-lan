//go:build windows

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestServiceCommandRejectsIncompleteTLSBeforeGuard(t *testing.T) {
	for _, cfg := range []config{
		{HubData: "data", HubListen: "127.0.0.1:8443", HubCert: "certificate.pem"},
		{HubData: "data", HubListen: "127.0.0.1:8443", HubKey: "private-key.pem"},
	} {
		command, err := hubServiceBinPath(cfg, nil)
		if command != "" || err == nil || !strings.Contains(err.Error(), "TLS certificate and key must be configured together") {
			t.Fatal("incomplete TLS configuration was not rejected before command generation")
		}
	}
}

func TestServiceCommandUsesPinnedExecutableAndRoundTripsArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinned fixture.exe")
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := fileIdentity(h)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	canonical, err := finalHandlePath(h)
	if err != nil {
		windows.CloseHandle(h)
		t.Fatal(err)
	}
	g := &serviceBinaryGuard{objects: []servicePathObject{{h, canonical, id, false}}}
	defer g.Close()
	for _, data := range []string{`C:\fixture\data\`, `C:\fixture space\data\`, `C:\fixture&data`, `C:\fixture"quoted`} {
		cfg := config{AgentchatPath: `C:\different\unverified.exe`, HubData: data, HubFiles: filepath.Join(data, "files"), HubListen: "127.0.0.1:8443", HubCert: `C:\cert space\certificate.pem`, HubKey: `C:\key space\key.pem`}
		command, err := hubServiceBinPath(cfg, g)
		if err != nil {
			t.Fatal(err)
		}
		got, err := windows.DecomposeCommandLine(command)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{canonical, "service", "run", "--name", "AgentChatHub", "--log", filepath.Join(data, "hub.log"), "--", "hub", "serve", "--data", data, "--files-dir", cfg.HubFiles, "--listen", cfg.HubListen, "--tls-cert", cfg.HubCert, "--tls-key", cfg.HubKey}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("SCM arguments changed: got %q want %q", got, want)
		}
	}
	if err := g.VerifyExecutable(canonical); err != nil {
		t.Fatal("SCM executable did not match pinned file", err)
	}
	other := filepath.Join(filepath.Dir(path), "other.exe")
	if err := os.WriteFile(other, []byte("other synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.VerifyExecutable(other); err == nil {
		t.Fatal("different SCM executable passed FileId binding")
	}
	g.objects[0].id.low++
	if _, err := hubServiceBinPath(config{HubData: "data", HubListen: "listen"}, g); err == nil {
		t.Fatal("changed executable identity accepted")
	}
	if _, err := hubServiceBinPath(config{HubData: "data", HubListen: "listen"}, nil); err == nil {
		t.Fatal("missing guard accepted")
	}
}
