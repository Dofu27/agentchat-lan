package cli

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentchat-lan/agentchat/internal/mockhub"
	"github.com/agentchat-lan/agentchat/internal/protocol"
)

func TestCLITextAndFileFlow(t *testing.T) {
	ts := httptest.NewServer(mockhub.New().Handler())
	defer ts.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	var stdout, stderr bytes.Buffer
	app := &App{Version: "test", Stdout: &stdout, Stderr: &stderr}

	run := func(args ...string) string {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(args); code != 0 {
			t.Fatalf("agentchat %s: code=%d stderr=%s", strings.Join(args, " "), code, stderr.String())
		}
		return stdout.String()
	}

	run("join", ts.URL, "--name", "cli-agent", "--kind", "test", "--config", cfgPath)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config was not written: %v", err)
	}

	run("send", "--room", protocol.DefaultRoom, "--text", "hello from cli", "--config", cfgPath)
	inboxJSON := run("inbox", "--wait", "0", "--json", "--config", cfgPath)
	if !strings.Contains(inboxJSON, "hello from cli") {
		t.Fatalf("inbox did not contain the message: %s", inboxJSON)
	}

	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("cli file bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pushJSON := run("push", "--room", protocol.DefaultRoom, "--file", src, "--text", "file attached", "--json", "--config", cfgPath)
	var pushed struct {
		Files   []protocol.FileRef `json:"files"`
		Message protocol.Message   `json:"message"`
	}
	if err := json.Unmarshal([]byte(pushJSON), &pushed); err != nil {
		t.Fatalf("decode push output: %v\n%s", err, pushJSON)
	}
	if len(pushed.Files) != 1 || pushed.Files[0].ID == "" {
		t.Fatalf("push output = %#v", pushed)
	}

	dst := filepath.Join(dir, "dst.txt")
	run("pull", pushed.Files[0].ID, "--out", dst, "--config", cfgPath)
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "cli file bytes\n" {
		t.Fatalf("downloaded = %q", got)
	}
}

func TestConfigShowRedactsToken(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	var stdout, stderr bytes.Buffer
	app := &App{Version: "test", Stdout: &stdout, Stderr: &stderr}

	ts := httptest.NewServer(mockhub.New().Handler())
	defer ts.Close()

	if code := app.Run([]string{"join", ts.URL, "--name", "redact-test", "--kind", "test", "--config", cfgPath}); code != 0 {
		t.Fatalf("join failed: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := app.Run([]string{"config", "show", "--config", cfgPath}); code != 0 {
		t.Fatalf("config show failed: %s", stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "mock_") {
		t.Fatalf("config show leaked a token: %s", out)
	}
	if !strings.Contains(out, "***redacted***") {
		t.Fatalf("config show did not redact: %s", out)
	}
}
