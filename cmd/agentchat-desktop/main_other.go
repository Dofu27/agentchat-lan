//go:build !windows

package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed launcher.html
var launcherFiles embed.FS

type launcherConfig struct {
	HubURL      string `json:"hub_url"`
	Role        string `json:"role"`
	AgentConfig string `json:"agent_config,omitempty"`
	TokenFile   string `json:"token_file,omitempty"`
}

func loadLauncherConfig(path string) launcherConfig {
	cfg := launcherConfig{HubURL: "http://127.0.0.1:8787", Role: "client"}
	if path == "" {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	cfg.HubURL = strings.TrimRight(strings.TrimSpace(cfg.HubURL), "/")
	if cfg.HubURL == "" {
		cfg.HubURL = "http://127.0.0.1:8787"
	}
	if cfg.Role == "" {
		cfg.Role = "client"
	}
	return cfg
}

func launcherHandler(cfg launcherConfig, cfgPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","launcher":"agentchat"}`))
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hub_url": cfg.HubURL, "role": cfg.Role, "config_path": cfgPath,
			"agent_config": cfg.AgentConfig != "", "token_file": cfg.TokenFile != "",
		})
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(launcherFiles, "launcher.html")
		if err != nil {
			http.Error(w, "launcher unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	return mux
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func main() {
	configPath := flag.String("config", "", "desktop JSON config path")
	listen := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	noOpen := flag.Bool("no-open", false, "do not launch a browser")
	flag.Parse()

	if *configPath == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			*configPath = filepath.Join(dir, "AgentChat", "desktop.json")
		}
	}
	cfg := loadLauncherConfig(*configPath)
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid listen address:", err)
		os.Exit(2)
	}
	ip := net.ParseIP(host)
	if ip == nil && host != "localhost" {
		fmt.Fprintln(os.Stderr, "listen must be a loopback address")
		os.Exit(2)
	}
	if ip != nil && !ip.IsLoopback() {
		fmt.Fprintln(os.Stderr, "listen must be a loopback address")
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.IP == nil || !addr.IP.IsLoopback() {
		ln.Close()
		fmt.Fprintln(os.Stderr, "refusing a non-loopback listener")
		os.Exit(1)
	}
	server := &http.Server{
		Handler:           launcherHandler(cfg, *configPath),
		ReadHeaderTimeout: 5 * time.Second,
	}
	url := "http://" + ln.Addr().String() + "/"
	fmt.Println("AgentChat launcher:", url)
	fmt.Println("hub:", cfg.HubURL)
	if !*noOpen {
		if err := openBrowser(url); err != nil {
			fmt.Fprintln(os.Stderr, "open browser:", err)
		}
	}
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
