// Package cli implements the agentchat command-line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/config"
)

// App carries process-level dependencies so commands are testable.
type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
}

// Run executes the CLI and returns a process exit code.
func Run(args []string, version string) int {
	app := &App{
		Version: version,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	return app.Run(args)
}

// Run dispatches a command.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}

	switch args[0] {
	case "join":
		return a.cmdJoin(args[1:])
	case "send":
		return a.cmdSend(args[1:])
	case "activity":
		return a.cmdActivity(args[1:])
	case "agent":
		return a.cmdAgent(args[1:])
	case "service":
		return a.cmdService(args[1:])
	case "codex-bridge":
		return a.cmdCodexBridge(args[1:])
	case "push":
		return a.cmdPush(args[1:])
	case "pull":
		return a.cmdPull(args[1:])
	case "inbox":
		return a.cmdInbox(args[1:])
	case "history":
		return a.cmdHistory(args[1:])
	case "rooms":
		return a.cmdRooms(args[1:])
	case "who":
		return a.cmdWho(args[1:])
	case "me":
		return a.cmdMe(args[1:])
	case "doctor":
		return a.cmdDoctor(args[1:])
	case "config":
		return a.cmdConfig(args[1:])
	case "mock-hub":
		return a.cmdMockHub(args[1:])
	case "mcp":
		return a.cmdMCP(args[1:])
	case "hub":
		return a.cmdHub(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(a.Stdout, "agentchat %s\n", a.Version)
		return 0
	case "help", "--help", "-h":
		a.usage()
		return 0
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n", args[0])
		a.usage()
		return 2
	}
}

func (a *App) usage() {
	fmt.Fprint(a.Stdout, `agentchat — LAN chat and file transfer for AI agents

Usage:
  agentchat <command> [flags]

Commands:
  join       Join a hub with an invite code and save a local config
  send       Send a message to a room
  activity   Publish an agent run progress event
  agent      Run the built-in long-poll agent loop
  service    Install or inspect the agent startup service
  codex-bridge  Forward an AgentChat message to a Codex session
  push       Upload one or more files and send them as a message
  pull       Download a file by ID and verify its SHA-256
  inbox      Read unacknowledged messages (optionally long-poll)
  history    Read room history
  rooms      List rooms
  who        List known agents
  me         Show the authenticated agent and limits
  doctor     Check config, hub, auth, and permissions
  config     Show or locate the local config
  mock-hub   Start the in-memory development hub
  mcp        Start the MCP stdio server
  hub        Run the production hub (init/serve/invite/audit)
  version    Print the version

Examples:
  agentchat join http://127.0.0.1:8787 --name dsh --kind dsh
  agentchat send --room general --text "hello"
  agentchat activity --room general --run-id tls --phase working --title "TLS design"
  agentchat agent --config ./agent.json --exec "agentchat codex-bridge --config ./agent.json"
  agentchat service install --config ./agent.json
  agentchat codex-bridge --config ./agent.json --thread <codex-session-id>
  agentchat push --room general --file ./report.pdf --text "report attached"
  agentchat pull fil_xxx --out ./downloads/report.pdf
  agentchat inbox --wait 30 --json
`)
}

func (a *App) newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	return fs
}

func (a *App) parse(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, false
		}
		return 2, false
	}
	return 0, true
}

func (a *App) fail(err error) int {
	fmt.Fprintf(a.Stderr, "error: %v\n", err)
	return 1
}

func (a *App) printJSON(v any) int {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return a.fail(err)
	}
	return 0
}

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path)
}

func (a *App) newClient(cfg *config.Config) (*client.Client, error) {
	c, err := client.NewWithTLS(cfg.HubURL, cfg.Token, cfg.TLSOptions())
	if err == nil && insecureHTTP(cfg.HubURL) {
		fmt.Fprintln(a.Stderr, "WARNING: non-loopback HTTP hub is insecure; tokens and traffic are unencrypted. Use HTTPS.")
	}
	return c, err
}

func insecureHTTP(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return false
	}
	return !net.ParseIP(host).IsLoopback()
}

func requestContext(seconds int) (context.Context, context.CancelFunc) {
	if seconds < 1 {
		seconds = 30
	}
	return context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
}

func configPathOrEmpty(path string) string {
	if path != "" {
		return path
	}
	p, err := config.DefaultPath()
	if err != nil {
		return ""
	}
	return p
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}
