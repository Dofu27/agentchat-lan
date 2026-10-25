//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	hubpkg "github.com/Dofu27/agentchat-lan/internal/hub"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	agentconfig "github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type config struct {
	Service       string            `json:"service"`
	HubURL        string            `json:"hub_url"`
	TokenFile     string            `json:"token_file"`
	CAFile        string            `json:"tls_ca_file,omitempty"`
	Title         string            `json:"title,omitempty"`
	Width         int               `json:"width,omitempty"`
	Height        int               `json:"height,omitempty"`
	LogFiles      map[string]string `json:"log_files,omitempty"`
	AgentchatPath string            `json:"agentchat_path,omitempty"`
	AgentConfig   string            `json:"agent_config,omitempty"`
	BridgeName    string            `json:"bridge_name,omitempty"`
	BridgeExec    string            `json:"bridge_exec,omitempty"`
	Connector     string            `json:"connector,omitempty"`
	Role          string            `json:"role,omitempty"`
	SetupDone     bool              `json:"setup_done,omitempty"`
	HubData       string            `json:"hub_data,omitempty"`
	HubListen     string            `json:"hub_listen,omitempty"`
	HubCert       string            `json:"hub_tls_cert,omitempty"`
	HubKey        string            `json:"hub_tls_key,omitempty"`
}

type app struct {
	cfg           config
	configPath    string
	token         string
	cap           string
	http          *http.Client
	logs          map[string]string
	mu            sync.RWMutex
	setupMu       sync.Mutex
	configVersion uint64
}

var instanceMutex windows.Handle

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "--uninstall-worker" {
		os.Exit(runUninstallWorker(os.Args[2:]))
	}
	replaceInstance := false
	for _, arg := range os.Args[1:] {
		if arg == "--replace-instance" {
			replaceInstance = true
		}
	}
	mutexName, _ := windows.UTF16PtrFromString(`Local\AgentChatConsole`)
	instanceMutex, _ = windows.CreateMutex(nil, false, mutexName)
	if !replaceInstance {
		if instanceMutex != 0 && windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
			if !activateExistingWindow() {
				notifyAlreadyRunning()
			}
			return
		}
		// Even if the mutex check races, never create a second Console window.
		if activateExistingWindow() {
			return
		}
	}
	var cfgPath string
	var service string
	var hub string
	var tokenFile string
	var headless bool
	var listen string
	flag.StringVar(&cfgPath, "config", "", "desktop config JSON")
	flag.BoolVar(&headless, "headless", false, "serve the local console API without opening a window")
	flag.BoolVar(&replaceInstance, "replace-instance", false, "replace an existing console window during an elevation handoff")
	flag.StringVar(&listen, "listen", "127.0.0.1:0", "local console listen address")
	flag.StringVar(&service, "service", "AgentChatFedB", "Windows service name")
	flag.StringVar(&hub, "hub", "https://agentchat.lan:8788", "hub base URL")
	flag.StringVar(&tokenFile, "token-file", "", "admin token file")
	flag.Parse()

	cfg := config{Service: service, HubURL: strings.TrimRight(hub, "/"), TokenFile: tokenFile, Title: "AgentChat Console"}
	if cfgPath == "" {
		dir, err := os.UserConfigDir()
		if err == nil {
			cfgPath = filepath.Join(dir, "AgentChat", "desktop.json")
		}
	}
	if cfgPath != "" {
		if data, err := os.ReadFile(cfgPath); err == nil {
			data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
			if err := json.Unmarshal(data, &cfg); err != nil {
				log.Fatalf("desktop config: %v", err)
			}
		}
		if f, err := os.OpenFile(filepath.Join(filepath.Dir(cfgPath), "console.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			log.SetOutput(f)
			defer f.Close()
		}
	}
	if cfg.Service == "" {
		cfg.Service = "AgentChatFedB"
	}
	if cfg.HubURL == "" {
		cfg.HubURL = "https://agentchat.lan:8788"
	}
	cfg.HubURL = strings.TrimRight(cfg.HubURL, "/")
	if cfg.Title == "" {
		cfg.Title = "AgentChat Console"
	}
	if cfg.Width < 800 {
		cfg.Width = 1100
	}
	if cfg.Height < 600 {
		cfg.Height = 780
	}
	if cfg.BridgeName == "" {
		cfg.BridgeName = "AgentChatBridge"
	}
	if cfg.Role == "" {
		cfg.Role = "hub"
	}
	switch cfg.Role {
	case "hub", "client", "bridge", "observer":
	default:
		log.Fatalf("desktop role must be hub, client, bridge, or observer")
	}
	if cfg.AgentConfig != "" && !filepath.IsAbs(cfg.AgentConfig) && cfgPath != "" {
		cfg.AgentConfig = filepath.Join(filepath.Dir(cfgPath), cfg.AgentConfig)
	}
	if cfg.BridgeExec != "" && !filepath.IsAbs(cfg.BridgeExec) && cfgPath != "" {
		cfg.BridgeExec = filepath.Join(filepath.Dir(cfgPath), cfg.BridgeExec)
	}
	if cfg.AgentchatPath == "" {
		cfg.AgentchatPath = bundledCLIPath()
	} else if !filepath.IsAbs(cfg.AgentchatPath) && cfgPath != "" {
		cfg.AgentchatPath = filepath.Join(filepath.Dir(cfgPath), cfg.AgentchatPath)
	}
	hubURL, err := url.Parse(cfg.HubURL)
	if err != nil || hubURL.Host == "" || (hubURL.Scheme != "https" && hubURL.Scheme != "http") {
		log.Fatalf("hub_url must be an http(s) URL")
	}
	if hubURL.Scheme != "https" && !isLoopbackHostName(hubURL.Hostname()) {
		log.Fatalf("hub_url must use https outside loopback")
	}
	client, err := newHubClient(cfg, cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	token := ""
	if cfg.TokenFile != "" {
		tokenFile := cfg.TokenFile
		if !filepath.IsAbs(tokenFile) && cfgPath != "" {
			tokenFile = filepath.Join(filepath.Dir(cfgPath), tokenFile)
		}
		if data, err := os.ReadFile(tokenFile); err == nil {
			token = strings.TrimSpace(string(data))
		}
	}
	capability, err := newCapability()
	if err != nil {
		log.Fatal(err)
	}
	logDir := "."
	if cfgPath != "" {
		logDir = filepath.Dir(cfgPath)
	}
	logs := map[string]string{
		"desktop": filepath.Join(logDir, "console.log"),
		"hub":     filepath.Join(logDir, "hub.log"),
		"bridge":  filepath.Join(logDir, "bridge.log"),
	}
	for name, p := range cfg.LogFiles {
		if !filepath.IsAbs(p) {
			p = filepath.Join(logDir, p)
		}
		logs[name] = p
	}
	a := &app{cfg: cfg, configPath: cfgPath, token: token, cap: capability, http: client, logs: logs}

	ln, srv, err := a.startServer(listen)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("desktop server: %v", err)
		}
	}()
	if headless {
		fmt.Printf("url=http://%s/\ncapability=%s\n", ln.Addr().String(), a.cap)
		select {}
	}

	fitWindowSize(&cfg)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title: cfg.Title, Width: uint(cfg.Width), Height: uint(cfg.Height), Center: true,
		},
	})
	if w == nil {
		log.Fatal("WebView2 runtime is not available; install Microsoft Edge WebView2 Runtime")
	}
	defer w.Destroy()
	hwnd := uintptr(w.Window())
	makeFrameless(hwnd)
	registerTaskbarCreated()
	ensureTrayIcon(hwnd)
	closeDialogEval = func() { w.Eval("window.__agentchatAskClose && window.__agentchatAskClose()") }
	origin := "http://" + ln.Addr().String()
	w.Init("if (window.top === window && location.origin === " + strconv.Quote(origin) +
		") { window.__agentchatDesktopCapability = " + strconv.Quote(a.cap) + "; }")
	_ = w.Bind("__winDrag", func() {
		releaseCapture()
		sendMessage(uintptr(w.Window()), 0x00A1, 2, 0) // WM_NCLBUTTONDOWN, HTCAPTION
	})
	_ = w.Bind("__winMin", func() { showWindow(uintptr(w.Window()), 6) })
	_ = w.Bind("__winMax", func() {
		if isZoomed(uintptr(w.Window())) {
			showWindow(uintptr(w.Window()), 9)
		} else {
			showWindow(uintptr(w.Window()), 3)
		}
	})
	_ = w.Bind("__winClose", func() { sendMessage(hwnd, wmClose, 0, 0) })
	_ = w.Bind("__winCloseChoice", func(choice string) { closeChoice(hwnd, choice) })
	_ = w.Bind("__winForceClose", func() {
		allowClose.Store(true)
		removeTrayIcon(hwnd)
		sendMessage(hwnd, wmClose, 0, 0)
	})
	_ = w.Bind("__winPickPath", func(kind, title, filter, initial string) string {
		systemDir, err := windows.GetSystemDirectory()
		if err != nil {
			return ""
		}
		ps := filepath.Join(systemDir, "WindowsPowerShell", "v1.0", "powershell.exe")
		script := `Add-Type -AssemblyName System.Windows.Forms
if ($env:ACPICK_KIND -eq "folder") {
  $d = New-Object System.Windows.Forms.FolderBrowserDialog
  $d.Description = $env:ACPICK_TITLE
  if ($env:ACPICK_INITIAL) { $d.SelectedPath = $env:ACPICK_INITIAL }
  if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) }
} else {
  if ($env:ACPICK_KIND -eq "save") { $d = New-Object System.Windows.Forms.SaveFileDialog }
  else { $d = New-Object System.Windows.Forms.OpenFileDialog }
  $d.Title = $env:ACPICK_TITLE
  if ($env:ACPICK_FILTER) { $d.Filter = $env:ACPICK_FILTER }
  if ($env:ACPICK_INITIAL) { $d.InitialDirectory = $env:ACPICK_INITIAL }
  if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.FileName) }
}`
		cmd := exec.Command(ps, "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", script)
		cmd.Env = append(os.Environ(),
			"ACPICK_KIND="+kind,
			"ACPICK_TITLE="+title,
			"ACPICK_FILTER="+filter,
			"ACPICK_INITIAL="+initial,
		)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	})
	_ = w.Bind("__winElevate", func() bool {
		exe, err := os.Executable()
		if err != nil {
			return false
		}
		verb, _ := windows.UTF16PtrFromString("runas")
		exePtr, _ := windows.UTF16PtrFromString(exe)
		argsPtr, _ := windows.UTF16PtrFromString("--config " + quoteWindowsArg(a.configPath) + " --replace-instance")
		result, _, _ := procShellExecuteW.Call(hwnd, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(exePtr)), uintptr(unsafe.Pointer(argsPtr)), 0, 1)
		if result <= 32 {
			return false
		}
		allowClose.Store(true)
		removeTrayIcon(hwnd)
		sendMessage(hwnd, wmClose, 0, 0)
		return true
	})
	w.Navigate(origin + "/")
	w.Run()
}

