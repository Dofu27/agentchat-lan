package cli

import (
	"fmt"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdActivity(args []string) int {
	fs := a.newFlagSet("activity")
	room := fs.String("room", protocol.DefaultRoom, "room ID")
	runID := fs.String("run-id", "", "stable run identifier (required)")
	phase := fs.String("phase", "", "started, working, waiting, done, or error (required)")
	title := fs.String("title", "", "short task title (required)")
	detail := fs.String("detail", "", "optional detail")
	progress := fs.Int("progress", -1, "optional progress 0-100")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if strings.TrimSpace(*runID) == "" {
		return a.fail(fmt.Errorf("--run-id is required"))
	}
	if strings.TrimSpace(*phase) == "" {
		return a.fail(fmt.Errorf("--phase is required"))
	}
	if strings.TrimSpace(*title) == "" {
		return a.fail(fmt.Errorf("--title is required"))
	}
	var progressPtr *int
	if *progress >= 0 {
		progressPtr = progress
	}

	act := protocol.Activity{
		RunID:    *runID,
		Phase:    *phase,
		Title:    *title,
		Detail:   *detail,
		Progress: progressPtr,
	}
	if err := act.Validate(); err != nil {
		return a.fail(err)
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	text := act.Title
	if act.Detail != "" {
		text += "\n\n" + act.Detail
	}
	msg, err := newClient(cfg).SendMessage(ctx, *room, protocol.SendMessageRequest{
		Text: text,
		Meta: protocol.MessageMeta{Activity: &act},
	})
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(msg)
	}
	fmt.Fprintf(a.Stdout, "activity %s room=%s seq=%d phase=%s run=%s\n", msg.ID, msg.Room, msg.Seq, act.Phase, act.RunID)
	return 0
}
