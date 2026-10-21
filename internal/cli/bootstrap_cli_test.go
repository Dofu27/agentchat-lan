package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHubInitResumeCLI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	app := &App{Version: "test", Stdout: &stdout, Stderr: &stderr}
	run := func(args ...string) int {
		stdout.Reset()
		stderr.Reset()
		return app.Run(args)
	}
	if code := run("hub", "init", "--resume-init"); code == 0 || !strings.Contains(stderr.String(), "requires --admin-token-file") {
		t.Fatal("resume without a retained credential was not rejected")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "retained.token")
	args := []string{"hub", "init", "--data", filepath.Join(dir, "data"), "--admin-token-file", path, "--json"}
	if code := run(args...); code != 0 {
		t.Fatalf("file-backed init failed: exit=%d", code)
	}
	credential, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	checkPrivateOutput := func() {
		t.Helper()
		var output map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
			t.Fatal("invalid CLI init JSON")
		}
		if _, present := output["admin_token"]; present {
			t.Fatal("file-backed CLI returned an admin token field")
		}
		if strings.Contains(stdout.String()+stderr.String(), strings.TrimSpace(string(credential))) {
			t.Fatal("CLI leaked retained credential")
		}
	}
	checkPrivateOutput()
	if code := run(append(append([]string{}, args...), "--resume-init")...); code != 0 {
		t.Fatalf("explicit resume failed: exit=%d", code)
	}
	checkPrivateOutput()
	if code := run(args...); code == 0 {
		t.Fatal("ordinary init overwrote an existing credential")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, credential) {
		t.Fatal("retry changed retained credential")
	}
}