var (
	user32DLL                = windows.NewLazySystemDLL("user32.dll")
	shell32DLL               = windows.NewLazySystemDLL("shell32.dll")
	procReleaseCapture       = user32DLL.NewProc("ReleaseCapture")
	procSendMessageW         = user32DLL.NewProc("SendMessageW")
	procPostMessageW         = user32DLL.NewProc("PostMessageW")
	procShowWindow           = user32DLL.NewProc("ShowWindow")
	procIsZoomed             = user32DLL.NewProc("IsZoomed")
	procGetWindowLong        = user32DLL.NewProc("GetWindowLongPtrW")
	procSetWindowLong        = user32DLL.NewProc("SetWindowLongPtrW")
	procSetWindowPos         = user32DLL.NewProc("SetWindowPos")
	procGetWindowRect        = user32DLL.NewProc("GetWindowRect")
	procCallWindowProc       = user32DLL.NewProc("CallWindowProcW")
	procSystemParametersInfo = user32DLL.NewProc("SystemParametersInfoW")
	procLoadIconW            = user32DLL.NewProc("LoadIconW")
	procGetCursorPos         = user32DLL.NewProc("GetCursorPos")
	procCreatePopupMenu      = user32DLL.NewProc("CreatePopupMenu")
	procAppendMenuW          = user32DLL.NewProc("AppendMenuW")
	procTrackPopupMenu       = user32DLL.NewProc("TrackPopupMenu")
	procDestroyMenu          = user32DLL.NewProc("DestroyMenu")
	procSetForegroundWindow  = user32DLL.NewProc("SetForegroundWindow")
	procBringWindowToTop     = user32DLL.NewProc("BringWindowToTop")
	procEnumWindows          = user32DLL.NewProc("EnumWindows")
	procGetWindowTextW       = user32DLL.NewProc("GetWindowTextW")
	procMessageBoxW          = user32DLL.NewProc("MessageBoxW")
	procRegisterWindowMessageW = user32DLL.NewProc("RegisterWindowMessageW")
	procShellNotifyIconW     = shell32DLL.NewProc("Shell_NotifyIconW")
	procShellExecuteW        = shell32DLL.NewProc("ShellExecuteW")
	procExtractIconExW       = shell32DLL.NewProc("ExtractIconExW")
	dwmapiDLL                = windows.NewLazySystemDLL("dwmapi.dll")
	procGetLongPathNameW     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetLongPathNameW")
	secureKernel32DLL        = windows.NewLazySystemDLL("kernel32.dll")
	procCreateDirectoryW     = secureKernel32DLL.NewProc("CreateDirectoryW")
	procCreateFileSecureW    = secureKernel32DLL.NewProc("CreateFileW")
	procDwmSetAttribute      = dwmapiDLL.NewProc("DwmSetWindowAttribute")
	uxthemeDLL               = windows.NewLazySystemDLL("uxtheme.dll")
	procSetWindowTheme       = uxthemeDLL.NewProc("SetWindowTheme")
)

const (
	wmClose      = 0x0010
	wmDestroy    = 0x0002
	wmNull       = 0x0000
	wmTray       = 0x8001
	trayShowID   = 1001
	trayExitID   = 1002
	swHide       = 0
	swMinimize   = 6
	swRestore    = 9
)

type trayGuid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         trayGuid
	HBalloonIcon     uintptr
}

var (
	trayIconAdded    bool
	taskbarCreatedMsg uint32
	closeDialogEval  func()
	allowClose       atomic.Bool
)

func registerTaskbarCreated() {
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	msg, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))
	taskbarCreatedMsg = uint32(msg)
}

func showTrayError(hwnd uintptr) {
	title, _ := windows.UTF16PtrFromString("AgentChat Console")
	text, _ := windows.UTF16PtrFromString("托盘图标创建失败，窗口保持打开。")
	_, _, _ = procMessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}

var activationHWND uintptr

var enumWindowsProc = syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
	var buf [256]uint16
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 0 && strings.Contains(strings.TrimSpace(windows.UTF16ToString(buf[:n])), "AgentChat Console") {
		activationHWND = hwnd
		return 0
	}
	return 1
})

func activateExistingWindow() bool {
	activationHWND = 0
	_, _, _ = procEnumWindows.Call(enumWindowsProc, 0)
	if activationHWND != 0 {
		showMainWindow(activationHWND)
		return true
	}
	return false
}

func notifyAlreadyRunning() {
	title, _ := windows.UTF16PtrFromString("AgentChat Console")
	text, _ := windows.UTF16PtrFromString("AgentChat Console 已在运行。请查看任务栏或通知区域。")
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x40)
}

func ensureTrayIcon(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if trayIconAdded {
		return true
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	nid.UFlags = 1 | 2 | 4 // NIF_MESSAGE | NIF_ICON | NIF_TIP
	nid.UCallbackMessage = wmTray
	icon := uintptr(0)
	if exe, err := os.Executable(); err == nil {
		if p, err := windows.UTF16PtrFromString(exe); err == nil {
			var large, small uintptr
			count, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(p)), 0,
				uintptr(unsafe.Pointer(&large)), uintptr(unsafe.Pointer(&small)), 1)
			if count > 0 && small != 0 {
				icon = small
			}
		}
	}
	if icon == 0 {
		icon, _, _ = procLoadIconW.Call(0, 32512) // IDI_APPLICATION
	}
	nid.HIcon = icon
	tip, _ := windows.UTF16FromString("AgentChat Console")
	copy(nid.SzTip[:], tip)
	if ok, _, _ := procShellNotifyIconW.Call(0, uintptr(unsafe.Pointer(&nid))); ok != 0 {
		trayIconAdded = true
		return true
	}
	trayIconAdded = false
	return false
}

func removeTrayIcon(hwnd uintptr) {
	if !trayIconAdded {
		return
	}
	var nid notifyIconData
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = hwnd
	nid.UID = 1
	_, _, _ = procShellNotifyIconW.Call(2, uintptr(unsafe.Pointer(&nid))) // NIM_DELETE
	trayIconAdded = false
}

func showMainWindow(hwnd uintptr) {
	showWindow(hwnd, swRestore)
	_, _, _ = procBringWindowToTop.Call(hwnd)
	const swpNoMoveNoSizeShow = 0x0001 | 0x0002 | 0x0040
	_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMoveNoSizeShow)
	_, _, _ = procSetForegroundWindow.Call(hwnd)
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	show, _ := windows.UTF16PtrFromString("显示 AgentChat Console")
	quit, _ := windows.UTF16PtrFromString("退出")
	_, _, _ = procAppendMenuW.Call(menu, 0, trayShowID, uintptr(unsafe.Pointer(show)))
	_, _, _ = procAppendMenuW.Call(menu, 0, trayExitID, uintptr(unsafe.Pointer(quit)))
	var pt winPoint
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	_, _, _ = procSetForegroundWindow.Call(hwnd)
	const tpmReturnCmd = 0x0100
	const tpmRightButton = 0x0002
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	_, _, _ = procPostMessageW.Call(hwnd, wmNull, 0, 0)
	switch int(cmd) {
	case trayShowID:
		showMainWindow(hwnd)
	case trayExitID:
		allowClose.Store(true)
		sendMessage(hwnd, wmClose, 0, 0)
	}
}

func closeChoice(hwnd uintptr, choice string) {
	switch choice {
	case "minimize":
		showWindow(hwnd, swMinimize)
	case "tray":
		if !ensureTrayIcon(hwnd) {
			showMainWindow(hwnd)
			showTrayError(hwnd)
			return
		}
		showWindow(hwnd, swHide)
	case "quit":
		allowClose.Store(true)
		removeTrayIcon(hwnd)
		sendMessage(hwnd, wmClose, 0, 0)
	}
}

type winRect struct {
	Left, Top, Right, Bottom int32
}

func fitWindowSize(cfg *config) {
	var r winRect
	const spiGetWorkArea = 0x0030
	if ok, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&r)), 0); ok == 0 {
		return
	}
	workW := int(r.Right - r.Left)
	workH := int(r.Bottom - r.Top)
	if workW < 900 || workH < 600 {
		return
	}
	maxW := workW * 92 / 100
	maxH := workH * 88 / 100
	if cfg.Width > maxW {
		cfg.Width = maxW
	}
	if cfg.Height > maxH {
		cfg.Height = maxH
	}
	if cfg.Width < 900 {
		cfg.Width = 900
	}
	if cfg.Height < 600 {
		cfg.Height = 600
	}
}

type winPoint struct {
	X, Y int32
}

type minMaxInfo struct {
	Reserved     winPoint
	MaxSize      winPoint
	MaxPosition  winPoint
	MinTrackSize winPoint
	MaxTrackSize winPoint
}

var minSizeOldProcs sync.Map

func minSizeProc(hwnd, msg, wp uintptr, lp *minMaxInfo) uintptr {
	const wmGetMinMaxInfo = 0x0024
	const wmTrayMouseUp = 0x0202
	const wmTrayMouseDouble = 0x0203
	const wmTrayMouseRightUp = 0x0205
	if msg == wmGetMinMaxInfo && lp != nil {
		lp.MinTrackSize.X = 900
		lp.MinTrackSize.Y = 600
		return 0
	}
	if taskbarCreatedMsg != 0 && msg == uintptr(taskbarCreatedMsg) {
		trayIconAdded = false
		if !ensureTrayIcon(hwnd) {
			showMainWindow(hwnd)
			showTrayError(hwnd)
		}
		return 0
	}
	switch msg {
	case wmClose:
		if !allowClose.Load() {
			if closeDialogEval != nil {
				closeDialogEval()
			}
			return 0
		}
	case wmTray:
		switch uintptr(unsafe.Pointer(lp)) {
		case wmTrayMouseUp, wmTrayMouseDouble:
			showMainWindow(hwnd)
		case wmTrayMouseRightUp:
			showTrayMenu(hwnd)
		}
		return 0
	case wmDestroy:
		removeTrayIcon(hwnd)
	}
	if old, ok := minSizeOldProcs.Load(hwnd); ok {
		r, _, _ := procCallWindowProc.Call(old.(uintptr), hwnd, msg, wp, uintptr(unsafe.Pointer(lp)))
		return r
	}
	return 0
}

var minSizeWndProc = syscall.NewCallback(minSizeProc)

func installMinSize(hwnd uintptr) {
	if _, exists := minSizeOldProcs.Load(hwnd); exists {
		return
	}
	const gwlWndProc = 0xFFFFFFFFFFFFFFFC
	old, _, _ := procSetWindowLong.Call(hwnd, gwlWndProc, minSizeWndProc)
	if old != 0 {
		minSizeOldProcs.Store(hwnd, old)
	}
}

