//go:build windows

package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
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
		if err := launchElevated(exe, `--config "`+strings.ReplaceAll(cfg, `"`, `\"`)+`"`, dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not launch the elevated Console: %v\n", err)
			fmt.Fprintln(os.Stderr, "Start AgentChat Console from the Start menu; Windows will ask for administrator approval.")
		}
	}
}

var (
	installerShell32           = windows.NewLazySystemDLL("shell32.dll")
	procInstallerShellExecuteW = installerShell32.NewProc("ShellExecuteW")
)

func launchElevated(exe, args, dir string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	work, _ := windows.UTF16PtrFromString(dir)
	result, _, _ := procInstallerShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)), uintptr(unsafe.Pointer(work)), 1)
	if result <= 32 {
		return fmt.Errorf("ShellExecuteW returned %d", result)
	}
	return nil
}

const installMarkerName = ".agentchat-install.json"

type installMarker struct {
	App           string   `json:"app"`
	Root          string   `json:"root"`
	VolumeSerial  uint32   `json:"volume_serial"`
	FileIndexHigh uint32   `json:"file_index_high"`
	FileIndexLow  uint32   `json:"file_index_low"`
	Files         []string `json:"files"`
	ConfigCreated bool     `json:"config_created"`
}

func openDirectoryIdentity(path string) (windows.Handle, uint32, uint32, uint32, error) {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	// Excluding FILE_SHARE_DELETE keeps the verified directory object from being
	// replaced by a junction between identity validation and file removal.
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, 0, 0, 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return 0, 0, 0, 0, errors.New("install path is a reparse point")
	}
	return h, info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, nil
}

func directoryIdentity(path string) (uint32, uint32, uint32, error) {
	h, vol, hi, lo, err := openDirectoryIdentity(path)
	if err != nil {
		return 0, 0, 0, err
	}
	windows.CloseHandle(h)
	return vol, hi, lo, nil
}

