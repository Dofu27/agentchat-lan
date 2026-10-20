package cli

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

type joinOutput struct {
	Agent  protocol.Agent  `json:"agent"`
	Rooms  []protocol.Room `json:"rooms"`
	Config string          `json:"config"`
	Notes  []string        `json:"notes,omitempty"`
}

func (a *App) cmdJoin(args []string) int {
	args = moveURLArgToEnd(args)
	fs := a.newFlagSet("join")
	name := fs.String("name", "", "agent name (required)")
	kind := fs.String("kind", "generic", "agent kind, e.g. codex, claude, dsh")
	code := fs.String("code", "", "one-time invite code")
	caFile := fs.String("ca-file", "", "TLS CA bundle PEM file (HTTPS only; mutually exclusive with --fingerprint)")
	caURL := fs.String("ca-url", "", "download the CA PEM over HTTPS and save it next to the config")
	caSHA := fs.String("ca-sha256", "", "expected SHA-256 of the downloaded CA (hex; requires --ca-url)")
	fingerprint := fs.String("fingerprint", "", "leaf DER sha256:<hex> pin in addition to system-root verification (HTTPS only)")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	var caps stringList
	fs.Var(&caps, "cap", "capability (repeatable)")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(a.Stderr, "usage: agentchat join <hub-url> --name <name> [--code <invite>] [--ca-file <pem> | --ca-url <https-url>]")
		return 2
	}
	if *name == "" {
		return a.fail(fmt.Errorf("--name is required"))
	}
	var emptyTrustFlag bool
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "ca-file" || f.Name == "fingerprint") && f.Value.String() == "" {
			emptyTrustFlag = true
		}
	})
	if emptyTrustFlag {
		return a.fail(fmt.Errorf("--ca-file and --fingerprint require non-empty values when specified"))
	}
	if *caFile != "" && *caURL != "" {
		return a.fail(fmt.Errorf("--ca-file and --ca-url are mutually exclusive"))
	}
	if *caSHA != "" && *caURL == "" {
		return a.fail(fmt.Errorf("--ca-sha256 requires --ca-url"))
	}

	hubURL := fs.Arg(0)
	if *caURL != "" {
		dest := filepath.Join(filepath.Dir(configPathOrEmpty(*cfgPath)), "agentchat-lan-ca.crt")
		if err := downloadCA(*caURL, *caSHA, dest); err != nil {
			return a.fail(err)
		}
		*caFile = dest
		fmt.Fprintf(a.Stdout, "CA saved: %s\n", dest)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	if *caFile != "" {
		absolute, err := filepath.Abs(*caFile)
		if err != nil {
			return a.fail(fmt.Errorf("resolve CA file: %w", err))
		}
		*caFile = absolute
	}
	trust := &config.Config{HubURL: hubURL, TLSCAFile: *caFile, TLSFingerprint: *fingerprint}
	c, err := a.newClient(trust)
	if err != nil {
		return a.fail(err)
	}
	defer c.HTTP.CloseIdleConnections()
	resp, err := c.Join(ctx, protocol.JoinRequest{
		InviteCode:   *code,
		Name:         *name,
		Kind:         *kind,
		Capabilities: caps,
	})
	if err != nil {
		return a.fail(err)
	}

	path := configPathOrEmpty(*cfgPath)
	cfg := &config.Config{
		HubURL:         hubURL,
		Token:          resp.Token,
		AgentID:        resp.Agent.ID,
		AgentName:      resp.Agent.Name,
		AgentKind:      resp.Agent.Kind,
		TLSCAFile:      *caFile,
		TLSFingerprint: strings.ToLower(*fingerprint),
	}
	for _, room := range resp.Rooms {
		cfg.Rooms = append(cfg.Rooms, room.ID)
	}
	if err := config.Save(path, cfg); err != nil {
		return a.fail(err)
	}

	out := joinOutput{
		Agent:  resp.Agent,
		Rooms:  resp.Rooms,
		Config: path,
		Notes:  []string{"token was saved to the config file and is not printed"},
	}
	if *jsonOut {
		return a.printJSON(out)
	}

	fmt.Fprintf(a.Stdout, "joined %s as %s (%s)\n", resp.Agent.Name, resp.Agent.ID, resp.Agent.Kind)
	fmt.Fprintf(a.Stdout, "config: %s\n", path)
	if len(resp.Rooms) > 0 {
		fmt.Fprint(a.Stdout, "rooms: ")
		for i, room := range resp.Rooms {
			if i > 0 {
				fmt.Fprint(a.Stdout, ", ")
			}
			fmt.Fprint(a.Stdout, room.ID)
		}
		fmt.Fprintln(a.Stdout)
	}
	fmt.Fprintln(a.Stdout, "token saved; it is not printed.")
	return 0
}

// moveURLArgToEnd lets users write both "join URL --name x" and
// "join --name x URL" with Go's flag package.
func moveURLArgToEnd(args []string) []string {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "http://") && !strings.HasPrefix(arg, "https://") {
			continue
		}
		out := make([]string, 0, len(args))
		out = append(out, args[:i]...)
		out = append(out, args[i+1:]...)
		out = append(out, arg)
		return out
	}
	return args
}
