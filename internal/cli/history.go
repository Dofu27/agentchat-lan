package cli

import (
	"fmt"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdHistory(args []string) int {
	fs := a.newFlagSet("history")
	room := fs.String("room", protocol.DefaultRoom, "room ID")
	after := fs.Int64("after", 0, "return messages after this room sequence")
	limit := fs.Int("limit", 50, "maximum messages (1-200)")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if *limit < 1 || *limit > 200 {
		return a.fail(fmt.Errorf("--limit must be between 1 and 200"))
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	resp, err := newClient(cfg).History(ctx, *room, *after, *limit)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(resp)
	}
	if len(resp.Messages) == 0 {
		fmt.Fprintln(a.Stdout, "no messages")
		return 0
	}
	for _, m := range resp.Messages {
		ts := m.TS.Local().Format(time.DateTime)
		fmt.Fprintf(a.Stdout, "[%s] %s in %s#%d: %s\n", ts, m.From.Name, m.Room, m.Seq, m.Text)
		for _, f := range m.Files {
			fmt.Fprintf(a.Stdout, "  file: %s (%d bytes, sha256 %s)\n", f.Name, f.Size, f.SHA256)
		}
	}
	return 0
}
