package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/logbroker"
	"github.com/Dofu27/agentchat-lan/internal/mockhub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBridgeStructuredFilesIndependentOfViewer(t *testing.T) {
	ts := httptest.NewServer(mockhub.New().Handler())
	defer ts.Close()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	logDir := filepath.Join(dir, "logs")
	var out, errs bytes.Buffer
	app := &App{Stdout: &out, Stderr: &errs}
	if code := app.Run([]string{"join", ts.URL, "--name", "viewer-test", "--kind", "test", "--config", cfgPath}); code != 0 {
		t.Fatal(errs.String())
	}
	sender, e := client.New(ts.URL, "").Join(context.Background(), protocol.JoinRequest{Name: "sender", Kind: "test"})
	if e != nil {
		t.Fatal(e)
	}
	text := "PRIVATE KEY\nmessage-body-NEVER_IN_LOG"
	if _, e = client.New(ts.URL, sender.Token).SendMessage(context.Background(), "general", protocol.SendMessageRequest{Text: text}); e != nil {
		t.Fatal(e)
	}
	// No reader or UI exists while the agent receives and acknowledges messages.
	if code := app.Run([]string{"agent", "--config", cfgPath, "--once", "--wait", "0", "--log-body-summary"}); code != 0 {
		t.Fatal(errs.String())
	}
	out.Reset()
	if code := app.Run([]string{"logs", "tail", "--dir", logDir, "--component", "bridge"}); code != 0 {
		t.Fatal(errs.String())
	}
	var batch logbroker.Batch
	if e = json.Unmarshal(out.Bytes(), &batch); e != nil {
		t.Fatal(e)
	}
	cursor := batch.Cursor
	found := false
	cfg, e := config.Load(cfgPath)
	if e != nil {
		t.Fatal(e)
	}
	for _, forbidden := range []string{sender.Token, cfg.Token, text, "PRIVATE KEY", "NEVER_IN_LOG"} {
		if strings.Contains(out.String(), forbidden) {
			t.Fatal("sensitive data in local broker")
		}
	}
	for _, entry := range batch.Entries {
		if entry.Record.Event == "received" {
			found = true
			if entry.Record.Room != "general" || entry.Record.Agent == "" || entry.Record.BodyChars == nil {
				t.Fatal(entry)
			}
		}
	}
	if !found {
		t.Fatal("bridge did not record received metadata")
	}
	// Producer restarts while the viewer is absent, then the saved cursor resumes.
	if code := app.Run([]string{"agent", "--config", cfgPath, "--once", "--wait", "0"}); code != 0 {
		t.Fatal(errs.String())
	}
	out.Reset()
	if code := app.Run([]string{"logs", "tail", "--dir", logDir, "--component", "bridge", "--after", cursor}); code != 0 {
		t.Fatal(errs.String())
	}
	if strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
		t.Fatal("viewer batches are not JSONL")
	}
	if e = json.Unmarshal(out.Bytes(), &batch); e != nil || len(batch.Entries) < 2 || batch.Gap {
		t.Fatal(batch, e)
	}
	// Failed queue commands do not spill prompt, command arguments or error text.
	out.Reset()
	errs.Reset()
	if code := app.Run([]string{"codex-bridge", "--config", cfgPath, "--thread", "fixture-thread", "--wait-turn", "0", "--message", text, "--codex", filepath.Join(dir, "missing.exe")}); code != 1 {
		t.Fatal(code)
	}
	batch, e = (logbroker.Reader{Dir: logDir}).Read(logbroker.Query{Component: "bridge", After: batch.Cursor, Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(batch)
	if bytes.Contains(raw, []byte("NEVER_IN_LOG")) || bytes.Contains(raw, []byte("missing.exe")) {
		t.Fatal("queue diagnostic leaked arbitrary text")
	}
	found = false
	for _, entry := range batch.Entries {
		found = found || entry.Record.Event == "queue_failed"
	}
	if !found {
		t.Fatal("missing queue failure metadata")
	}
}
func TestLogTailCannotInitializeMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	var out, errs bytes.Buffer
	a := &App{Stdout: &out, Stderr: &errs}
	if code := a.Run([]string{"logs", "tail", "--dir", dir}); code != 1 {
		t.Fatal(code)
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("tail created directory")
	}
}

func TestHubStopsWhenStructuredLoggingFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	started := make(chan string, 1)
	var errs bytes.Buffer
	app := &App{Stdout: hubStartupWriter{started: started}, Stderr: &errs}
	done := make(chan int, 1)
	go func() { done <- app.hubServeContext(ctx, []string{"--data", dir, "--listen", "127.0.0.1:0"}) }()
	var address string
	select {
	case line := <-started:
		address = strings.Fields(line)[3]
	case code := <-done:
		t.Fatalf("startup %d %s", code, errs.String())
	case <-time.After(5 * time.Second):
		t.Fatal("startup timeout")
	}
	if err := os.Mkdir(filepath.Join(logDir, "hub-00000000000000000002.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(address + "/healthz")
	if err == nil {
		resp.Body.Close()
	}
	select {
	case code := <-done:
		if code != 1 || !strings.Contains(errs.String(), "structured logging failed") {
			t.Fatal(code, errs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hub kept running without file logs")
	}
}