func makeFrameless(hwnd uintptr) {
	const (
		gwlStyle        = 0xFFFFFFFFFFFFFFF0
		wsCaption       = 0x00C00000
		wsThickFrame    = 0x00040000
		wsPopup         = 0x80000000
		wsMinimizeBox   = 0x00020000
		wsMaximizeBox   = 0x00010000
		wsSysMenu       = 0x00080000
		swpNoSize       = 0x0001
		swpNoMove       = 0x0002
		swpNoZOrder     = 0x0004
		swpFrameChanged = 0x0020
	)
	style, _, _ := procGetWindowLong.Call(hwnd, uintptr(gwlStyle))
	style &^= wsCaption
	style |= wsPopup | wsThickFrame | wsMinimizeBox | wsMaximizeBox | wsSysMenu
	_, _, _ = procSetWindowLong.Call(hwnd, uintptr(gwlStyle), style)
	_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoZOrder|swpFrameChanged)
	dark := int32(1)
	_, _, _ = procDwmSetAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&dark)), 4)   // DWMWA_USE_IMMERSIVE_DARK_MODE
	border := uint32(0x00170F0A)                                                      // dark COLORREF
	_, _, _ = procDwmSetAttribute.Call(hwnd, 34, uintptr(unsafe.Pointer(&border)), 4) // DWMWA_BORDER_COLOR
	theme, _ := windows.UTF16PtrFromString("DarkMode_Explorer")
	_, _, _ = procSetWindowTheme.Call(hwnd, uintptr(unsafe.Pointer(theme)), 0)
	installMinSize(hwnd)
}

func releaseCapture() { _, _, _ = procReleaseCapture.Call() }
func sendMessage(hwnd, msg, wparam, lparam uintptr) {
	_, _, _ = procSendMessageW.Call(hwnd, msg, wparam, lparam)
}
func showWindow(hwnd uintptr, cmd int32) {
	_, _, _ = procShowWindow.Call(hwnd, uintptr(cmd))
}
func isZoomed(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

func newCapability() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newHubClient(cfg config, cfgPath string) (*http.Client, error) {
	caFile := cfg.CAFile
	if caFile != "" && !filepath.IsAbs(caFile) && cfgPath != "" {
		caFile = filepath.Join(filepath.Dir(cfgPath), caFile)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read hub CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("hub CA contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	return &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("hub redirect refused")
		},
	}, nil
}

func isLoopbackHostName(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateLoopbackListen(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	if !isLoopbackHostName(host) {
		return fmt.Errorf("desktop console may only bind a loopback address")
	}
	return nil
}

func (a *app) hostAllowed(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return false
	}
	return isLoopbackHostName(host)
}

func (a *app) api(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.hostAllowed(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "untrusted host"})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "untrusted origin"})
			return
		}
		if r.Method == http.MethodPost {
			mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
			multipartUpload := r.URL.Path == "/api/chat/upload" && mediaType == "multipart/form-data"
			if mediaType != "application/json" && !multipartUpload {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "application/json required"})
				return
			}
		}
		got := strings.TrimSpace(r.Header.Get("X-AgentChat-Desktop-Capability"))
		if a.cap == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.cap)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "desktop capability required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type agentConfigFile struct {
	HubURL    string `json:"hub_url"`
	AgentName string `json:"agent_name"`
	AgentID   string `json:"agent_id"`
	Token     string `json:"token"`
}

func (a *app) validateAgentConfigFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg agentConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return errors.New("agent config is not valid JSON")
	}
	if strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.AgentID) == "" {
		return errors.New("agent config has no token or agent_id")
	}
	desktopOrigin, err := canonicalOrigin(a.cfg.HubURL)
	if err != nil {
		return err
	}
	agentOrigin, err := canonicalOrigin(cfg.HubURL)
	if err != nil || agentOrigin != desktopOrigin {
		return errors.New("agent config hub_url does not match the desktop hub")
	}
	return nil
}

func (a *app) loadAgentConfig() (agentConfigFile, error) {
	if strings.TrimSpace(a.cfg.AgentConfig) == "" {
		return agentConfigFile{}, errors.New("agent_config is not configured")
	}
	data, err := os.ReadFile(a.cfg.AgentConfig)
	if err != nil {
		return agentConfigFile{}, err
	}
	var cfg agentConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return agentConfigFile{}, err
	}
	if strings.TrimSpace(cfg.Token) == "" || strings.TrimSpace(cfg.AgentID) == "" {
		return agentConfigFile{}, errors.New("agent config has no token or agent_id")
	}
	desktopOrigin, err := canonicalOrigin(a.cfg.HubURL)
	if err != nil {
		return agentConfigFile{}, err
	}
	agentOrigin, err := canonicalOrigin(cfg.HubURL)
	if err != nil || agentOrigin != desktopOrigin {
		return agentConfigFile{}, errors.New("agent config hub_url does not match the desktop hub")
	}
	return cfg, nil
}

func canonicalOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return "", errors.New("invalid hub origin")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || strings.ContainsAny(host, "%") {
		return "", errors.New("non-canonical hub host")
	}
	for _, r := range host {
		if r > 127 {
			return "", errors.New("non-ASCII hub host must be configured as punycode")
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

func (a *app) startServer(listen string) (net.Listener, *http.Server, error) {
	if err := validateLoopbackListen(listen); err != nil {
		return nil, nil, err
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, nil, err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		ln.Close()
		return nil, nil, fmt.Errorf("desktop console bound a non-loopback address")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if !a.hostAllowed(r.Host) {
			http.Error(w, "untrusted host", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, desktopHTML)
	})
	mux.Handle("GET /api/status", a.api(http.HandlerFunc(a.handleStatus)))
	mux.Handle("GET /api/network", a.api(http.HandlerFunc(a.handleNetwork)))
	mux.Handle("POST /api/service", a.api(http.HandlerFunc(a.handleService)))
	mux.Handle("GET /api/logs", a.api(http.HandlerFunc(a.handleLogs)))
	mux.Handle("GET /api/bridge", a.api(http.HandlerFunc(a.handleBridge)))
	mux.Handle("POST /api/bridge", a.api(http.HandlerFunc(a.handleBridge)))
	mux.Handle("GET /api/connectors", a.api(http.HandlerFunc(a.handleConnectors)))
	mux.Handle("POST /api/connectors", a.api(http.HandlerFunc(a.handleConnectors)))
	mux.Handle("GET /api/chat", a.api(http.HandlerFunc(a.handleChat)))
	mux.Handle("GET /api/chat/rooms", a.api(http.HandlerFunc(a.handleChatRooms)))
	mux.Handle("GET /api/chat/messages", a.api(http.HandlerFunc(a.handleChatMessages)))
	mux.Handle("GET /api/chat/agents", a.api(http.HandlerFunc(a.handleChatAgents)))
	mux.Handle("POST /api/chat/dm", a.api(http.HandlerFunc(a.handleChatDM)))
	mux.Handle("GET /api/chat/search", a.api(http.HandlerFunc(a.handleChatSearch)))
	mux.Handle("POST /api/chat/send", a.api(http.HandlerFunc(a.handleChatSend)))
	mux.Handle("POST /api/chat/upload", a.api(http.HandlerFunc(a.handleChatUpload)))
	mux.Handle("GET /api/chat/files/{id}", a.api(http.HandlerFunc(a.handleChatFile)))
	mux.Handle("GET /api/admin/agents", a.api(http.HandlerFunc(a.handleAdminAgents)))
	mux.Handle("POST /api/admin/agents", a.api(http.HandlerFunc(a.handleAdminCreateAgent)))
	mux.Handle("DELETE /api/admin/agents/{id}", a.api(http.HandlerFunc(a.handleAdminRevokeAgent)))
	mux.Handle("POST /api/admin/agents/{id}/token", a.api(http.HandlerFunc(a.handleAdminRotateAgent)))
	mux.Handle("POST /api/admin/rooms", a.api(http.HandlerFunc(a.handleAdminCreateRoom)))
	mux.Handle("DELETE /api/admin/rooms/{id}", a.api(http.HandlerFunc(a.handleAdminArchiveRoom)))
	mux.Handle("GET /api/admin/invites", a.api(http.HandlerFunc(a.handleAdminInvites)))
	mux.Handle("POST /api/admin/invites", a.api(http.HandlerFunc(a.handleAdminInvites)))
	mux.Handle("GET /api/admin/audit", a.api(http.HandlerFunc(a.handleAdminAudit)))
	mux.Handle("GET /api/admin/federation", a.api(http.HandlerFunc(a.handleAdminFederation)))
	mux.Handle("POST /api/client/join", a.api(http.HandlerFunc(a.handleJoin)))
	mux.Handle("GET /api/settings", a.api(http.HandlerFunc(a.handleSettings)))
	mux.Handle("POST /api/settings", a.api(http.HandlerFunc(a.handleSettings)))
	mux.Handle("POST /api/uninstall", a.api(http.HandlerFunc(a.handleUninstall)))
	mux.Handle("POST /api/hub/init", a.api(http.HandlerFunc(a.handleHubInit)))
	mux.Handle("POST /api/hub/clear-data", a.api(http.HandlerFunc(a.handleHubClearData)))
	mux.Handle("POST /api/hub/service", a.api(http.HandlerFunc(a.handleHubService)))
	mux.Handle("POST /api/hub/firewall", a.api(http.HandlerFunc(a.handleHubFirewall)))
	mux.Handle("POST /api/hub/tls", a.api(http.HandlerFunc(a.handleHubTLS)))
	mux.Handle("GET /api/federation/outbox", a.api(http.HandlerFunc(a.handleOutbox)))
	mux.Handle("POST /api/federation/retry", a.api(http.HandlerFunc(a.handleRetry)))
	return ln, &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, nil
}

type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	original := len(p)
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	return original, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	raw := b.buf.String()
	redacted := redactOutput(raw)
	if b.truncated {
		return redacted + "\n[output truncated]"
	}
	return redacted
}

func bridgeResult(cfg config, action string) map[string]any {
	result := map[string]any{
		"action":       action,
		"name":         cfg.BridgeName,
		"agent_config": cfg.AgentConfig,
		"agentchat":    cfg.AgentchatPath,
		"bridge_exec":  cfg.BridgeExec,
	}
	if cfg.AgentchatPath == "" {
		result["ok"] = false
		result["error"] = "agentchat_path is not configured"
		return result
	}
	args := []string{"bridge", action, "--name", cfg.BridgeName}
	if action == "install" {
		if cfg.AgentConfig == "" {
			result["ok"] = false
			result["error"] = "agent_config is not configured"
			return result
		}
		args = []string{"bridge", "install", "--config", cfg.AgentConfig, "--name", cfg.BridgeName, "--mention-only", "--typing", "--start-now"}
		if cfg.BridgeExec != "" {
			args = append(args, "--exec", cfg.BridgeExec)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	protected, err := ensureProtectedServiceBinary(resolvedCLIPath(cfg.AgentchatPath))
	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
		return result
	}
	out, err := runBoundedCommand(ctx, protected, args, 16<<10)
	result["output"] = redactOutput(strings.TrimSpace(out))
	result["ok"] = err == nil
	if err != nil {
		result["error"] = redactOutput(err.Error())
	}
	return result
}

type connectorProbe struct {
	Found  bool   `json:"found"`
	Path   string `json:"path,omitempty"`
	Config string `json:"config,omitempty"`
	Script string `json:"script,omitempty"`
	Home   string `json:"home,omitempty"`
	Note   string `json:"note,omitempty"`
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func codexProbe() connectorProbe {
	var out connectorProbe
	home, _ := os.UserHomeDir()
	if p, err := exec.LookPath("codex"); err == nil {
		out.Path = p
	}
	if out.Path == "" {
		out.Path = firstExisting(
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "OpenAI", "Codex", "bin", "codex.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "OpenAI", "Codex", "bin", "codex.exe"),
		)
	}
	configDir := os.Getenv("CODEX_HOME")
	if configDir == "" {
		configDir = filepath.Join(home, ".codex")
	}
	out.Config = filepath.Join(configDir, "config.toml")
	out.Found = out.Path != "" || firstExisting(out.Config) != ""
	return out
}

func dshProbe() connectorProbe {
	var out connectorProbe
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(os.Getenv("DSH_HOME"), "DSH Desktop.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "DSH", "DSH Desktop.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "DSH", "DSH Desktop.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "DSH", "DSH Desktop.exe"),
		filepath.Join(home, "DSH", "DSH Desktop.exe"),
	}
	out.Path = firstExisting(candidates...)
	if out.Path != "" {
		out.Script = filepath.Join(filepath.Dir(out.Path), "resources", "app", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
	}
	out.Home = os.Getenv("DSH_HOME")
	if out.Home == "" {
		out.Home = filepath.Join(home, ".dsh")
	}
	out.Found = out.Path != ""
	return out
}

func codexMCPBlock(agentchat, agentConfig string) string {
	return "[mcp_servers.agentchat]\n" +
		"command = " + strconv.Quote(agentchat) + "\n" +
		"args = [\"mcp\", \"--config\", " + strconv.Quote(agentConfig) + "]\n"
}

func writeCodexMCP(path, block string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("Codex config.toml path is empty")
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if strings.Contains(string(existing), "[mcp_servers.agentchat]") {
		return "", errors.New("Codex already has [mcp_servers.agentchat]; not overwriting")
	}
	backup := ""
	if len(existing) > 0 {
		backup = path + ".bak-agentchat-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, existing, 0o600); err != nil {
			return "", fmt.Errorf("backup Codex config: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		b.WriteByte('\n')
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(block)
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return backup, err
	}
	return backup, nil
}

func quoteCmdArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func dshBridgeExec(agentchat, agentConfig, dsh, script, dshHome, workdir string) string {
	args := []string{quoteCmdArg(agentchat), "dsh-bridge", "--config", quoteCmdArg(agentConfig),
		"--dsh", quoteCmdArg(dsh), "--dsh-script", quoteCmdArg(script),
		"--dsh-home", quoteCmdArg(dshHome), "--workdir", quoteCmdArg(workdir), "--timeout", "300"}
	return strings.Join(args, " ")
}

func writeConnectorHook(name, command string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "AgentChat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+"-hook.cmd")
	content := "@echo off\r\n" + command + "\r\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (a *app) connectorStatus() map[string]any {
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	codex := codexProbe()
	dsh := dshProbe()
	out := map[string]any{
		"agentchat":    cfg.AgentchatPath,
		"agent_config": cfg.AgentConfig,
		"connector":    cfg.Connector,
		"codex":        codex,
		"dsh":          dsh,
		"ready":        cfg.AgentConfig != "" && cfg.AgentchatPath != "",
	}
	if cfg.AgentConfig != "" && cfg.AgentchatPath != "" {
		out["mcp"] = codexMCPBlock(resolvedCLIPath(cfg.AgentchatPath), cfg.AgentConfig)
	}
	return out
}

func (a *app) handleConnectors(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, a.connectorStatus())
		return
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	var req struct {
		Action      string `json:"action"`
		Connector   string `json:"connector"`
		Name        string `json:"name"`
		Path        string `json:"path"`
		Exec        string `json:"exec"`
		AgentConfig string `json:"agent_config"`
		DSHHome     string `json:"dsh_home"`
		Workdir     string `json:"workdir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	if strings.EqualFold(strings.TrimSpace(req.Action), "use-config") {
		path := strings.TrimSpace(req.AgentConfig)
		if path == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "agent_config path is required"})
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "agent_config path is invalid"})
			return
		}
		if err := a.validateAgentConfigFile(abs); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": redactOutput(err.Error())})
			return
		}
		a.mu.RLock()
		cfg := a.cfg
		a.mu.RUnlock()
		cfg.AgentConfig = abs
		if err := a.saveConfig(cfg); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": redactOutput(err.Error())})
			return
		}
		a.mu.Lock()
		a.cfg = cfg
		a.configVersion++
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_config": abs})
		return
	}
	connector := strings.ToLower(strings.TrimSpace(req.Connector))
	if connector != "codex" && connector != "dsh" && connector != "generic" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported connector"})
		return
	}
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	if cfg.AgentConfig == "" || cfg.AgentchatPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "join a hub and configure an agent first"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "AgentChatBridge-" + connector
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`).MatchString(name) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "connector name may contain letters, digits, dot, dash, underscore only"})
		return
	}
	switch connector {
	case "codex":
		path := strings.TrimSpace(req.Path)
		if path == "" {
			path = codexProbe().Config
		}
		if !strings.EqualFold(filepath.Ext(path), ".toml") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Codex config path must end in .toml"})
			return
		}
		block := codexMCPBlock(resolvedCLIPath(cfg.AgentchatPath), cfg.AgentConfig)
		backup, err := writeCodexMCP(path, block)
		out := map[string]any{"ok": err == nil, "connector": "codex", "config": path, "backup": backup, "mcp": block}
		if err != nil {
			out["error"] = redactOutput(err.Error())
		} else {
			cfg.Connector = "codex"
			if saveErr := a.saveConfig(cfg); saveErr != nil {
				out["ok"] = false
				out["error"] = redactOutput(saveErr.Error())
			} else {
				a.mu.Lock()
				a.cfg = cfg
				a.configVersion++
				a.mu.Unlock()
			}
		}
		writeJSON(w, http.StatusOK, out)
	case "dsh":
		probe := dshProbe()
		dsh := strings.TrimSpace(req.Path)
		if dsh == "" {
			dsh = probe.Path
		}
		if dsh == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "DSH Desktop.exe not found; set the path manually"})
			return
		}
		if info, err := os.Stat(dsh); err != nil || info.IsDir() {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "DSH Desktop.exe path is not a file"})
			return
		}
		script := probe.Script
		if script == "" {
			script = filepath.Join(filepath.Dir(dsh), "resources", "app", "node_modules", "@deepseek-ai", "dsh", "lib", "bin.js")
		}
		if info, err := os.Stat(script); err != nil || info.IsDir() {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "DSH lib/bin.js not found; set a valid DSH install"})
			return
		}
		home := strings.TrimSpace(req.DSHHome)
		if home == "" {
			home = probe.Home
		}
		workdir := strings.TrimSpace(req.Workdir)
		if workdir == "" {
			workdir, _ = os.UserHomeDir()
		}
		cfg.Connector = "dsh"
		cfg.BridgeName = name
		hook, err := writeConnectorHook(name, dshBridgeExec(resolvedCLIPath(cfg.AgentchatPath), cfg.AgentConfig, dsh, script, home, workdir))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": redactOutput(err.Error())})
			return
		}
		cfg.BridgeExec = hook
		if err := a.saveConfig(cfg); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": redactOutput(err.Error())})
			return
		}
		a.mu.Lock()
		a.cfg = cfg
		a.configVersion++
		a.mu.Unlock()
		result := bridgeResult(cfg, "install")
		result["connector"] = "dsh"
		result["exec"] = redactOutput(cfg.BridgeExec)
		writeJSON(w, http.StatusOK, result)
	case "generic":
		execCmd := strings.TrimSpace(req.Exec)
		if execCmd == "" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "generic connector requires a command"})
			return
		}
		cfg.Connector = "generic"
		cfg.BridgeName = name
		hook, err := writeConnectorHook(name, execCmd)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": redactOutput(err.Error())})
			return
		}
		cfg.BridgeExec = hook
		if err := a.saveConfig(cfg); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": redactOutput(err.Error())})
			return
		}
		a.mu.Lock()
		a.cfg = cfg
		a.configVersion++
		a.mu.Unlock()
		result := bridgeResult(cfg, "install")
		result["connector"] = "generic"
		result["exec"] = redactOutput(cfg.BridgeExec)
		writeJSON(w, http.StatusOK, result)
	}
}

