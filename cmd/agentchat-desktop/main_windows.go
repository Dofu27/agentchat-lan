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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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

var (
	instanceMutex windows.Handle
	adminWindow   webview2.WebView
	chatWindow    webview2.WebView
)

func main() {
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
	origin := "http://" + ln.Addr().String()
	w.Init("if (window.top === window && location.origin === " + strconv.Quote(origin) +
		") { window.__agentchatDesktopCapability = " + strconv.Quote(a.cap) + "; }")
	_ = w.Bind("__openAdminWeb", func() error {
		a.mu.RLock()
		hasToken := a.token != ""
		a.mu.RUnlock()
		if !hasToken {
			return errors.New("admin token file is not configured")
		}
		w.Dispatch(func() {
			if err := a.openAdminWindow(); err != nil {
				log.Printf("open admin window: %v", err)
			}
		})
		return nil
	})
	_ = w.Bind("__openChatWeb", func() error {
		w.Dispatch(func() {
			if err := a.openChatWindow(); err != nil {
				log.Printf("open chat window: %v", err)
			}
		})
		return nil
	})
	w.Navigate(origin + "/")
	w.Run()
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
			if mediaType != "application/json" {
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

func (a *app) openAdminWindow() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if adminWindow != nil {
		adminWindow.Navigate(a.cfg.HubURL + "/admin")
		return nil
	}
	adminWindow = webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title: "AgentChat Admin", Width: 1280, Height: 860, Center: true,
		},
	})
	if adminWindow == nil {
		return errors.New("could not create the admin WebView2 window")
	}
	origin, err := canonicalOrigin(a.cfg.HubURL)
	if err != nil {
		return err
	}
	adminWindow.Init(adminLoginScript(a.token, origin))
	adminWindow.Navigate(a.cfg.HubURL + "/admin")
	return nil
}

type agentConfigFile struct {
	HubURL    string `json:"hub_url"`
	AgentName string `json:"agent_name"`
	AgentID   string `json:"agent_id"`
	Token     string `json:"token"`
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

func (a *app) openChatWindow() error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if chatWindow != nil {
		chatWindow.Navigate(a.cfg.HubURL + "/")
		return nil
	}
	agent, err := a.loadAgentConfig()
	if err != nil {
		return err
	}
	chatWindow = webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: false,
		WindowOptions: webview2.WindowOptions{
			Title: "AgentChat", Width: 1280, Height: 860, Center: true,
		},
	})
	if chatWindow == nil {
		return errors.New("could not create the chat WebView2 window")
	}
	origin, err := canonicalOrigin(a.cfg.HubURL)
	if err != nil {
		return err
	}
	chatWindow.Init(chatLoginScript(agent.Token, origin))
	chatWindow.Navigate(a.cfg.HubURL + "/")
	return nil
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

func chatLoginScript(token, origin string) string {
	return `(function(){var t=` + strconv.Quote(token) + `,o=` + strconv.Quote(origin) +
		`;if(location.origin!==o)return;try{sessionStorage.setItem("agentchat_token",t);}catch(e){}})();`
}

func adminLoginScript(token, origin string) string {
	return `(function(){var t=` + strconv.Quote(token) + `,o=` + strconv.Quote(origin) + `;if(location.origin!==o)return;var n=0;function go(){var login=document.getElementById("admin-login");var i=document.getElementById("admin-token");var f=document.getElementById("admin-login-form");if(login&&login.classList.contains("hidden"))return;if(i&&f&&!i.value){i.value=t;f.requestSubmit();return;}if(n++<20)setTimeout(go,250);}if(document.readyState==="loading")document.addEventListener("DOMContentLoaded",go);else go();})();`
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
	mux.Handle("GET /api/chat", a.api(http.HandlerFunc(a.handleChat)))
	mux.Handle("POST /api/client/join", a.api(http.HandlerFunc(a.handleJoin)))
	mux.Handle("GET /api/settings", a.api(http.HandlerFunc(a.handleSettings)))
	mux.Handle("POST /api/settings", a.api(http.HandlerFunc(a.handleSettings)))
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

func (a *app) handleSettings(w http.ResponseWriter, r *http.Request) {
	a.setupMu.Lock()
	defer a.setupMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodPost {
		var req struct {
			Role      string `json:"role"`
			SetupDone *bool  `json:"setup_done"`
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
		if err := a.saveConfig(candidate); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "cannot save desktop config"})
			return
		}
		a.cfg = candidate
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
