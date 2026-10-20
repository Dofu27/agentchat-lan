package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

func TestFederationCLIIdentityAndConfig(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	app := &App{Stdout: &stdout, Stderr: &stderr}
	args := []string{"hub", "federation-id", "--data", dir}
	if app.Run(args) != 0 {
		t.Fatal(stderr.String())
	}
	var first map[string]string
	if json.Unmarshal(stdout.Bytes(), &first) != nil || first["node_id"] == "" {
		t.Fatal("missing node identity")
	}
	stdout.Reset()
	if app.Run(args) != 0 {
		t.Fatal(stderr.String())
	}
	var second map[string]string
	json.Unmarshal(stdout.Bytes(), &second)
	if first["node_id"] != second["node_id"] {
		t.Fatal("CLI identity changed")
	}
	secret := "independent-cli-test-peer-secret-123456789"
	secretPath := filepath.Join(dir, "peer.secret")
	if err := os.WriteFile(secretPath, []byte(secret+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "federation.json")
	raw := `{"peer":{"node_id":"remote-node","base_url":"http://127.0.0.1:1","secret_file":"peer.secret"},"links":[{"local_room":"general","remote_room":"general"}],"max_outbox":10,"max_attempts":2}`
	if err := os.WriteFile(configPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), hubDBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fed, err := loadFederation(context.Background(), st, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if fed.NodeID() != first["node_id"] {
		t.Fatal("node identity changed")
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatal("secret printed")
	}
	if err := os.WriteFile(configPath, []byte(`{"peer":{"node_id":"remote","secret":"must-not-be-inline"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFederation(context.Background(), st, configPath); err == nil {
		t.Fatal("unknown inline secret accepted")
	}
}
func TestFederationServeFlagLifecycle(t *testing.T) {
	dir := t.TempDir()
	secret := "isolated-startup-test-secret-123456789"
	if err := os.WriteFile(filepath.Join(dir, "peer.secret"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "federation.json")
	if err := os.WriteFile(configPath, []byte(`{"peer":{"node_id":"remote-node","base_url":"http://127.0.0.1:1","secret_file":"peer.secret"},"links":[{"local_room":"general","remote_room":"general"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	started := make(chan string, 1)
	done := make(chan int, 1)
	app := &App{Stdout: hubStartupWriter{started: started}, Stderr: &stderr}
	go func() {
		done <- app.hubServeContext(ctx, []string{"--data", dir, "--listen", "127.0.0.1:0", "--federation-config", configPath})
	}()
	select {
	case line := <-started:
		response, err := http.Get(strings.Fields(line)[3] + "/healthz")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Error("health failed")
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("federation hub failed to start")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatal(stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not shut down with hub")
	}
	if strings.Contains(stderr.String(), secret) {
		t.Fatal("secret printed")
	}
	var unsafeOutput, unsafeError bytes.Buffer
	unsafeApp := &App{Stdout: &unsafeOutput, Stderr: &unsafeError}
	if code := unsafeApp.hubServeContext(context.Background(), []string{"--data", dir, "--listen", "0.0.0.0:0", "--federation-config", configPath}); code == 0 || !strings.Contains(unsafeError.String(), "requires HTTPS") {
		t.Fatal("federation exposed on non-loopback plaintext HTTP")
	}
}
