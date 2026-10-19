package cli

import (
	"fmt"
	"strings"

	"github.com/agentchat-lan/agentchat/internal/config"
	"github.com/agentchat-lan/agentchat/internal/protocol"
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
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	var caps stringList
	fs.Var(&caps, "cap", "capability (repeatable)")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(a.Stderr, "usage: agentchat join <hub-url> --name <name> [--code <invite>]")
		return 2
	}
	if *name == "" {
		return a.fail(fmt.Errorf("--name is required"))
	}

	hubURL := fs.Arg(0)
	ctx, cancel := requestContext(30)
	defer cancel()

	c := newClient(&config.Config{HubURL: hubURL})
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
		HubURL:    hubURL,
		Token:     resp.Token,
		AgentID:   resp.Agent.ID,
		AgentName: resp.Agent.Name,
		AgentKind: resp.Agent.Kind,
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
