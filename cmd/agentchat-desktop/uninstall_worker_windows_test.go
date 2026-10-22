//go:build windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeTestMarker(t *testing.T, root string, marker uninstallMarker) {
	t.Helper()
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agentchat-install.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallWorkerRejectsJunctionRoot(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(t.TempDir(), "install")
	victim := filepath.Join(target, "victim.txt")
	if err := os.WriteFile(victim, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("cmd.exe", "/c", "mklink", "/J", root, target).Run(); err != nil {
		t.Skipf("junction unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(root) })
	if code := runUninstallWorker([]string{root, "0", "false"}); code == 0 {
		t.Fatalf("worker accepted a junction root")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim was removed: %v", err)
	}
}

func TestPinFileBlocksSamePathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentchat.exe")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	pinned, err := pinFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.delete()
	moved := path + ".moved"
	if err := os.Rename(path, moved); err == nil {
		t.Fatalf("pinned file was renamed")
	}
	if err := os.Remove(path); err == nil {
		t.Fatalf("pinned file was deleted")
	}
}

func TestUninstallWorkerRejectsReplacedRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "install")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim.txt")
	if err := os.WriteFile(victim, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestMarker(t, root, uninstallMarker{
		App: "agentchat-desktop", Root: root,
		VolumeSerial: 1, FileIndexHigh: 2, FileIndexLow: 3,
		Files: []string{"AgentChatConsole.exe"},
	})
	if code := runUninstallWorker([]string{root, "0", "false"}); code == 0 {
		t.Fatalf("worker accepted an identity-mismatched root")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("victim was removed: %v", err)
	}
}
