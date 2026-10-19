package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/Dofu27/agentchat-lan/internal/config"
)

func (a *App) cmdRooms(args []string) int {
	fs := a.newFlagSet("rooms")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	rooms, err := newClient(cfg).Rooms(ctx)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(rooms)
	}

	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tNAME\tTOPIC")
	for _, room := range rooms {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", room.ID, room.Kind, room.Name, room.Topic)
	}
	if err := tw.Flush(); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *App) cmdWho(args []string) int {
	fs := a.newFlagSet("who")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	agents, err := newClient(cfg).Agents(ctx)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(agents)
	}

	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tSTATUS\tLAST SEEN\tID")
	for _, agent := range agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			agent.Name, agent.Kind, agent.Status,
			agent.LastSeen.Local().Format("2006-01-02 15:04:05"), agent.ID)
	}
	if err := tw.Flush(); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *App) cmdMe(args []string) int {
	fs := a.newFlagSet("me")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	me, err := newClient(cfg).Me(ctx)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(me)
	}

	fmt.Fprintf(a.Stdout, "agent: %s (%s, %s)\n", me.Agent.Name, me.Agent.ID, me.Agent.Kind)
	fmt.Fprintf(a.Stdout, "rooms: %s\n", strings.Join(me.Rooms, ", "))
	fmt.Fprintf(a.Stdout, "limits: text=%d bytes, files/message=%d, file=%d bytes, hops=%d\n",
		me.Limits.MaxTextBytes, me.Limits.MaxFilesPerMessage, me.Limits.MaxFileSize, me.Limits.MaxHops)
	return 0
}

func (a *App) cmdConfig(args []string) int {
	sub := "show"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	switch sub {
	case "path":
		path, err := config.DefaultPath()
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintln(a.Stdout, path)
		return 0
	case "show":
		fs := a.newFlagSet("config show")
		cfgPath := fs.String("config", "", "config file path")
		codeExit, ok := a.parse(fs, args)
		if !ok {
			return codeExit
		}
		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			return a.fail(err)
		}
		return a.printJSON(cfg.Redacted())
	default:
		fmt.Fprintf(a.Stderr, "unknown config subcommand %q; use show or path\n", sub)
		return 2
	}
}

func (a *App) cmdMCP(args []string) int {
	fmt.Fprintln(a.Stderr, "agentchat mcp is not implemented yet (Codex task C4).")
	return 1
}

func (a *App) cmdHub(args []string) int {
	fmt.Fprintln(a.Stderr, "agentchat hub is not implemented yet (Codex task C1).")
	return 1
}
