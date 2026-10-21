//go:build windows

package main

import (
	"bufio"
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
	"time"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type config struct {
	Service   string            `json:"service"`
	HubURL    string            `json:"hub_url"`
	TokenFile string            `json:"token_file"`
	CAFile    string            `json:"tls_ca_file,omitempty"`
	Title     string            `json:"title,omitempty"`
	Width     int               `json:"width,omitempty"`
	Height    int               `json:"height,omitempty"`
	LogFiles  map[string]string `json:"log_files,omitempty"`
}

type app struct {
	cfg   config
	token string
	cap   string
	http  *http.Client
	logs  map[string]string
}

var (
	instanceMutex windows.Handle
	adminWindow   webview2.WebView
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
	hubURL, err := url.Parse(cfg.HubURL)
	if err != nil || hubURL.Host == "" || (hubURL.Scheme != "https" && hubURL.Scheme != "http") {
		log.Fatalf("hub_url must be an http(s) URL")
	}
	if hubURL.Scheme != "https" && !isLoopbackHostName(hubURL.Hostname()) {
		log.Fatalf("hub_url must use https outside loopback")
	}
	caFile := cfg.CAFile
	if caFile != "" && !filepath.IsAbs(caFile) && cfgPath != "" {
		caFile = filepath.Join(filepath.Dir(cfgPath), caFile)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			log.Fatalf("read hub CA: %v", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			log.Fatalf("hub CA contains no certificates")
		}
		tlsConfig.RootCAs = pool
	}
	client := &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("hub redirect refused")
		},
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
	a := &app{cfg: cfg, token: token, cap: capability, http: client, logs: logs}

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

	mutex, err := acquireConsoleMutex()
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			log.Print("AgentChat Console is already running")
			return
		}
		log.Fatalf("single-instance guard: %v", err)
	}
	defer windows.CloseHandle(mutex)

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
		if a.token == "" {
			return errors.New("admin token file is not configured")
		}
		w.Dispatch(func() {
			if err := a.openAdminWindow(); err != nil {
				log.Printf("open admin window: %v", err)
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
		if r.Method == http.MethodPost && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "application/json required"})
			return
		}
		got := strings.TrimSpace(r.Header.Get("X-AgentChat-Desktop-Capability"))
		if subtle.ConstantTimeCompare([]byte(got), []byte(a.cap)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "desktop capability required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) openAdminWindow() error {
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
	adminWindow.Init(adminLoginScript(a.token, a.cfg.HubURL))
	adminWindow.Navigate(a.cfg.HubURL + "/admin")
	return nil
}

func adminLoginScript(token, hubURL string) string {
	u, _ := url.Parse(hubURL)
	origin := ""
	if u != nil {
		origin = u.Scheme + "://" + u.Host
	}
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
	mux.Handle("GET /api/federation/outbox", a.api(http.HandlerFunc(a.handleOutbox)))
	mux.Handle("POST /api/federation/retry", a.api(http.HandlerFunc(a.handleRetry)))
	return ln, &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, nil
}

func acquireConsoleMutex() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(`Local\AgentChatConsole`)
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		return 0, err
	}
	return handle, nil
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
	case "start":
		if st, e := s.Query(); e == nil && (st.State == svc.Running || st.State == svc.StartPending) {
			return nil
		}
		return s.Start()
	case "stop":
		if st, e := s.Query(); e == nil && (st.State == svc.Stopped || st.State == svc.StopPending) {
			return nil
		}
		_, err = s.Control(svc.Stop)
		return err
	case "restart":
		if st, e := s.Query(); e == nil && st.State == svc.Stopped {
			return s.Start()
		}
		_, _ = s.Control(svc.Stop)
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			st, _ := s.Query()
			if st.State == svc.Stopped {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		return s.Start()
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
	out := map[string]any{
		"service_name": a.cfg.Service, "hub_url": a.cfg.HubURL,
	}
	if st, err := serviceStatus(a.cfg.Service); err == nil {
		out["service"] = st
	} else {
		out["service_error"] = err.Error()
	}
	if err := a.hubGet("/healthz", nil); err != nil {
		out["hub_error"] = err.Error()
	} else {
		out["hub_ok"] = true
	}
	if a.token != "" {
		var status map[string]any
		if err := a.hubGet("/v1/admin/federation", &status); err == nil {
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
	if err := serviceAction(a.cfg.Service, req.Action, req.Startup); err != nil {
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
	if a.token == "" {
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
	if a.token == "" {
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
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.cfg.HubURL+"/v1/admin/federation/outbox/"+req.ID+"/retry", strings.NewReader(string(body)))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", req.Key)
	resp, err := a.http.Do(httpReq)
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

func (a *app) hubGet(path string, dst any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, a.cfg.HubURL+path, nil)
	if err != nil {
		return err
	}
	if strings.HasPrefix(path, "/v1/admin/") {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.http.Do(req)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
