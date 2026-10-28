package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/logbroker"
)

const defaultServiceName = "agentchat-agent"

func (a *App) cmdService(args []string) int {
	if len(args) == 0 {
		return a.fail(fmt.Errorf("usage: agentchat service install|uninstall|status|run"))
	}
	switch args[0] {
	case "install":
		return a.serviceInstall(args[1:])
	case "uninstall":
		return a.serviceUninstall(args[1:])
	case "status":
		return a.serviceStatus(args[1:])
	case "run":
		return a.serviceRun(args[1:])
	default:
		return a.fail(fmt.Errorf("unknown service command %q", args[0]))
	}
}

func (a *App) serviceInstall(args []string) int {
	fs := a.newFlagSet("service install")
	logging := logFlags(fs)
	logSID := fs.String("log-user-sid", "", "Windows user SID allowed with LocalSystem to write logs")
	name := fs.String("name", defaultServiceName, "service or task name")
	cfgPath := fs.String("config", "", "agent config file path")
	hook := fs.String("exec", "", "optional agent hook command")
	mentionOnly := fs.Bool("mention-only", false, "only invoke the hook for @me, @all, and DM messages")
	typing := fs.Bool("typing", false, "publish typing while the hook runs")
	startNow := fs.Bool("start-now", false, "ack the existing inbox before serving new messages")
	startCutoffs := fs.String("start-cutoffs", "", "legacy hub cutoff as room=seq,room=seq")
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
	logOpts := logging.options("bridge", filepath.Join(filepath.Dir(abs), "logs"))
	if err := os.MkdirAll(logOpts.Dir, 0700); err != nil {
		return a.fail(fmt.Errorf("prepare logs failed"))
	}
	if err := logbroker.PreparePermissions(logOpts.Dir, *logSID); err != nil {
		if runtime.GOOS != "windows" {
			return a.fail(fmt.Errorf("prepare log permissions failed"))
		}
		// Per-user bridges cannot always change a protected DACL. The log
		// directory is still user-owned; keep the install usable and surface
		// the degraded ACL preparation explicitly.
		fmt.Fprintf(a.Stderr, "warning: log ACL preparation failed: %v\n", err)
	}
	probe, err := logbroker.New(logOpts)
	if err != nil {
		return a.fail(fmt.Errorf("invalid log installation settings"))
	}
	if err = probe.Write(logbroker.Record{Level: "info", Event: "stopped"}); err != nil {
		return a.fail(fmt.Errorf("structured log probe failed"))
	}
	agentArgs := []string{"agent", "--config", abs, "--log-dir", logOpts.Dir, "--log-max-bytes", fmt.Sprint(logOpts.MaxBytes), "--log-files", fmt.Sprint(logOpts.Files)}
	if logOpts.BodySummary {
		agentArgs = append(agentArgs, "--log-body-summary")
	}
	if strings.TrimSpace(*hook) != "" {
		agentArgs = append(agentArgs, "--exec", *hook)
	}
	if *mentionOnly {
		agentArgs = append(agentArgs, "--mention-only")
	}
	if *typing {
		agentArgs = append(agentArgs, "--typing")
	}
	if *startNow {
		agentArgs = append(agentArgs, "--start-now")
	}
	if strings.TrimSpace(*startCutoffs) != "" {
		agentArgs = append(agentArgs, "--start-cutoffs", *startCutoffs)
	}

	if runtime.GOOS == "windows" {
		launcher, err := writeWindowsLauncher(*name, exe, agentArgs)
		if err != nil {
			return a.fail(err)
		}
		tr := `cmd /c "` + launcher + `"`
		cmd := exec.Command("schtasks", "/Create", "/TN", *name, "/SC", "ONLOGON", "/TR", tr, "/F")
		out, err := cmd.CombinedOutput()
		if err == nil && exec.Command("schtasks", "/Query", "/TN", *name).Run() == nil {
			if runOut, runErr := exec.Command("schtasks", "/Run", "/TN", *name).CombinedOutput(); runErr != nil {
				fmt.Fprintf(a.Stderr, "installed scheduled task %s but could not start it: %s\n", *name, decodeConsoleOutput(runOut))
				return 1
			}
			fmt.Fprintf(a.Stdout, "installed and started scheduled task %s at logon (launcher: %s)\n", *name, launcher)
			return 0
		}
		startup, startupErr := installWindowsStartup(*name, launcher)
		if startupErr != nil {
			if err != nil {
				return a.fail(fmt.Errorf("create scheduled task: %w: %s", err, strings.TrimSpace(decodeConsoleOutput(out))))
			}
			return a.fail(fmt.Errorf("Task Scheduler unavailable and Startup install failed: %w", startupErr))
		}
		fmt.Fprintf(a.Stdout, "Task Scheduler unavailable; installed Startup entry %s (launcher: %s)\n", startup, launcher)
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
			return a.fail(fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(decodeConsoleOutput(out))))
		}
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", *name).CombinedOutput(); err != nil {
			return a.fail(fmt.Errorf("systemctl enable: %w: %s", err, strings.TrimSpace(decodeConsoleOutput(out))))
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
		removeErrs := []string{}
		if launcher, pathErr := windowsLauncherPath(*name); pathErr == nil {
			if err := os.Remove(launcher); err != nil && !os.IsNotExist(err) {
				removeErrs = append(removeErrs, fmt.Sprintf("launcher: %v", err))
			}
		}
		if startup, startupErr := windowsStartupPath(*name); startupErr == nil {
			if err := os.Remove(startup); err != nil && !os.IsNotExist(err) {
				removeErrs = append(removeErrs, fmt.Sprintf("startup: %v", err))
			}
		}
		if err != nil && !scheduledTaskMissing(decodeConsoleOutput(out)) {
			return a.fail(fmt.Errorf("delete scheduled task: %w: %s", err, strings.TrimSpace(decodeConsoleOutput(out))))
		}
		if len(removeErrs) > 0 {
			return a.fail(fmt.Errorf("remove bridge files: %s", strings.Join(removeErrs, "; ")))
		}
		fmt.Fprintf(a.Stdout, "removed scheduled task/startup entry %s\n", *name)
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
		if err == nil {
			fmt.Fprint(a.Stdout, decodeConsoleOutput(out))
			return 0
		}
		if startup, startupErr := windowsStartupPath(*name); startupErr == nil {
			if _, statErr := os.Stat(startup); statErr == nil {
				fmt.Fprintf(a.Stdout, "installed Startup entry %s\n", startup)
				return 0
			}
		}
		return a.fail(fmt.Errorf("scheduled task %s is not installed: %s", *name, strings.TrimSpace(decodeConsoleOutput(out))))
	}
	if runtime.GOOS == "linux" {
		out, err := exec.Command("systemctl", "--user", "is-active", *name).CombinedOutput()
		if err != nil {
			return a.fail(fmt.Errorf("systemd user service %s is not active: %s", *name, strings.TrimSpace(decodeConsoleOutput(out))))
		}
		fmt.Fprint(a.Stdout, decodeConsoleOutput(out))
		return 0
	}
	return a.fail(fmt.Errorf("service status is not implemented on %s", runtime.GOOS))
}

func writeWindowsLauncher(name, exe string, args []string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "AgentChat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".cmd")
	line := quoteWindowsCommand(exe) + " " + quoteWindowsArgs(args) + "\r\n"
	content := "@echo off\r\n" + line
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func scheduledTaskMissing(output string) bool {
	lower := strings.ToLower(output)
	for _, marker := range []string{"cannot find", "does not exist", "not found", "找不到", "不存在"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func windowsStartupPath(name string) (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA is not set")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", name+".cmd"), nil
}

func installWindowsStartup(name, launcher string) (string, error) {
	dest, err := windowsStartupPath(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	data, err := os.ReadFile(launcher)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return "", err
	}
	return dest, nil
}

func windowsLauncherPath(name string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "AgentChat", name+".cmd"), nil
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
