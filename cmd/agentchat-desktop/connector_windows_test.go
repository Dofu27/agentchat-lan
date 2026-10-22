//go:build windows

package main

import (
	"os"
	"strings"
	"testing"
)

func TestCodexMCPBlock(t *testing.T) {
	got := codexMCPBlock(`C:\Program Files\AgentChat\agentchat.exe`, `C:\data\agent.json`)
	if !strings.Contains(got, `[mcp_servers.agentchat]`) || !strings.Contains(got, `"mcp"`) {
		t.Fatalf("unexpected MCP block:\n%s", got)
	}
	if !strings.Contains(got, `Program Files`) || !strings.Contains(got, `agent.json`) {
		t.Fatalf("MCP block lost paths:\n%s", got)
	}
}

func TestWriteCodexMCPRefusesDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := dir + `\config.toml`
	if err := os.WriteFile(path, []byte("[mcp_servers.agentchat]\ncommand = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCodexMCP(path, codexMCPBlock("new", "new.json")); err == nil {
		t.Fatal("expected duplicate refusal")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `command = "old"`) {
		t.Fatal("duplicate write changed config")
	}
}

func TestWriteCodexMCPBacksUp(t *testing.T) {
	dir := t.TempDir()
	path := dir + `\config.toml`
	if err := os.WriteFile(path, []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := writeCodexMCP(path, codexMCPBlock(`C:\AgentChat\agentchat.exe`, `C:\data\agent.json`))
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" {
		t.Fatal("expected backup path")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[mcp_servers.agentchat]") || !strings.Contains(string(got), `model = "x"`) {
		t.Fatalf("unexpected config:\n%s", got)
	}
}

func TestDSHBridgeExec(t *testing.T) {
	got := dshBridgeExec(`C:\AgentChat\agentchat.exe`, `C:\data\agent.json`, `C:\DSH\DSH Desktop.exe`, `C:\DSH\bin.js`, `C:\home`, `C:\work`)
	for _, want := range []string{"dsh-bridge", "--dsh", "--dsh-script", "--dsh-home", "--workdir", `C:\DSH\DSH Desktop.exe`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %s", want, got)
		}
	}
}
