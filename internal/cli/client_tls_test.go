package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/mockhub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCLIHTTPSJoinAndCommands(t *testing.T) {
	certFile, keyFile, _ := hubTestCertificate(t)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	handler := mockhub.New().Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	var stdout, stderr bytes.Buffer
	app := &App{Version: "test", Stdout: &stdout, Stderr: &stderr}
	run := func(args ...string) string {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(args); code != 0 {
			t.Fatalf("%s failed: %s", args, stderr.String())
		}
		return stdout.String()
	}
	sum := sha256.Sum256(cert.Certificate[0])
	pin := "sha256:" + hex.EncodeToString(sum[:])
	for _, flags := range [][]string{
		nil, {"--fingerprint", pin}, {"--ca-file", certFile, "--fingerprint", pin},
		{"--fingerprint", "malformed"}, {"--ca-file", certFile + ".missing"},
		{"--fingerprint="}, {"--ca-file="},
	} {
		stdout.Reset()
		stderr.Reset()
		args := append([]string{"join", srv.URL, "--name", "test", "--config", path}, flags...)
		if code := app.Run(args); code == 0 {
			t.Fatalf("invalid trust accepted: %v", flags)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("failed join saved config")
		}
	}
	if requests.Load() != 0 {
		t.Fatal("join sent before trust verification")
	}
	t.Chdir(filepath.Dir(certFile))
	run("join", srv.URL, "--name", "tls-cli", "--ca-file", filepath.Base(certFile), "--config", path)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCAFile != certFile || cfg.TLSFingerprint != "" {
		t.Fatal("join did not persist CA trust")
	}
	for _, command := range []string{"rooms", "who", "me", "doctor"} {
		run(command, "--config", path)
	}
	run("send", "--text", "HTTPS message", "--config", path)
	run("activity", "--run-id", "tls-test", "--phase", "working", "--title", "HTTPS activity", "--config", path)
	if !strings.Contains(run("inbox", "--config", path, "--json"), "HTTPS message") {
		t.Fatal("inbox lost message")
	}
	if !strings.Contains(run("history", "--config", path, "--json"), "HTTPS message") {
		t.Fatal("history lost message")
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("HTTPS file content"), 0600); err != nil {
		t.Fatal(err)
	}
	result := run("push", "--file", source, "--config", path, "--json")
	var pushed struct {
		Files []protocol.FileRef `json:"files"`
	}
	if err := json.Unmarshal([]byte(result), &pushed); err != nil || len(pushed.Files) != 1 {
		t.Fatalf("push: %s %v", result, err)
	}
	dest := filepath.Join(t.TempDir(), "download.txt")
	run("pull", pushed.Files[0].ID, "--out", dest, "--config", path)
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "HTTPS file content" {
		t.Fatalf("file transfer failed: %s %v", data, err)
	}
	if strings.Contains(run("config", "show", "--config", path), cfg.Token) {
		t.Fatal("config leaked token")
	}

	// Changing trust in the saved config must affect subsequent CLI and MCP
	// requests, rather than silently using the old/default transport.
	otherCA, _, _ := hubTestCertificate(t)
	for _, ca := range []string{certFile, otherCA} {
		cfg.TLSCAFile = ca
		if err := config.Save(path, cfg); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTLSMCPProcess$", "--", path)
		cmd.Env = append(os.Environ(), "AGENTCHAT_TEST_MCP=1")
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "tls-test", Version: "1"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for _, tool := range []string{"chat_rooms", "chat_who", "chat_inbox"} {
			resp, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{}})
			if err != nil {
				cs.Close()
				cancel()
				t.Fatal(err)
			}
			if resp.IsError != (ca == otherCA) {
				t.Fatalf("MCP %s TLS verification result: %#v", tool, resp)
			}
		}
		cs.Close()
		cancel()
		stdout.Reset()
		stderr.Reset()
		code := app.Run([]string{"rooms", "--config", path})
		if (code == 0) != (ca == certFile) {
			t.Fatalf("CLI ignored config trust: %s", stderr.String())
		}
	}
}

func TestTLSMCPProcess(t *testing.T) {
	if os.Getenv("AGENTCHAT_TEST_MCP") != "1" {
		return
	}
	os.Exit(Run([]string{"mcp", "--config", os.Args[len(os.Args)-1]}, "test"))
}

func TestJoinRejectsTLSOptionsForHTTP(t *testing.T) {
	for _, flags := range [][]string{{"--ca-file", "ca.pem"}, {"--fingerprint", "sha256:" + strings.Repeat("a", 64)}} {
		var stdout, stderr bytes.Buffer
		app := &App{Stdout: &stdout, Stderr: &stderr}
		args := append([]string{"join", "http://127.0.0.1:1", "--name", "test", "--config", filepath.Join(t.TempDir(), "cfg.json")}, flags...)
		if app.Run(args) == 0 || !strings.Contains(stderr.String(), "https://") {
			t.Fatalf("HTTP trust accepted: %s", stderr.String())
		}
	}
}

func TestInsecureHTTPWarningAndDoctor(t *testing.T) {
	for _, tc := range []struct {
		url      string
		insecure bool
	}{
		{"http://127.0.0.1:8787", false}, {"http://127.9.8.7:8787", false}, {"http://[::1]:8787", false}, {"http://localhost:8787", false},
		{"http://agentchat.lan:8787", true}, {"http://hub.lan:8787", true}, {"http://[::]:8787", true}, {"https://hub.lan:8787", false},
	} {
		if got := insecureHTTP(tc.url); got != tc.insecure {
			t.Errorf("insecureHTTP(%s)=%v", tc.url, got)
		}
	}
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}
	c, err := app.newClient(&config.Config{HubURL: "http://hub.lan:8787"})
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP.CloseIdleConnections()
	if !strings.Contains(stderr.String(), "WARNING: non-loopback HTTP") {
		t.Fatal("missing warning")
	}
	// No network route is needed to check the doctor security indicator.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, &config.Config{HubURL: "http://0.0.0.0:1", Token: "test", AgentName: "test"}); err != nil {
		t.Fatal(err)
	}
	if code := app.Run([]string{"doctor", "--config", path}); code == 0 || !strings.Contains(stdout.String(), "[FAIL] transport") {
		t.Fatalf("doctor did not flag insecure HTTP: %s", stdout.String())
	}
}