func (a *app) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Invite string `json:"invite"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	req.Invite = strings.TrimSpace(req.Invite)
	req.Name = strings.TrimSpace(req.Name)
	req.Kind = strings.TrimSpace(req.Kind)
	if req.Kind == "" {
		req.Kind = "human"
	}
	if req.Invite == "" || req.Name == "" || len(req.Invite) > 512 || len(req.Name) > 64 || len(req.Kind) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invite and name are required"})
		return
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.RLock()
	path := a.cfg.AgentConfig
	hubURL := a.cfg.HubURL
	configPath := a.configPath
	client := a.http
	a.mu.RUnlock()
	if path == "" {
		base := configPath
		if base == "" {
			if dir, err := os.UserConfigDir(); err == nil {
				base = filepath.Join(dir, "AgentChat", "desktop.json")
			}
		}
		path = filepath.Join(filepath.Dir(base), "agentchat.json")
	}
	if _, err := os.Stat(path); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "agent_config already exists"})
		return
	} else if !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot inspect agent_config"})
		return
	}
	body, _ := json.Marshal(protocol.JoinRequest{InviteCode: req.Invite, Name: req.Name, Kind: req.Kind})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+"/v1/join", bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot build join request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "hub is unreachable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": fmt.Sprintf("hub rejected join with HTTP %d", resp.StatusCode)})
		return
	}
	var joined protocol.JoinResponse
	if err := json.NewDecoder(http.MaxBytesReader(w, resp.Body, 1<<20)).Decode(&joined); err != nil || joined.Token == "" || joined.Agent.ID == "" {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "invalid join response from hub"})
		return
	}
	rooms := make([]string, 0, len(joined.Rooms))
	for _, room := range joined.Rooms {
		rooms = append(rooms, room.ID)
	}
	agentCfg := agentconfig.Config{
		HubURL: hubURL, Token: joined.Token, AgentID: joined.Agent.ID,
		AgentName: joined.Agent.Name, AgentKind: joined.Agent.Kind, Rooms: rooms,
	}
	if err := agentconfig.SaveNew(path, &agentCfg); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "agent_config already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save agent config"})
		return
	}
	a.mu.Lock()
	candidate := a.cfg
	candidate.AgentConfig = path
	if err := a.saveConfig(candidate); err != nil {
		a.mu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save desktop config"})
		return
	}
	a.cfg = candidate
	a.configVersion++
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "agent_id": joined.Agent.ID, "agent_name": joined.Agent.Name, "rooms": rooms, "agent_config": path})
}

func (a *app) saveConfig(cfg config) error {
	if a.configPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(a.configPath)
	tmp, err := os.CreateTemp(dir, ".desktop-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(tmpName)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(a.configPath)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func (a *app) saveDesktopConfig() error {
	return a.saveConfig(a.cfg)
}

func longWindowsPath(p string) string {
	ptr, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 32768)
	n, _, _ := procGetLongPathNameW.Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || n >= uintptr(len(buf)) {
		return p
	}
	return windows.UTF16ToString(buf[:int(n)])
}

func canonicalPath(path string) string {
	p := filepath.Clean(path)
	for _, prefix := range []string{`\\?\UNC\`, `\\?\`, `\??\`} {
		if strings.HasPrefix(p, prefix) {
			p = strings.TrimPrefix(p, prefix)
			if prefix == `\\?\UNC\` {
				p = `\\` + p
			}
			break
		}
	}
	p = longWindowsPath(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return strings.ToLower(filepath.Clean(p))
}

func withinPath(path, root string) bool {
	cp, cr := canonicalPath(path), canonicalPath(root)
	if rel, err := filepath.Rel(cr, cp); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return true
	}
	rootInfo, err := os.Stat(cr)
	if err != nil {
		return false
	}
	for p := cp; ; p = filepath.Dir(p) {
		if info, statErr := os.Stat(p); statErr == nil && os.SameFile(info, rootInfo) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
	}
}

func openVerifiedCLI(path string) (*os.File, error) {
	expected := strings.ToLower(strings.TrimSpace(expectedCLISHA256))
	if len(expected) != 64 {
		return nil, errors.New("CLI digest is not embedded in this Console build")
	}
	f, hash, err := openVerifiedFile(path)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(hash, expected) {
		f.Close()
		return nil, errors.New("CLI does not match the verified release payload")
	}
	return f, nil
}

type winSecurityAttributes struct {
	Length             uint32
	SecurityDescriptor uintptr
	InheritHandle      int32
}

func createSecureDirectory(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := winSecurityAttributes{
		Length:             uint32(unsafe.Sizeof(winSecurityAttributes{})),
		SecurityDescriptor: uintptr(unsafe.Pointer(sd)),
	}
	r, _, callErr := procCreateDirectoryW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&sa)))
	runtime.KeepAlive(sd)
	if r == 0 {
		return fmt.Errorf("create protected directory: %w", callErr)
	}
	return nil
}

func createSecureTokenFile(path, token string) error {
	sd, err := windows.SecurityDescriptorFromString("O:BAG:SYD:P(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sa := winSecurityAttributes{
		Length:             uint32(unsafe.Sizeof(winSecurityAttributes{})),
		SecurityDescriptor: uintptr(unsafe.Pointer(sd)),
	}
	h, _, callErr := procCreateFileSecureW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(windows.GENERIC_WRITE),
		0,
		uintptr(unsafe.Pointer(&sa)),
		uintptr(windows.CREATE_NEW),
		uintptr(windows.FILE_ATTRIBUTE_NORMAL),
		0,
	)
	runtime.KeepAlive(sd)
	if h == ^uintptr(0) {
		return fmt.Errorf("create admin token file: %w", callErr)
	}
	f := os.NewFile(h, path)
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func mergeChains(dst, src *pinnedDirectoryChain) {
	dst.handles = append(dst.handles, src.handles...)
	src.handles = nil
}

func prepareDataDirectory(dataPath string) (*pinnedDirectoryChain, error) {
	data := filepath.Clean(dataPath)
	parent := filepath.Dir(data)
	chain := &pinnedDirectoryChain{}
	fail := func(err error) (*pinnedDirectoryChain, error) {
		chain.Close()
		return nil, err
	}
	parentInfo, err := os.Lstat(parent)
	if os.IsNotExist(err) {
		programData := os.Getenv("ProgramData")
		if programData == "" || !withinPath(parent, filepath.Join(programData, "AgentChat")) {
			return fail(errors.New("data parent directory does not exist; create a protected data directory first"))
		}
		ancestor := filepath.Dir(parent)
		ancestorChain, err := pinDataDirectoryChain(ancestor)
		if err != nil {
			return fail(fmt.Errorf("pin data ancestor: %w", err))
		}
		mergeChains(chain, ancestorChain)
		if err := createSecureDirectory(parent, "O:BAG:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FR;;;BU)"); err != nil {
			return fail(fmt.Errorf("create data parent: %w", err))
		}
		parentInfo, err = os.Lstat(parent)
	}
	if err != nil {
		return fail(err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("data parent must be a regular directory"))
	}
	parentChain, err := pinDataDirectoryChain(parent)
	if err != nil {
		return fail(fmt.Errorf("data parent is not a protected directory: %w", err))
	}
	mergeChains(chain, parentChain)

	dataInfo, err := os.Lstat(data)
	switch {
	case os.IsNotExist(err):
		if err := createSecureDirectory(data, "O:BAG:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"); err != nil {
			return fail(fmt.Errorf("create data directory: %w", err))
		}
	case err != nil:
		return fail(err)
	default:
		if !dataInfo.IsDir() || dataInfo.Mode()&os.ModeSymlink != 0 {
			return fail(errors.New("data path must be a regular directory"))
		}
		return fail(errors.New("data directory already exists; remove it to initialize"))
	}
	dataChain, err := pinDataDirectoryChain(data)
	if err != nil {
		return fail(fmt.Errorf("data directory is not protected: %w", err))
	}
	mergeChains(chain, dataChain)
	return chain, nil
}

func (a *app) handleHubClearData(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil || req.Confirm != "delete-hub-data" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "type delete-hub-data to confirm"})
		return
	}
	a.mu.RLock()
	data, service := a.cfg.HubData, a.cfg.Service
	a.mu.RUnlock()
	if data == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub data path is not configured"})
		return
	}
	if st, err := serviceStatus(service); err == nil && st["state"] == "running" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "stop the Hub service before deleting its data"})
		return
	}
	abs, err := filepath.Abs(data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid data path"})
		return
	}
	abs = filepath.Clean(abs)
	vol := filepath.VolumeName(abs)
	if vol == "" || strings.Trim(abs, `\`) == strings.Trim(vol, `\`) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "refusing to delete a volume root"})
		return
	}
	base := strings.ToLower(filepath.Base(abs))
	parent := strings.ToLower(filepath.Base(filepath.Dir(abs)))
	if base != "hub-data" && parent != "agentchat" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "refusing to delete a non-managed data directory"})
		return
	}
	if err := os.RemoveAll(abs); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot delete hub data"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": abs})
}

func (a *app) handleHubInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Data      string `json:"data"`
		TokenFile string `json:"token_file"`
		Listen    string `json:"listen"`
		HubURL    string `json:"hub_url"`
		TLSCert   string `json:"tls_cert"`
		TLSKey    string `json:"tls_key"`
		TLSCA     string `json:"tls_ca"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	for _, p := range []string{req.Data, req.TokenFile, req.TLSCert, req.TLSKey, req.TLSCA} {
		if p != "" && (!filepath.IsAbs(p) || strings.ContainsRune(p, 0)) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "paths must be absolute"})
			return
		}
	}
	if req.Data == "" || req.TokenFile == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "data and token_file are required"})
		return
	}
	host, port, err := net.SplitHostPort(req.Listen)
	if err != nil || host == "" || port == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "listen must be host:port"})
		return
	}
	if (req.TLSCert == "") != (req.TLSKey == "") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tls_cert and tls_key must be supplied together"})
		return
	}
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.RLock()
	base := a.cfg
	configPath := a.configPath
	expectedVersion := a.configVersion
	a.mu.RUnlock()
	if base.AgentchatPath == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "agentchat_path is not configured"})
		return
	}
	hubURL := strings.TrimRight(strings.TrimSpace(req.HubURL), "/")
	if hubURL == "" {
		scheme := "http"
		if req.TLSCert != "" {
			scheme = "https"
		}
		hubURL = scheme + "://" + host + ":" + port
	}
	parsed, err := url.Parse(hubURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub_url must be http(s)"})
		return
	}
	if parsed.Scheme == "http" && !isLoopbackHostName(parsed.Hostname()) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "non-loopback hub_url must use https"})
		return
	}
	if parsed.Scheme == "https" && (req.TLSCert == "" || req.TLSKey == "") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "https hub_url requires a TLS certificate and private key"})
		return
	}
	hostname := strings.Trim(parsed.Hostname(), "[]")
	if i := strings.IndexByte(hostname, '%'); i >= 0 {
		hostname = hostname[:i]
	}
	if ip := net.ParseIP(hostname); ip != nil && ip.IsUnspecified() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub_url must use 127.0.0.1 or this machine's LAN address, not a wildcard listen address"})
		return
	}
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		appData := filepath.Join(base, "AgentChat")
		if withinPath(req.Data, appData) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Hub data must not be stored inside the per-user AgentChat install directory; choose a separate local data path"})
			return
		}
	}
	candidate := base
	candidate.Service = "AgentChatHub"
	candidate.HubData = req.Data
	candidate.TokenFile = req.TokenFile
	candidate.HubListen = req.Listen
	candidate.HubCert = req.TLSCert
	candidate.HubKey = req.TLSKey
	candidate.CAFile = req.TLSCA
	candidate.HubURL = hubURL
	candidate.Role = "hub"
	candidate.SetupDone = true
	client, err := newHubClient(candidate, configPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "hub TLS client could not be built"})
		return
	}
	dataGuard, err := prepareDataDirectory(req.Data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	defer dataGuard.Close()
	if _, err := os.Stat(req.TokenFile); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "admin token file already exists; remove it to initialize"})
		return
	} else if !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot inspect admin token file"})
		return
	}
	// Do not delete a failed init's data/token here: another process may have
	// created those paths after our preflight stat. Leave them for the operator
	// to inspect instead of risking removal of unowned files.
	cleanupInit := func() {}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token := protocol.NewToken("adm")
	if err := os.MkdirAll(filepath.Dir(req.TokenFile), 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot create admin token directory"})
		return
	}
	if err := createSecureTokenFile(req.TokenFile, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "admin token could not be written"})
		return
	}
	if err := hubpkg.InitWithToken(ctx, filepath.Join(req.Data, "agentchat.db"), token); err != nil {
		cleanupInit()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub init failed", "output": redactOutput(err.Error())})
		return
	}
	tokenData := []byte(token)
	a.mu.Lock()
	if a.configVersion != expectedVersion {
		a.mu.Unlock()
		cleanupInit()
		writeJSON(w, http.StatusConflict, map[string]any{"error": "desktop config changed during hub init; retry"})
		return
	}
	if err := a.saveConfig(candidate); err != nil {
		a.mu.Unlock()
		cleanupInit()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "hub initialized but desktop config could not be saved"})
		return
	}
	oldClient := a.http
	a.cfg = candidate
	a.configVersion++
	a.token = strings.TrimSpace(string(tokenData))
	a.http = client
	if oldClient != nil {
		oldClient.CloseIdleConnections()
	}
	a.mu.Unlock()
	next := ""
	serviceError := ""
	if err := trustedServiceBinary(candidate.AgentchatPath); err != nil {
		serviceError = err.Error()
	} else {
		binPath := quoteWindowsArg(candidate.AgentchatPath) + " service run --name AgentChatHub --log " +
			quoteWindowsArg(filepath.Join(req.Data, "hub.log")) + " -- hub serve --data " +
			quoteWindowsArg(req.Data) + " --listen " + quoteWindowsArg(req.Listen)
		if req.TLSCert != "" {
			binPath += " --tls-cert " + quoteWindowsArg(req.TLSCert) + " --tls-key " + quoteWindowsArg(req.TLSKey)
		}
		systemDir, err := windows.GetSystemDirectory()
		if err != nil {
			serviceError = "cannot locate the Windows service controller"
		} else {
			next = quoteWindowsArg(filepath.Join(systemDir, "sc.exe")) + " create AgentChatHub binPath= " + quoteWindowsArg(binPath) + " start= auto obj= LocalSystem"
		}
	}
	result := map[string]any{
		"ok": true, "hub_url": hubURL, "data": req.Data,
		"token_file": req.TokenFile, "next": next,
	}
	if serviceError != "" {
		result["service_error"] = serviceError
	}
	writeJSON(w, http.StatusOK, result)
}

func quoteWindowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range s {
		if r == '\\' {
			backslashes++
			continue
		}
		if r == '"' {
			b.WriteString(strings.Repeat(`\`, 2*backslashes+1))
			b.WriteByte('"')
			backslashes = 0
			continue
		}
		if backslashes > 0 {
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
		}
		b.WriteRune(r)
	}
	if backslashes > 0 {
		b.WriteString(strings.Repeat(`\`, 2*backslashes))
	}
	b.WriteByte('"')
	return b.String()
}

func hubServiceBinPath(cfg config, guard *serviceBinaryGuard) (string, error) {
	if cfg.HubData == "" || cfg.HubListen == "" {
		return "", errors.New("host setup is incomplete")
	}
	if (cfg.HubCert == "") != (cfg.HubKey == "") {
		return "", errors.New("TLS certificate and key must be configured together")
	}
	executable, err := guard.ExecutablePath()
	if err != nil {
		return "", err
	}
	args := []string{executable, "service", "run", "--name", "AgentChatHub", "--log", filepath.Join(cfg.HubData, "hub.log"), "--", "hub", "serve", "--data", cfg.HubData, "--listen", cfg.HubListen}
	if cfg.HubCert != "" {
		args = append(args, "--tls-cert", cfg.HubCert, "--tls-key", cfg.HubKey)
	}
	return windows.ComposeCommandLine(args), nil
}

func (a *app) handleHubService(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	run := func(args ...string) (string, error) {
		systemDir, err := windows.GetSystemDirectory()
		if err != nil {
			return "", err
		}
		out, err := runBoundedCommand(ctx, filepath.Join(systemDir, "sc.exe"), args, 16<<10)
		return strings.TrimSpace(out), err
	}
	result := map[string]any{"action": req.Action}
	switch req.Action {
	case "install":
		source := resolvedCLIPath(cfg.AgentchatPath)
		protected, err := ensureProtectedServiceBinary(source)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if !strings.EqualFold(protected, cfg.AgentchatPath) {
			cfg.AgentchatPath = protected
			a.mu.Lock()
			if saveErr := a.saveConfig(cfg); saveErr != nil {
				a.mu.Unlock()
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save protected service binary path"})
				return
			}
			a.cfg = cfg
			a.configVersion++
			a.mu.Unlock()
		}
		guard, err := openTrustedServiceBinary(cfg.AgentchatPath)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		defer guard.Close()
		bin, err := hubServiceBinPath(cfg, guard)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if err := guard.Verify(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		out, err := run("create", "AgentChatHub", "binPath=", bin, "start=", "auto", "obj=", "LocalSystem")
		result["output"] = out
		if err != nil {
			result["ok"] = false
			result["error"] = "service install failed (run the console as administrator)"
			writeJSON(w, http.StatusOK, result)
			return
		}
		err = serviceAction("AgentChatHub", "start", "")
		result["ok"] = err == nil
		if err != nil {
			result["error"] = "service installed but verified start failed: " + redactOutput(err.Error())
		}
	case "start", "restart":
		err := serviceAction("AgentChatHub", req.Action, "")
		result["ok"] = err == nil
		if err != nil {
			result["error"] = redactOutput(err.Error())
		}
	case "stop":
		out, err := run(req.Action, "AgentChatHub")
		result["output"] = out
		result["ok"] = err == nil
		if err != nil {
			result["error"] = "service control failed (administrator required)"
		}
	case "uninstall":
		_, _ = run("stop", "AgentChatHub")
		out, err := run("delete", "AgentChatHub")
		result["output"] = out
		result["ok"] = err == nil
		if err != nil {
			result["error"] = "service delete failed (administrator required)"
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported service action"})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *app) handleHubFirewall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	a.mu.RLock()
	listen := a.cfg.HubListen
	a.mu.RUnlock()
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub listen address is not configured"})
		return
	}
	if strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub is loopback-only; no LAN firewall rule is needed"})
		return
	}
	name := "AgentChat Hub " + port
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot locate Windows tools"})
		return
	}
	netsh := filepath.Join(systemDir, "netsh.exe")
	var args []string
	switch req.Action {
	case "open":
		args = []string{"advfirewall", "firewall", "add", "rule", "name=" + name, "dir=in", "action=allow", "protocol=TCP", "localport=" + port, "profile=any"}
	case "remove":
		args = []string{"advfirewall", "firewall", "delete", "rule", "name=" + name}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported firewall action"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := runBoundedCommand(ctx, netsh, args, 16<<10)
	result := map[string]any{"ok": err == nil, "output": redactOutput(strings.TrimSpace(out)), "port": port}
	if err != nil {
		result["error"] = "firewall change failed; run the console as administrator"
	}
	writeJSON(w, http.StatusOK, result)
}

func localTLSFiles() (string, string, string) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "AgentChat", "tls")
	return dir, filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
}

func generateHubCertificate() (string, string, string, error) {
	dir, certPath, keyPath := localTLSFiles()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return "", "", "", err
	}
	host, _ := os.Hostname()
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host, Organization: []string{"AgentChat LAN"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{host, "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP == nil || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
				continue
			}
			template.IPAddresses = append(template.IPAddresses, ipnet.IP)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", "", err
	}
	return certPath, keyPath, fmt.Sprintf("%x", sha256.Sum256(der)), nil
}

func certificateFingerprint(certPath string) (string, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("certificate is not PEM")
	}
	return fmt.Sprintf("%x", sha256.Sum256(block.Bytes)), nil
}

func resetLocalCertificate() (string, string, string, error) {
	dir, certPath, keyPath := localTLSFiles()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", err
	}
	oldCert, certErr := os.ReadFile(certPath)
	oldKey, keyErr := os.ReadFile(keyPath)
	if certErr != nil && !os.IsNotExist(certErr) {
		return "", "", "", fmt.Errorf("cannot back up existing certificate: %w", certErr)
	}
	if keyErr != nil && !os.IsNotExist(keyErr) {
		return "", "", "", fmt.Errorf("cannot back up existing private key: %w", keyErr)
	}
	restore := func() error {
		var errs []error
		if oldCert != nil {
			if err := os.WriteFile(certPath, oldCert, 0o600); err != nil {
				errs = append(errs, err)
			}
		} else if err := os.Remove(certPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		if oldKey != nil {
			if err := os.WriteFile(keyPath, oldKey, 0o600); err != nil {
				errs = append(errs, err)
			}
		} else if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	cert, key, fingerprint, err := generateHubCertificate()
	if err != nil {
		restoreErr := restore()
		return "", "", "", fmt.Errorf("generate certificate: %v (restore: %v)", err, restoreErr)
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		restoreErr := restore()
		return "", "", "", fmt.Errorf("locate Windows tools: %v (restore: %v)", err, restoreErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if out, err := runBoundedCommand(ctx, filepath.Join(systemDir, "certutil.exe"),
		[]string{"-addstore", "-user", "Root", cert}, 16<<10); err != nil {
		restoreErr := restore()
		return "", "", "", fmt.Errorf("certificate install failed: %s (restore: %v)", redactOutput(strings.TrimSpace(out)), restoreErr)
	}
	return cert, key, fingerprint, nil
}

func (a *app) handleHubTLS(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	var req struct {
		Action string `json:"action"`
		Cert   string `json:"cert"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	_, certPath, keyPath := localTLSFiles()
	switch req.Action {
	case "generate":
		if _, err := os.Stat(certPath); err == nil {
			fingerprint, ferr := certificateFingerprint(certPath)
			if ferr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": ferr.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cert": certPath, "key": keyPath, "fingerprint": fingerprint, "existing": true})
			return
		}
		cert, key, fingerprint, err := generateHubCertificate()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": redactOutput(err.Error())})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cert": cert, "key": key, "fingerprint": fingerprint})
	case "reset":
		cert, key, fingerprint, err := resetLocalCertificate()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "certificate reset failed: " + redactOutput(err.Error())})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cert": cert, "key": key, "fingerprint": fingerprint, "restart_required": true})
	case "install":
		path := strings.TrimSpace(req.Cert)
		if path == "" {
			path = certPath
		}
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "certificate path must be absolute"})
			return
		}
		if _, err := os.Stat(path); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "certificate file not found"})
			return
		}
		systemDir, err := windows.GetSystemDirectory()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot locate Windows tools"})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, err = runBoundedCommand(ctx, filepath.Join(systemDir, "certutil.exe"),
			[]string{"-addstore", "-user", "Root", path}, 16<<10)
		result := map[string]any{"ok": err == nil, "cert": path}
		if err != nil {
			result["error"] = "certificate install failed"
		} else {
			result["output"] = "证书已安装"
			if fingerprint, ferr := certificateFingerprint(path); ferr == nil {
				result["fingerprint"] = fingerprint
			}
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported certificate action"})
	}
}

