package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHubInitInviteAndAudit(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "admin.token")
	var stdout, stderr bytes.Buffer
	app := &App{Version: "test", Stdout: &stdout, Stderr: &stderr}

	run := func(args ...string) string {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if code := app.Run(args); code != 0 {
			t.Fatalf("agentchat %v: code=%d stderr=%s", args, code, stderr.String())
		}
		return stdout.String()
	}

	initJSON := run("hub", "init", "--data", dir, "--admin-token-file", tokenPath, "--json")
	var initOut struct {
		Database string `json:"database"`
	}
	if err := json.Unmarshal([]byte(initJSON), &initOut); err != nil {
		t.Fatalf("decode init: %v\n%s", err, initJSON)
	}
	if initOut.Database != filepath.Join(dir, "agentchat.db") {
		t.Fatalf("database = %q", initOut.Database)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("admin token file: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(tokenPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("token file mode = %o", info.Mode().Perm())
		}
	}

	inviteJSON := run("hub", "invite", "--data", dir, "--ttl", "1h", "--uses", "2", "--json")
	var invite struct {
		Code string `json:"code"`
		Room string `json:"room"`
		Uses int    `json:"uses"`
	}
	if err := json.Unmarshal([]byte(inviteJSON), &invite); err != nil {
		t.Fatalf("decode invite: %v\n%s", err, inviteJSON)
	}
	if invite.Code == "" || invite.Room != "general" || invite.Uses != 2 {
		t.Fatalf("invite = %#v", invite)
	}

	auditJSON := run("hub", "audit", "--data", dir, "--json")
	if len(auditJSON) == 0 {
		t.Fatal("audit returned empty output")
	}
}
