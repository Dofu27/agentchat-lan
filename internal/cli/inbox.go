package cli

import (
	"fmt"
	"time"
)

func (a *App) cmdInbox(args []string) int {
	fs := a.newFlagSet("inbox")
	wait := fs.Int("wait", 0, "long-poll seconds (0-60)")
	limit := fs.Int("limit", 50, "maximum messages (1-200)")
	ack := fs.Bool("ack", false, "advance the server cursor after reading")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if *wait < 0 || *wait > 60 {
		return a.fail(fmt.Errorf("--wait must be between 0 and 60"))
	}
	if *limit < 1 || *limit > 200 {
		return a.fail(fmt.Errorf("--limit must be between 1 and 200"))
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(*wait + 20)
	defer cancel()

	c, err := a.newClient(cfg)
	if err != nil {
		return a.fail(err)
	}
	defer c.HTTP.CloseIdleConnections()
	resp, err := c.Inbox(ctx, *wait, *limit)
	if err != nil {
		return a.fail(err)
	}

	if *ack && len(resp.Messages) > 0 {
		acks := make(map[string]int64)
		for _, m := range resp.Messages {
			if m.Seq > acks[m.Room] {
				acks[m.Room] = m.Seq
			}
		}
		if err := c.Ack(ctx, acks); err != nil {
			return a.fail(err)
		}
	}

	if *jsonOut {
		return a.printJSON(resp)
	}

	if len(resp.Messages) == 0 {
		fmt.Fprintln(a.Stdout, "no new messages")
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
