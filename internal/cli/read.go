package cli

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdRead(args []string) int {
	fs := a.newFlagSet("read")
	cfgPath := fs.String("config", "", "config file path")
	room := fs.String("room", protocol.DefaultRoom, "room ID")
	seq := fs.Int64("seq", 0, "explicit read sequence (required)")
	if code, ok := a.parse(fs, args); !ok {
		return code
	}
	present := false
	fs.Visit(func(f *flag.Flag) { present = present || f.Name == "seq" })
	if !present || *seq < 0 || *room == "" || fs.NArg() != 0 {
		return a.fail(fmt.Errorf("read requires --seq >= 0, a room, and no positional arguments"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	c, err := a.newClient(cfg)
	if err != nil {
		return a.fail(err)
	}
	defer c.HTTP.CloseIdleConnections()
	ctx, cancel := requestContext(20)
	defer cancel()
	receipt, err := c.MarkRead(ctx, *room, *seq)
	if err != nil {
		return a.fail(err)
	}
	if err = json.NewEncoder(a.Stdout).Encode(receipt); err != nil {
		return a.fail(err)
	}
	return 0
}
