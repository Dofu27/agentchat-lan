//go:build windows

package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed assets
var assets embed.FS

func main() {
	role := flag.String("role", "hub", "hub, client, bridge, or observer")
	hub := flag.String("hub", "", "optional hub URL")
	installDir := flag.String("install-dir", "", "override install directory")
	noStartup := flag.Bool("no-startup", false, "do not create a login startup entry")
	noLaunch := flag.Bool("no-launch", false, "do not launch the console")
	uninstall := flag.Bool("uninstall", false, "remove shortcuts and binaries")
	removeData := flag.Bool("remove-data", false, "with -uninstall, remove the install directory")
	flag.Parse()

	switch *role {
	case "hub", "client", "bridge", "observer":
	default:
		fatal(fmt.Errorf("role must be hub, client, bridge, or observer"))
	}
	dir := strings.TrimSpace(*installDir)
	if dir == "" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			fatal(fmt.Errorf("LOCALAPPDATA is not set"))
		}
		dir = filepath.Join(base, "AgentChat")
	}
	if *uninstall {
		if err := uninstallDesktop(dir, *removeData); err != nil {
			fatal(err)
		}
		return
	}
	if err := installDesktop(dir, *role, *hub, !*noStartup); err != nil {
		fatal(err)
	}
	if !*noLaunch {
		exe := filepath.Join(dir, "AgentChatConsole.exe")
		cfg := filepath.Join(dir, "desktop.json")
		cmd := exec.Command(exe, "--config", cfg)
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			fatal(fmt.Errorf("launch console: %w", err))
		}
	}
}

func installDesktop(dir, role, hub string, startup bool) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"AgentChatConsole.exe", "agentchat.exe"} {
		data, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return fmt.Errorf("installer is missing %s (build the release installer)", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			return err
		}
	}
	cfgPath := filepath.Join(dir, "desktop.json")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfg := map[string]any{
			"title": "AgentChat Console", "width": 1180, "height": 820,
			"role": role, "agentchat_path": filepath.Join(dir, "agentchat.exe"),
		}
		if hub != "" {
			cfg["hub_url"] = hub
		}
		data, _ := json.MarshalIndent(cfg, "", "  ")
		if err := os.WriteFile(cfgPath, data, 0o600); err != nil {
			return err
		}
	}
	if startup {
		started := false
		startupDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		if err := os.MkdirAll(startupDir, 0o700); err == nil {
			line := "@echo off\r\nstart \"\" \"" + filepath.Join(dir, "AgentChatConsole.exe") + "\" --config \"" + cfgPath + "\"\r\n"
			if err := os.WriteFile(filepath.Join(startupDir, "AgentChat Console.cmd"), []byte(line), 0o600); err == nil {
				started = true
			}
		}
		if !started {
			// Some managed hosts deny the Startup folder. HKCU Run is the
			// per-user fallback and still needs no administrator rights.
			value := "\"" + filepath.Join(dir, "AgentChatConsole.exe") + "\" --config \"" + cfgPath + "\""
			err := exec.Command("reg.exe", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
				"/v", "AgentChatConsole", "/t", "REG_SZ", "/d", value, "/f").Run()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot create Startup entry: %v\n", err)
			} else {
				fmt.Println("Installed HKCU Run startup entry")
			}
		}
	}
	createStartMenu(dir, cfgPath)
	fmt.Printf("Installed AgentChat Console to %s\n", dir)
	return nil
}

func createStartMenu(dir, cfgPath string) {
	programs := os.Getenv("APPDATA")
	if programs == "" {
		return
	}
	folder := filepath.Join(programs, "Microsoft", "Windows", "Start Menu", "Programs", "AgentChat")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return
	}
	target := filepath.Join(dir, "AgentChatConsole.exe")
	link := filepath.Join(folder, "AgentChat Console.lnk")
	script := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut('" + strings.ReplaceAll(link, "'", "''") +
		"');$s.TargetPath='" + strings.ReplaceAll(target, "'", "''") +
		"';$s.Arguments='--config \"" + strings.ReplaceAll(cfgPath, "'", "''") +
		"\"';$s.WorkingDirectory='" + strings.ReplaceAll(dir, "'", "''") + "';$s.Save()"
	_ = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

func uninstallDesktop(dir string, removeData bool) error {
	startup := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "AgentChat Console.cmd")
	_ = os.Remove(startup)
	_ = exec.Command("reg.exe", "delete", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, "/v", "AgentChatConsole", "/f").Run()
	programs := os.Getenv("APPDATA")
	if programs != "" {
		_ = os.Remove(filepath.Join(programs, "Microsoft", "Windows", "Start Menu", "Programs", "AgentChat", "AgentChat Console.lnk"))
	}
	if removeData {
		return os.RemoveAll(dir)
	}
	for _, name := range []string{"AgentChatConsole.exe", "agentchat.exe"} {
		_ = os.Remove(filepath.Join(dir, name))
	}
	fmt.Printf("Removed AgentChat binaries from %s; configuration kept\n", dir)
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agentchat installer:", err)
	os.Exit(1)
}