func webView2Present() bool {
	for _, root := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "EdgeWebView", "Application"),
	} {
		if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

func ensureWebView2() error {
	if webView2Present() {
		return nil
	}
	data, err := assets.ReadFile("assets/MicrosoftEdgeWebView2RuntimeInstallerX64.exe")
	if err != nil {
		return errors.New("WebView2 is missing and this installer does not contain the offline runtime")
	}
	tmp, err := os.CreateTemp("", "AgentChat-WebView2-*.exe")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	ps := filepath.Join(os.Getenv("WINDIR"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	command := "Start-Process -FilePath '" + strings.ReplaceAll(tmpPath, "'", "''") +
		"' -ArgumentList '/silent','/install' -Verb RunAs -Wait"
	cmd := exec.Command(ps, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("WebView2 installation failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !webView2Present() {
		return errors.New("WebView2 installation completed but runtime was not detected")
	}
	return nil
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
	if err := ensureWebView2(); err != nil {
		return err
	}
	cfgPath := filepath.Join(dir, "desktop.json")
	configCreated := false
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
		configCreated = true
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	vol, hi, lo, err := directoryIdentity(absDir)
	if err != nil {
		return err
	}
	marker := installMarker{
		App: "agentchat-desktop", Root: filepath.Clean(absDir),
		VolumeSerial: vol, FileIndexHigh: hi, FileIndexLow: lo,
		Files: []string{"AgentChatConsole.exe", "agentchat.exe"}, ConfigCreated: configCreated,
	}
	markerData, _ := json.MarshalIndent(marker, "", "  ")
	if err := os.WriteFile(filepath.Join(absDir, installMarkerName), markerData, 0o600); err != nil {
		return err
	}
	if startup {
		started := false
		startupDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
		if err := os.MkdirAll(startupDir, 0o700); err == nil {
			// A .lnk avoids cmd.exe percent/quote expansion in the target path.
			if err := createShortcut(filepath.Join(startupDir, "AgentChat Console.lnk"),
				filepath.Join(absDir, "AgentChatConsole.exe"), "--config \""+cfgPath+"\"", absDir); err == nil {
				started = true
			}
		}
		if !started {
			// Some managed hosts deny the Startup folder. HKCU Run is the
			// per-user fallback and still needs no administrator rights.
			value := "\"" + filepath.Join(absDir, "AgentChatConsole.exe") + "\" --config \"" + cfgPath + "\""
			err := exec.Command("reg.exe", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
				"/v", "AgentChatConsole", "/t", "REG_SZ", "/d", value, "/f").Run()
			if err != nil {
				task := "\"" + filepath.Join(absDir, "AgentChatConsole.exe") + "\" --config \"" + cfgPath + "\""
				if taskErr := exec.Command("schtasks.exe", "/Create", "/TN", "AgentChatConsole",
					"/SC", "ONLOGON", "/TR", task, "/F", "/RL", "LIMITED").Run(); taskErr != nil {
					fmt.Fprintf(os.Stderr, "warning: cannot create Startup entry: %v (task: %v)\n", err, taskErr)
				} else {
					fmt.Println("Installed ONLOGON scheduled task")
				}
			} else {
				fmt.Println("Installed HKCU Run startup entry")
			}
		}
	}
	if err := createStartMenu(absDir, cfgPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot create Start Menu shortcut: %v\n", err)
	}
	fmt.Printf("Installed AgentChat Console to %s\n", absDir)
	return nil
}

func createShortcut(link, target, args, workdir string) error {
	script := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut('" + strings.ReplaceAll(link, "'", "''") +
		"');$s.TargetPath='" + strings.ReplaceAll(target, "'", "''") +
		"';$s.Arguments='" + strings.ReplaceAll(args, "'", "''") +
		"';$s.WorkingDirectory='" + strings.ReplaceAll(workdir, "'", "''") +
		"';$s.IconLocation='" + strings.ReplaceAll(target, "'", "''") + ",0" +
		"';$s.Description='AgentChat Console';$s.Save()"
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

func createStartMenu(dir, cfgPath string) error {
	programs := os.Getenv("APPDATA")
	if programs == "" {
		return errors.New("APPDATA is not set")
	}
	folder := filepath.Join(programs, "Microsoft", "Windows", "Start Menu", "Programs", "AgentChat")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	return createShortcut(filepath.Join(folder, "AgentChat Console.lnk"),
		filepath.Join(dir, "AgentChatConsole.exe"), "--config \""+cfgPath+"\"", dir)
}

func protectedInstallRoot(path string) bool {
	path = strings.ToLower(filepath.Clean(path))
	for _, root := range []string{os.Getenv("USERPROFILE"), os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles"), os.Getenv("WINDIR")} {
		if root == "" {
			continue
		}
		root = strings.ToLower(filepath.Clean(root))
		if path == root || strings.HasPrefix(root, path+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func safeMarkerPath(root, name string) (string, bool) {
	if name == "" || filepath.Base(name) != name {
		return "", false
	}
	return filepath.Join(root, name), true
}

func uninstallDesktop(dir string, removeData bool) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if protectedInstallRoot(abs) || filepath.VolumeName(abs)+string(os.PathSeparator) == abs {
		return fmt.Errorf("refusing to uninstall from protected directory %s", abs)
	}
	data, err := os.ReadFile(filepath.Join(abs, installMarkerName))
	if err != nil {
		return fmt.Errorf("refusing uninstall without ownership marker: %w", err)
	}
	var marker installMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.App != "agentchat-desktop" || !strings.EqualFold(filepath.Clean(marker.Root), abs) {
		return fmt.Errorf("ownership marker does not match %s", abs)
	}
	dirHandle, vol, hi, lo, err := openDirectoryIdentity(abs)
	if err != nil {
		return fmt.Errorf("ownership identity does not match %s", abs)
	}
	defer func() {
		if dirHandle != 0 {
			windows.CloseHandle(dirHandle)
		}
	}()
	if marker.VolumeSerial != 0 || marker.FileIndexHigh != 0 || marker.FileIndexLow != 0 {
		if vol != marker.VolumeSerial || hi != marker.FileIndexHigh || lo != marker.FileIndexLow {
			return fmt.Errorf("ownership identity does not match %s", abs)
		}
	}
	var removeErr error
	remove := func(path string) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			removeErr = errors.Join(removeErr, fmt.Errorf("%s: %w", path, err))
		}
	}
	var owned []string
	for _, name := range marker.Files {
		path, ok := safeMarkerPath(abs, name)
		if !ok {
			return fmt.Errorf("invalid owned file in marker: %q", name)
		}
		if info, err := os.Lstat(path); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to remove reparse/symlink: %s", path)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		owned = append(owned, path)
	}
	if err := removeStartupEntries(); err != nil {
		return err
	}
	for _, path := range owned {
		remove(path)
	}
	if removeData && marker.ConfigCreated {
		remove(filepath.Join(abs, "desktop.json"))
	}
	if removeData {
		if removeErr != nil {
			return removeErr
		}
		remove(filepath.Join(abs, installMarkerName))
		if removeErr != nil {
			return removeErr
		}
		windows.CloseHandle(dirHandle)
		dirHandle = 0
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Kept %s because it contains unknown files\n", abs)
		}
		return nil
	}
	if removeErr != nil {
		return removeErr
	}
	fmt.Printf("Removed AgentChat binaries from %s; configuration kept\n", abs)
	return nil
}

func removeStartupEntries() error {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil
	}
	var errs error
	startup := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	for _, name := range []string{"AgentChat Console.lnk", "AgentChat Console.cmd"} {
		if err := os.Remove(filepath.Join(startup, name)); err != nil && !os.IsNotExist(err) {
			errs = errors.Join(errs, err)
		}
	}
	menu := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "AgentChat", "AgentChat Console.lnk")
	if err := os.Remove(menu); err != nil && !os.IsNotExist(err) {
		errs = errors.Join(errs, err)
	}
	runKey := `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	queryErr := exec.Command("reg.exe", "query", runKey, "/v", "AgentChatConsole").Run()
	if queryErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(queryErr, &exitErr) || exitErr.ExitCode() != 1 {
			errs = errors.Join(errs, fmt.Errorf("inspect HKCU startup entry: %w", queryErr))
		}
	} else if err := exec.Command("reg.exe", "delete", runKey, "/v", "AgentChatConsole", "/f").Run(); err != nil {
		errs = errors.Join(errs, fmt.Errorf("remove HKCU startup entry: %w", err))
	}
	if err := exec.Command("schtasks.exe", "/Query", "/TN", "AgentChatConsole").Run(); err == nil {
		if err := exec.Command("schtasks.exe", "/Delete", "/TN", "AgentChatConsole", "/F").Run(); err != nil {
			errs = errors.Join(errs, fmt.Errorf("remove scheduled task: %w", err))
		}
	}
	return errs
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "agentchat installer:", err)
	os.Exit(1)
}
