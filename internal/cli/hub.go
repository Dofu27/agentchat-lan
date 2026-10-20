package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (a *App) cmdHub(args []string) int {
	if len(args) == 0 {
		a.hubUsage()
		return 2
	}
	switch args[0] {
	case "init":
		return a.hubInit(args[1:])
	case "serve":
		return a.hubServe(args[1:])
	case "federation-id":
		return a.hubFederationID(args[1:])
	case "federation":
		return a.hubFederationOperations(args[1:])
	case "invite":
		return a.hubInvite(args[1:])
	case "list-invites":
		return a.hubListInvites(args[1:])
	case "audit":
		return a.hubAudit(args[1:])
	case "gc":
		return a.hubGC(args[1:])
	case "backup":
		return a.hubBackup(args[1:])
	case "restore":
		return a.hubRestore(args[1:])
	case "help", "-h", "--help":
		a.hubUsage()
		return 0
	default:
		if strings.HasPrefix(args[0], "-") {
			return a.hubServe(args)
		}
		fmt.Fprintf(a.Stderr, "unknown hub subcommand %q\n\n", args[0])
		a.hubUsage()
		return 2
	}
}

func (a *App) hubUsage() {
	fmt.Fprint(a.Stdout, `agentchat hub — run and administer the production hub

Usage:
  agentchat hub init   --data <dir> [--admin-token-file <path>] [--json]
  agentchat hub serve  --data <dir> [--listen 127.0.0.1:8787]
                       [--tls-cert <path> --tls-key <path>]
                       [--federation-config <path>] (two-node PoC only)
                       [--max-storage-bytes <n>]
  agentchat hub federation-id --data <dir>
  agentchat hub federation status --data <dir> (read-only persisted state)
  agentchat hub invite --data <dir> [--room general] [--name <agent>]
                       [--ttl 24h] [--uses 1] [--json]
  agentchat hub list-invites --data <dir> [--json]
  agentchat hub audit  --data <dir> [--limit 100] [--json]
  agentchat hub gc     --data <dir> [--days 30] [--apply] [--json]
  agentchat hub backup --data <dir> --out <backup.tar.gz> [--force] [--json]
  agentchat hub restore --data <dir> --in <backup.tar.gz> [--force] [--json]

The database lives at <data>/agentchat.db. The default listener is loopback;
pass --listen 0.0.0.0:8787 only on a trusted LAN with a firewall rule.
Provide both TLS flags to serve HTTPS on any listen address (TLS 1.2+).
`)
}

func hubDBPath(data string) string {
	return filepath.Join(data, "agentchat.db")
}

func (a *App) hubInit(args []string) int {
	fs := a.newFlagSet("hub init")
	data := fs.String("data", "./data", "data directory")
	tokenFile := fs.String("admin-token-file", "", "write the admin token to this file instead of stdout")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 0 {
		return a.fail(fmt.Errorf("hub init takes no positional arguments"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	token, err := hub.Init(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(err)
	}

	if *tokenFile != "" {
		if err := writeSecret(*tokenFile, token); err != nil {
			return a.fail(err)
		}
	}
	out := map[string]any{
		"data":       *data,
		"database":   hubDBPath(*data),
		"admin_file": *tokenFile,
	}
	if *tokenFile == "" {
		out["admin_token"] = token
	}
	if *jsonOut {
		if code := a.printJSON(out); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.Stdout, "initialized hub data=%s database=%s\n", *data, hubDBPath(*data))
		if *tokenFile != "" {
			fmt.Fprintf(a.Stdout, "admin token written to %s (keep it private)\n", *tokenFile)
		} else {
			fmt.Fprintf(a.Stdout, "admin token (shown once): %s\n", token)
		}
	}
	return 0
}

func (a *App) hubServe(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.hubServeContext(ctx, args)
}

func (a *App) hubServeContext(ctx context.Context, args []string) int {
	fs := a.newFlagSet("hub serve")
	data := fs.String("data", "./data", "data directory")
	listen := fs.String("listen", "127.0.0.1:8787", "listen address")
	certFile := fs.String("tls-cert", "", "TLS certificate PEM file (requires --tls-key)")
	keyFile := fs.String("tls-key", "", "TLS private key PEM file (requires --tls-cert)")
	rpm := fs.Int("requests-per-minute", 60, "per-agent request budget")
	burst := fs.Int("burst", 20, "per-agent burst")
	uploadsPerMinute := fs.Int("uploads-per-minute", 10, "per-agent file upload budget")
	maxStorageBytes := fs.Int64("max-storage-bytes", 0, "hub-wide file storage quota in bytes; 0 is unlimited")
	federationConfig := fs.String("federation-config", "", "operator federation config; omitted disables federation")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 0 {
		return a.fail(fmt.Errorf("hub serve takes no positional arguments"))
	}
	if *maxStorageBytes < 0 {
		return a.fail(fmt.Errorf("--max-storage-bytes must be non-negative"))
	}

	var certSet, keySet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "tls-cert":
			certSet = true
		case "tls-key":
			keySet = true
		}
	})
	if certSet != keySet || (certSet && (*certFile == "" || *keyFile == "")) {
		return a.fail(fmt.Errorf("--tls-cert and --tls-key must be provided together with non-empty paths"))
	}
	var tlsConfig *tls.Config
	if certSet {
		// Validate before opening the database or listener; never fall back to HTTP.
		cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			return a.fail(fmt.Errorf("load hub TLS certificate/key: %w", err))
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}

	st, err := store.Open(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(err)
	}
	defer st.Close()
	var fed *hub.Federation
	if *federationConfig != "" {
		fed, err = loadFederation(ctx, st, *federationConfig, filepath.Join(*data, "files"))
		if err != nil {
			return a.fail(err)
		}
	}

	srv := hub.New(st, hub.Options{
		Version:              a.Version,
		RequestsPerMinute:    *rpm,
		Burst:                *burst,
		FileUploadsPerMinute: *uploadsPerMinute,
		MaxFileSize:          protocol.MaxFileSize,
		MaxStorageBytes:      *maxStorageBytes,
		FileDir:              filepath.Join(*data, "files"),
		Logger:               log.New(a.Stderr, "hub ", log.LstdFlags),
		Federation:           fed,
	})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return a.fail(err)
	}
	if fed != nil && tlsConfig == nil {
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok || !addr.IP.IsLoopback() {
			ln.Close()
			return a.fail(fmt.Errorf("federation requires HTTPS on non-loopback listeners"))
		}
	}
	scheme := "http"
	if tlsConfig != nil {
		ln = tls.NewListener(ln, tlsConfig)
		scheme = "https"
	} else if addr, ok := ln.Addr().(*net.TCPAddr); !ok || !addr.IP.IsLoopback() {
		fmt.Fprintln(a.Stderr, "WARNING: non-loopback HTTP listener is insecure; tokens and traffic are unencrypted. Use --tls-cert and --tls-key to enable HTTPS.")
	}
	fmt.Fprintf(a.Stdout, "hub listening on %s://%s data=%s\n", scheme, ln.Addr().String(), *data)
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	workerDone := make(chan error, 1)
	if fed != nil {
		go func() {
			err := fed.Run(serveCtx)
			workerDone <- err
			if err != nil {
				cancelServe()
			}
		}()
	}
	err = srv.Serve(serveCtx, ln)
	cancelServe()
	if fed != nil {
		if workerErr := <-workerDone; workerErr != nil {
			return a.fail(workerErr)
		}
	}
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintln(a.Stdout, "hub stopped")
	return 0
}