func (a *app) handleUninstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm    string `json:"confirm"`
		RemoveData bool   `json:"remove_data"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&req); err != nil || req.Confirm != "uninstall" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "uninstall confirmation required"})
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "install path unavailable"})
		return
	}
	dir := filepath.Dir(exe)
	markerData, err := os.ReadFile(filepath.Join(dir, ".agentchat-install.json"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "not an AgentChat per-user install"})
		return
	}
	var marker uninstallMarker
	if json.Unmarshal(markerData, &marker) != nil || marker.App != "agentchat-desktop" || !strings.EqualFold(filepath.Clean(marker.Root), filepath.Clean(dir)) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "install ownership marker does not match"})
		return
	}
	if len(managedUninstallFiles(marker, dir, false)) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "install manifest has no managed files"})
		return
	}
	if m, err := mgr.Connect(); err == nil {
		if s, err := m.OpenService("AgentChatFedB"); err == nil {
			s.Close()
			m.Disconnect()
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "remove the AgentChatFedB service first"})
			return
		}
		m.Disconnect()
	}
	startup := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	for _, name := range []string{"AgentChat Console.lnk", "AgentChat Tray.lnk"} {
		_ = os.Remove(filepath.Join(startup, name))
	}
	if err := startUninstallWorker(dir, req.RemoveData); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "cannot start uninstall worker"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data_removed": req.RemoveData})
}

func (a *app) handleNetwork(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	certPath, keyPath, listen := a.cfg.HubCert, a.cfg.HubKey, a.cfg.HubListen
	a.mu.RUnlock()
	scheme := "https"
	tlsConfigured := strings.TrimSpace(certPath) != "" && strings.TrimSpace(keyPath) != ""
	port := "8787"
	if _, p, err := net.SplitHostPort(listen); err == nil && p != "" {
		port = p
	}
	certIPs := map[string]bool{}
	if data, err := os.ReadFile(certPath); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
				for _, ip := range cert.IPAddresses {
					if v4 := ip.To4(); v4 != nil {
						certIPs[v4.String()] = true
					}
				}
			}
		}
	}
	var candidates []string
	seen := map[string]bool{}
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			v4 := ip.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
				continue
			}
			s := v4.String()
			if !seen[s] {
				seen[s] = true
				candidates = append(candidates, s)
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		pi, pj := net.ParseIP(candidates[i]).IsPrivate(), net.ParseIP(candidates[j]).IsPrivate()
		if pi != pj {
			return pi
		}
		ci, cj := certIPs[candidates[i]], certIPs[candidates[j]]
		if ci != cj {
			return ci
		}
		return candidates[i] < candidates[j]
	})
	preferred := ""
	for _, ip := range candidates {
		if certIPs[ip] {
			preferred = ip
			break
		}
	}
	if preferred == "" && len(candidates) > 0 {
		preferred = candidates[0]
	}
	urls := make([]string, 0, len(candidates))
	for _, ip := range candidates {
		urls = append(urls, scheme+"://"+net.JoinHostPort(ip, port))
	}
	if preferred == "" {
		preferred = "127.0.0.1"
		urls = append(urls, scheme+"://"+net.JoinHostPort(preferred, port))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"preferred": scheme + "://" + net.JoinHostPort(preferred, port),
		"urls":      urls, "cert_ips": certIPs, "port": port, "scheme": scheme,
		"tls_configured": tlsConfigured,
	})
}

func (a *app) handleSettings(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost {
		var req struct {
			Action    string  `json:"action"`
			Role      string  `json:"role"`
			SetupDone *bool   `json:"setup_done"`
			TokenFile *string `json:"token_file"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}
		if req.Action == "leave_hub" || req.Action == "reset_all" {
			candidate := a.cfg
			managedRoot := filepath.Join(os.Getenv("LOCALAPPDATA"), "AgentChat")
			managedConfig := filepath.Join(managedRoot, "agentchat.json")
			agentConfig := strings.TrimSpace(candidate.AgentConfig)
			deleteAgent := agentConfig != "" && strings.EqualFold(filepath.Clean(agentConfig), filepath.Clean(managedConfig))
			var pinned pinnedFile
			var managedChain *pinnedDirectoryChain
			if deleteAgent {
				rootInfo, err := os.Lstat(managedRoot)
				if os.IsNotExist(err) {
					deleteAgent = false
				} else if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot validate managed agent directory"})
					return
				} else if managedChain, err = pinReparseChain(managedRoot); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot pin managed agent directory"})
					return
				} else if info, err := os.Lstat(managedConfig); os.IsNotExist(err) {
					deleteAgent = false
					_ = info
				} else if err != nil {
					managedChain.Close()
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot inspect managed agent config"})
					return
				} else if pinned, err = pinFile(managedConfig); err != nil {
					managedChain.Close()
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot pin managed agent config"})
					return
				}
			}
			if managedChain != nil {
				defer managedChain.Close()
			}
			candidate.AgentConfig = ""
			candidate.SetupDone = false
			if req.Action == "reset_all" {
				candidate.Role = "hub"
				candidate.HubURL = ""
				candidate.HubData = ""
				candidate.HubListen = ""
				candidate.HubCert = ""
				candidate.HubKey = ""
				candidate.CAFile = ""
				candidate.TokenFile = ""
			}
			if err := a.saveConfig(candidate); err != nil {
				pinned.close()
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save desktop config"})
				return
			}
			a.cfg = candidate
			if req.Action == "reset_all" {
				a.token = ""
			}
			a.configVersion++
			if pinned.handle != 0 {
				if err := pinned.delete(); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "settings saved but managed agent config could not be deleted"})
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": req.Action})
			return
		}
		candidate := a.cfg
		if req.Role != "" {
			switch req.Role {
			case "hub", "client", "bridge", "observer":
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid role"})
				return
			}
			candidate.Role = req.Role
		}
		if req.SetupDone != nil {
			candidate.SetupDone = *req.SetupDone
		}
		token := a.token
		if req.TokenFile != nil {
			path := strings.TrimSpace(*req.TokenFile)
			token = ""
			if path != "" {
				data, err := os.ReadFile(path)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cannot read admin token file"})
					return
				}
				token = strings.TrimSpace(string(data))
				if token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "admin token file is empty"})
					return
				}
			}
			candidate.TokenFile = path
		}
		if err := a.saveConfig(candidate); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save desktop config"})
			return
		}
		a.cfg = candidate
		a.token = token
		a.configVersion++
	}
	defaultData := ""
	if base := os.Getenv("ProgramData"); base != "" {
		defaultData = filepath.Join(base, "AgentChat", "hub-data")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role": a.cfg.Role, "setup_done": a.cfg.SetupDone, "is_admin": windows.GetCurrentProcessToken().IsElevated(), "config_path": a.configPath,
		"hub_url": a.cfg.HubURL, "agent_config": a.cfg.AgentConfig,
		"token_file": a.cfg.TokenFile,
		"hub_data":   a.cfg.HubData, "hub_listen": a.cfg.HubListen,
		"hub_tls_cert": a.cfg.HubCert, "hub_tls_key": a.cfg.HubKey,
		"default_data":  defaultData,
		"default_token": filepath.Join(filepath.Dir(defaultData), "admin.token"),
	})
}

func (a *app) hubRaw(method, path, token string, body []byte) ([]byte, int, error) {
	a.mu.RLock()
	client, hubURL := a.http, a.cfg.HubURL
	a.mu.RUnlock()
	if client == nil {
		return nil, 0, errors.New("hub client is not configured")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, hubURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("hub returned HTTP %d", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

func (a *app) agentToken() (string, error) {
	agent, err := a.loadAgentConfig()
	if err != nil {
		return "", err
	}
	return agent.Token, nil
}

func writeHubData(w http.ResponseWriter, data []byte, status int, err error) {
	if err != nil && len(data) == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	if err != nil && status < 400 {
		status = http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

type hubEndpoint struct {
	client  *http.Client
	baseURL string
	token   string
}

func (a *app) agentEndpoint() (hubEndpoint, error) {
	a.mu.RLock()
	client, baseURL, configPath := a.http, a.cfg.HubURL, a.cfg.AgentConfig
	a.mu.RUnlock()
	if client == nil {
		return hubEndpoint{}, errors.New("hub client is not configured")
	}
	if strings.TrimSpace(configPath) == "" {
		return hubEndpoint{}, errors.New("agent_config is not configured")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return hubEndpoint{}, err
	}
	var agent agentConfigFile
	if err := json.Unmarshal(data, &agent); err != nil {
		return hubEndpoint{}, errors.New("agent config is not valid JSON")
	}
	if strings.TrimSpace(agent.Token) == "" || strings.TrimSpace(agent.AgentID) == "" {
		return hubEndpoint{}, errors.New("agent config has no token or agent_id")
	}
	baseOrigin, err := canonicalOrigin(baseURL)
	if err != nil {
		return hubEndpoint{}, err
	}
	agentOrigin, err := canonicalOrigin(agent.HubURL)
	if err != nil || agentOrigin != baseOrigin {
		return hubEndpoint{}, errors.New("agent config hub_url does not match the desktop hub")
	}
	return hubEndpoint{client: client, baseURL: baseURL, token: agent.Token}, nil
}

func (a *app) adminEndpoint() (hubEndpoint, error) {
	a.mu.RLock()
	client, baseURL, token := a.http, a.cfg.HubURL, a.token
	a.mu.RUnlock()
	if client == nil {
		return hubEndpoint{}, errors.New("hub client is not configured")
	}
	if token == "" {
		return hubEndpoint{}, errors.New("admin token is not configured")
	}
	return hubEndpoint{client: client, baseURL: baseURL, token: token}, nil
}

func (e hubEndpoint) raw(method, path string, body []byte) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("hub returned HTTP %d", resp.StatusCode)
	}
	return data, resp.StatusCode, nil
}

func (a *app) handleChatRooms(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	data, status, err := endpoint.raw(http.MethodGet, "/v1/rooms", nil)
	writeHubData(w, data, status, err)
}

func (a *app) handleChatAgents(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	data, status, err := endpoint.raw(http.MethodGet, "/v1/agents", nil)
	writeHubData(w, data, status, err)
}

func (a *app) handleChatDM(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	var req struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	req.Agent = strings.TrimSpace(req.Agent)
	if req.Agent == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent is required"})
		return
	}
	body, _ := json.Marshal(map[string]string{"agent": req.Agent})
	data, status, err := endpoint.raw(http.MethodPost, "/v1/dm", body)
	writeHubData(w, data, status, err)
}

func (a *app) handleChatSearch(w http.ResponseWriter, r *http.Request) {
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	if room == "" {
		room = "general"
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "search query is required"})
		return
	}
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	path := "/v1/rooms/" + url.PathEscape(room) + "/search?q=" + url.QueryEscape(q) + "&limit=50"
	data, status, err := endpoint.raw(http.MethodGet, path, nil)
	writeHubData(w, data, status, err)
}

