package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const defaultServiceName = "agentchat-agent"

func (a *App) cmdService(args []string) int {
	if len(args) == 0 {
		return a.fail(fmt.Errorf("usage: agentchat service install|uninstall|status"))
	}
	switch args[0] {
	case "install":
		return a.serviceInstall(args[1:])
	case "uninstall":
		return a.serviceUninstall(args[1:])
	case "status":
		return a.serviceStatus(args[1:])
	default:
		return a.fail(fmt.Errorf("unknown service command %q", args[0]))
	}
}

func (a *App) serviceInstall(args []string) int {
	fs := a.newFlagSet("service install")
	name := fs.String("name", defaultServiceName, "service or task name")
	cfgPath := fs.String("config", "", "agent config file path")
	hook := fs.String("exec", "", "optional agent hook command")
	if codeExit, ok := a.parse(fs, args); !ok {
		return codeExit
	}
	if strings.TrimSpace(*cfgPath) == "" {
		return a.fail(fmt.Errorf("--config is required"))
	}
	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	exe, err := os.Executable()
	if err != nil {
		return a.fail(err)
	}
	agentArgs := []string{"agent", "--config", abs}
	if strings.TrimSpace(*hook) != "" {
		agentArgs = append(agentArgs, "--exec", *hook)
	}

	if runtime.GOOS == "windows" {
		tr := quoteWindowsCommand(exe) + " " + quoteWindowsArgs(agentArgs)
		cmd := exec.Command("schtasks", "/Create", "/TN", *name, "/SC", "ONLOGON", "/TR", tr, "/F")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return a.fail(fmt.Errorf("create scheduled task: %w: %s", err, strings.TrimSpace(string(out))))
		}
		fmt.Fprintf(a.Stdout, "installed scheduled task %s at logon\n", *name)
		return 0
	}
	if runtime.GOOS == "linux" {
		unitPath, err := userUnitPath(*name)
		if err != nil {
			return a.fail(err)
		}
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
			return a.fail(err)
		}
		unit := fmt.Sprintf(`[Unit]
Description=AgentChat LAN agent loop
After=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`, systemdCommand(exe, agentArgs))
		if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
			return a.fail(err)
		}
		if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			return a.fail(fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out))))
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", *name).CombinedOutput(); err != nil {
			return a.fail(fmt.Errorf("systemctl enable: %w: %s", err, strings.TrimSpace(string(out))))
		}
		fmt.Fprintf(a.Stdout, "installed systemd user service %s\n", *name)
		return 0
	}
	return a.fail(fmt.Errorf("service install is not implemented on %s", runtime.GOOS))
}

func (a *App) serviceUninstall(args []string) int {
	fs := a.newFlagSet("service uninstall")
	name := fs.String("name", defaultServiceName, "service or task name")
	if codeExit, ok := a.parse(fs, args); !ok {
		return codeExit
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("schtasks", "/Delete", "/TN", *name, "/F").CombinedOutput()
		if err != nil {
			return a.fail(fmt.Errorf("delete scheduled task: %w: %s", err, strings.TrimSpace(string(out))))
		}
		fmt.Fprintf(a.Stdout, "removed scheduled task %s\n", *name)
		return 0
	}
	if runtime.GOOS == "linux" {
		_ = exec.Command("systemctl", "--user", "disable", "--now", *name).Run()
		unitPath, err := userUnitPath(*name)
		if err != nil {
			return a.fail(err)
		}
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			return a.fail(err)
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Fprintf(a.Stdout, "removed systemd user service %s\n", *name)
		return 0
	}
	return a.fail(fmt.Errorf("service uninstall is not implemented on %s", runtime.GOOS))
}

func (a *App) serviceStatus(args []string) int {
	fs := a.newFlagSet("service status")
	name := fs.String("name", defaultServiceName, "service or task name")
	if codeExit, ok := a.parse(fs, args); !ok {
		return codeExit
	}
	if runtime.GOOS == "windows" {
		out, err := exec.Command("schtasks", "/Query", "/TN", *name).CombinedOutput()
		if err != nil {
			return a.fail(fmt.Errorf("scheduled task %s is not installed: %s", *name, strings.TrimSpace(string(out))))
		}
		fmt.Fprint(a.Stdout, string(out))
		return 0
	}
	if runtime.GOOS == "linux" {
		out, err := exec.Command("systemctl", "--user", "is-active", *name).CombinedOutput()
		if err != nil {
			return a.fail(fmt.Errorf("systemd user service %s is not active: %s", *name, strings.TrimSpace(string(out))))
		}
		fmt.Fprint(a.Stdout, string(out))
		return 0
	}
	return a.fail(fmt.Errorf("service status is not implemented on %s", runtime.GOOS))
}

func userUnitPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "systemd", "user", name+".service"), nil
}

func quoteWindowsCommand(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func quoteWindowsArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteWindowsCommand(arg))
	}
	return strings.Join(quoted, " ")
}

func systemdCommand(exe string, args []string) string {
	quoted := []string{quoteWindowsCommand(exe)}
	for _, arg := range args {
		quoted = append(quoted, quoteWindowsCommand(arg))
	}
	return strings.Join(quoted, " ")
}