func (a *App) hubInvite(args []string) int {
	fs := a.newFlagSet("hub invite")
	data := fs.String("data", "./data", "data directory")
	room := fs.String("room", "general", "room ID")
	name := fs.String("name", "", "bind the invite to this agent name")
	ttl := fs.Duration("ttl", 24*time.Hour, "invite lifetime")
	uses := fs.Int("uses", 1, "number of uses")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 0 {
		return a.fail(fmt.Errorf("hub invite takes no positional arguments"))
	}
	if *ttl < time.Minute || *ttl > 30*24*time.Hour {
		return a.fail(fmt.Errorf("--ttl must be between 1m and 720h"))
	}
	if *uses < 1 || *uses > 1000 {
		return a.fail(fmt.Errorf("--uses must be between 1 and 1000"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.Open(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(err)
	}
	defer st.Close()

	code, err := hub.NewInviteCode()
	if err != nil {
		return a.fail(err)
	}
	expiry, err := st.CreateInvite(ctx, code, store.Invite{Room: *room, Name: *name, TTL: *ttl, Uses: *uses}, "cli-admin")
	if err != nil {
		return a.fail(err)
	}

	out := map[string]any{
		"code":       code,
		"room":       *room,
		"name":       *name,
		"expires_at": expiry,
		"uses":       *uses,
	}
	if *jsonOut {
		return a.printJSON(out)
	}
	fmt.Fprintf(a.Stdout, "invite %s room=%s uses=%d expires=%s\n", code, *room, *uses, expiry.Local().Format(time.DateTime))
	if *name != "" {
		fmt.Fprintf(a.Stdout, "bound to agent name: %s\n", *name)
	}
	return 0
}

func (a *App) hubListInvites(args []string) int {
	fs := a.newFlagSet("hub list-invites")
	data := fs.String("data", "./data", "data directory")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(err)
	}
	defer st.Close()

	invites, err := st.ListInvites(ctx)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(invites)
	}
	for _, inv := range invites {
		fmt.Fprintf(a.Stdout, "room=%s name=%s uses=%d expires=%s\n", inv.Room, inv.Name, inv.Uses, inv.ExpiresAt.Local().Format(time.DateTime))
	}
	return 0
}

func (a *App) hubAudit(args []string) int {
	fs := a.newFlagSet("hub audit")
	data := fs.String("data", "./data", "data directory")
	limit := fs.Int("limit", 100, "maximum events (1-1000)")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if *limit < 1 || *limit > 1000 {
		return a.fail(fmt.Errorf("--limit must be between 1 and 1000"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(err)
	}
	defer st.Close()

	events, err := st.Audit(ctx, *limit)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(events)
	}
	for _, e := range events {
		fmt.Fprintf(a.Stdout, "%s %s %v\n", e.TS.Local().Format(time.DateTime), e.Action, e.Detail)
	}
	return 0
}

func writeSecret(path, secret string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; refusing to overwrite a secret", path)
		}
		return fmt.Errorf("create token file: %w", err)
	}
	if _, err := f.WriteString(secret + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("write token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close token file: %w", err)
	}
	return nil
}