func (a *app) handleChatMessages(w http.ResponseWriter, r *http.Request) {
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	if room == "" {
		room = "general"
	}
	q := url.Values{}
	for _, key := range []string{"before_seq", "after_seq", "limit"} {
		if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
			q.Set(key, v)
		}
	}
	if q.Get("limit") == "" {
		q.Set("limit", "50")
	}
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	data, status, err := endpoint.raw(http.MethodGet, "/v1/rooms/"+url.PathEscape(room)+"/messages?"+q.Encode(), nil)
	writeHubData(w, data, status, err)
}

func (a *app) handleChatSend(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	var req struct {
		Room  string   `json:"room"`
		Text  string   `json:"text"`
		Files []string `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid message"})
		return
	}
	req.Room = strings.TrimSpace(req.Room)
	req.Text = strings.TrimSpace(req.Text)
	if req.Room == "" || req.Text == "" || len(req.Text) > 32<<10 || len(req.Files) > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "room and text are required"})
		return
	}
	payload := map[string]any{"room": req.Room, "text": req.Text}
	if len(req.Files) > 0 {
		payload["files"] = req.Files
	}
	body, _ := json.Marshal(payload)
	data, status, err := endpoint.raw(http.MethodPost, "/v1/rooms/"+url.PathEscape(req.Room)+"/messages", body)
	writeHubData(w, data, status, err)
}

func (a *app) handleChatUpload(w http.ResponseWriter, r *http.Request) {
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint.baseURL+"/v1/files", r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "cannot create upload request"})
		return
	}
	req.ContentLength = r.ContentLength
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	req.Header.Set("Authorization", "Bearer "+endpoint.token)
	resp, err := endpoint.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "upload failed"})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

func (a *app) handleChatFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "file id is required"})
		return
	}
	endpoint, err := a.agentEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint.baseURL+"/v1/files/"+url.PathEscape(id), nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "cannot create download request"})
		return
	}
	req.Header.Set("Authorization", "Bearer "+endpoint.token)
	resp, err := endpoint.client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "download failed"})
		return
	}
	defer resp.Body.Close()
	for _, header := range []string{"Content-Type", "Content-Disposition", "Content-Length"} {
		if v := resp.Header.Get(header); v != "" {
			w.Header().Set(header, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *app) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	a.adminProxy(w, r, http.MethodGet, "/v1/admin/agents")
}

func (a *app) adminProxy(w http.ResponseWriter, r *http.Request, method, path string) {
	endpoint, err := a.adminEndpoint()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": err.Error()})
		return
	}
	var body []byte
	if r.Body != nil && method != http.MethodGet {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 64<<10))
	}
	data, status, err := endpoint.raw(method, path, body)
	writeHubData(w, data, status, err)
}

func (a *app) handleAdminCreateAgent(w http.ResponseWriter, r *http.Request) {
	a.adminProxy(w, r, http.MethodPost, "/v1/admin/agents")
}

func (a *app) handleAdminRevokeAgent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent id is required"})
		return
	}
	a.adminProxy(w, r, http.MethodDelete, "/v1/admin/agents/"+url.PathEscape(id))
}

func (a *app) handleAdminRotateAgent(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "agent id is required"})
		return
	}
	a.adminProxy(w, r, http.MethodPost, "/v1/admin/agents/"+url.PathEscape(id)+"/token")
}

func (a *app) handleAdminCreateRoom(w http.ResponseWriter, r *http.Request) {
	a.adminProxy(w, r, http.MethodPost, "/v1/admin/rooms")
}

func (a *app) handleAdminArchiveRoom(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "room id is required"})
		return
	}
	a.adminProxy(w, r, http.MethodDelete, "/v1/admin/rooms/"+url.PathEscape(id))
}

func (a *app) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.adminProxy(w, r, http.MethodGet, "/v1/admin/invites")
		return
	}
	a.adminProxy(w, r, http.MethodPost, "/v1/admin/invites")
}

func (a *app) handleAdminFederation(w http.ResponseWriter, r *http.Request) {
	a.adminProxy(w, r, http.MethodGet, "/v1/admin/federation")
}

func (a *app) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	a.adminProxy(w, r, http.MethodGet, "/v1/admin/audit?limit=50")
}

func (a *app) handleChat(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := map[string]any{
		"hub_url":      a.cfg.HubURL,
		"agent_config": a.cfg.AgentConfig,
	}
	agent, err := a.loadAgentConfig()
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["ok"] = true
	out["agent_name"] = agent.AgentName
	out["agent_id"] = agent.AgentID
	writeJSON(w, http.StatusOK, out)
}

func bundledCLIPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for _, name := range []string{"agentchat-console.exe", "agentchat.exe"} {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func resolvedCLIPath(configured string) string {
	if bundled := bundledCLIPath(); bundled != "" {
		return bundled
	}
	return configured
}

func (a *app) handleBridge(w http.ResponseWriter, r *http.Request) {
	action := "status"
	if r.Method == http.MethodPost {
		var req struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
			return
		}
		action = strings.TrimSpace(req.Action)
	}
	switch action {
	case "status", "install", "uninstall":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported bridge action"})
		return
	}
	a.mu.RLock()
	cfg := a.cfg
	a.mu.RUnlock()
	writeJSON(w, http.StatusOK, bridgeResult(cfg, action))
}

func serviceStatus(name string) (map[string]any, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("service manager: %w (run the console as administrator)", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return map[string]any{"installed": false, "name": name}, nil
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return nil, err
	}
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	startup := "unknown"
	switch cfg.StartType {
	case mgr.StartAutomatic:
		startup = "automatic"
	case mgr.StartManual:
		startup = "manual"
	case mgr.StartDisabled:
		startup = "disabled"
	}
	return map[string]any{
		"installed": true, "name": name, "state": serviceState(st.State),
		"pid": st.ProcessId, "startup": startup,
	}, nil
}

func serviceState(st svc.State) string {
	switch st {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Paused:
		return "paused"
	default:
		return fmt.Sprintf("state-%d", st)
	}
}

func serviceAction(name, action, startup string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %w (run the console as administrator)", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return err
	}
	defer s.Close()
	switch action {
	case "start", "restart":
		return startRegisteredService(s, action, nativeExecutableVerifier)
	case "stop":
		if st, e := s.Query(); e == nil && (st.State == svc.Stopped || st.State == svc.StopPending) {
			return nil
		}
		_, err = s.Control(svc.Stop)
		return err
	case "startup":
		cfg, err := s.Config()
		if err != nil {
			return err
		}
		switch startup {
		case "automatic":
			cfg.StartType = mgr.StartAutomatic
		case "manual":
			cfg.StartType = mgr.StartManual
		case "disabled":
			cfg.StartType = mgr.StartDisabled
		default:
			return fmt.Errorf("startup must be automatic, manual, or disabled")
		}
		return s.UpdateConfig(cfg)
	default:
		return fmt.Errorf("unknown action")
	}
}

func (a *app) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	service, hubURL, token, client := a.cfg.Service, a.cfg.HubURL, a.token, a.http
	hasToken := token != ""
	a.mu.RUnlock()
	out := map[string]any{
		"service_name": service, "hub_url": hubURL,
		"is_admin": windows.GetCurrentProcessToken().IsElevated(),
	}
	if st, err := serviceStatus(service); err == nil {
		out["service"] = st
	} else {
		out["service_error"] = err.Error()
	}
	if err := hubGetWith(client, hubURL, token, "/healthz", nil); err != nil {
		out["hub_error"] = err.Error()
	} else {
		out["hub_ok"] = true
	}
	if hasToken {
		var status map[string]any
		if err := hubGetWith(client, hubURL, token, "/v1/admin/federation", &status); err == nil {
			out["federation"] = status
		} else {
			out["federation_error"] = err.Error()
		}
	} else {
		out["federation_error"] = "admin token file is not configured"
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *app) handleService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string `json:"action"`
		Startup string `json:"startup"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
		return
	}
	a.mu.RLock()
	service := a.cfg.Service
	a.mu.RUnlock()
	if err := serviceAction(service, req.Action, req.Startup); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func tailFile(path string, maxLines int, maxBytes int64) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - maxBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lines := make([]string, 0, maxLines)
	for scanner.Scan() {
		lines = append(lines, strings.TrimSuffix(scanner.Text(), "\r"))
		if len(lines) > maxLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func tailLogPath(path, component string, maxLines int) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return tailFile(path, maxLines, 1<<20)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasPrefix(name, component+"-") && strings.HasSuffix(name, ".jsonl") {
			names = append(names, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	lines := []string{}
	for _, name := range names {
		part, err := tailFile(filepath.Join(path, name), maxLines, 1<<20)
		if err != nil {
			return nil, err
		}
		lines = append(part, lines...)
		if len(lines) >= maxLines {
			break
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

func (a *app) handleLogs(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = "desktop"
	}
	path, ok := a.logs[source]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown log source"})
		return
	}
	lines, err := tailLogPath(path, source, 1000)
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusOK, map[string]any{"source": source, "lines": []string{}, "available": false})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": source, "lines": lines, "available": true})
}

func (a *app) handleOutbox(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	hasToken := a.token != ""
	a.mu.RUnlock()
	if !hasToken {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "admin token file is not configured"})
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "dead"
	}
	var out map[string]any
	if err := a.hubGet("/v1/admin/federation/outbox?state="+state+"&limit=50", &out); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *app) handleRetry(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	hubURL, token, client := a.cfg.HubURL, a.token, a.http
	a.mu.RUnlock()
	if token == "" || client == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "admin token file is not configured"})
		return
	}
	var req struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.ID == "" || req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "id and key are required"})
		return
	}
	body, _ := json.Marshal(map[string]any{})
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, hubURL+"/v1/admin/federation/outbox/"+req.ID+"/retry", strings.NewReader(string(body)))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", req.Key)
	resp, err := client.Do(httpReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

func hubGetWith(client *http.Client, hubURL, token, path string, dst any) error {
	if client == nil {
		return errors.New("hub client is not configured")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, hubURL+path, nil)
	if err != nil {
		return err
	}
	if strings.HasPrefix(path, "/v1/admin/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("hub returned HTTP %d", resp.StatusCode)
	}
	if dst == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(dst)
}

func (a *app) hubGet(path string, dst any) error {
	a.mu.RLock()
	hubURL, token, client := a.cfg.HubURL, a.token, a.http
	a.mu.RUnlock()
	return hubGetWith(client, hubURL, token, path, dst)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
