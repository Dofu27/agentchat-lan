//go:build windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	mutexName, _ := windows.UTF16PtrFromString(`Local\AgentChatConsole`)
	instanceMutex, _ = windows.CreateMutex(nil, false, mutexName)
	if instanceMutex != 0 && windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		return
	}
	var cfgPath string
	var service string
	var hub string
	var tokenFile string
	var headless bool
	var listen string
	flag.StringVar(&cfgPath, "config", "", "desktop config JSON")
	flag.BoolVar(&headless, "headless", false, "serve the local console API without opening a window")
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
		if exe, err := os.Executable(); err == nil {
			cfg.AgentchatPath = filepath.Join(filepath.Dir(exe), "agentchat.exe")
		}
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
	procShellNotifyIconW     = shell32DLL.NewProc("Shell_NotifyIconW")
	procExtractIconExW       = shell32DLL.NewProc("ExtractIconExW")
	dwmapiDLL                = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetAttribute      = dwmapiDLL.NewProc("DwmSetWindowAttribute")
	uxthemeDLL               = windows.NewLazySystemDLL("uxtheme.dll")
	procSetWindowTheme       = uxthemeDLL.NewProc("SetWindowTheme")
)

const (
	wmClose    = 0x0010
	wmDestroy  = 0x0002
	wmNull     = 0x0000
	wmTray     = 0x8001
	trayShowID = 1001
	trayExitID = 1002
	swHide     = 0
	swMinimize = 6
	swRestore  = 9
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
	trayIconAdded   bool
	closeDialogEval func()
	allowClose      atomic.Bool
)

func ensureTrayIcon(hwnd uintptr) {
	if trayIconAdded || hwnd == 0 {
		return
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
	}
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
		showWindow(hwnd, swHide)
		ensureTrayIcon(hwnd)
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
	mux.Handle("POST /api/hub/service", a.api(http.HandlerFunc(a.handleHubService)))
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
	out, err := runBoundedCommand(ctx, cfg.AgentchatPath, args, 16<<10)
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
		out["mcp"] = codexMCPBlock(cfg.AgentchatPath, cfg.AgentConfig)
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
		block := codexMCPBlock(cfg.AgentchatPath, cfg.AgentConfig)
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
		hook, err := writeConnectorHook(name, dshBridgeExec(cfg.AgentchatPath, cfg.AgentConfig, dsh, script, home, workdir))
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
	if _, err := os.Stat(req.Data); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "data directory already exists; remove it to initialize"})
		return
	} else if !os.IsNotExist(err) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot inspect data directory"})
		return
	}
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
	args := []string{"hub", "init", "--data", req.Data, "--admin-token-file", req.TokenFile, "--json"}
	out, err := runBoundedCommand(ctx, base.AgentchatPath, args, 16<<10)
	if err != nil {
		cleanupInit()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "hub init failed", "output": redactOutput(strings.TrimSpace(out))})
		return
	}
	tokenData, err := os.ReadFile(req.TokenFile)
	if err != nil {
		cleanupInit()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "hub initialized but admin token could not be read"})
		return
	}
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

func (a *app) handleSettings(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost {
		var req struct {
			Role      string  `json:"role"`
			SetupDone *bool   `json:"setup_done"`
			TokenFile *string `json:"token_file"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request"})
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
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		defaultData = filepath.Join(base, "AgentChat", "hub")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role": a.cfg.Role, "setup_done": a.cfg.SetupDone, "config_path": a.configPath,
		"hub_url": a.cfg.HubURL, "agent_config": a.cfg.AgentConfig,
		"token_file": a.cfg.TokenFile,
		"hub_data":   a.cfg.HubData, "hub_listen": a.cfg.HubListen,
		"hub_tls_cert": a.cfg.HubCert, "hub_tls_key": a.cfg.HubKey,
		"default_data":  defaultData,
		"default_token": filepath.Join(defaultData, "admin.token"),
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
