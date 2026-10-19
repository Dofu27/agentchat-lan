package cli

import (
	"fmt"
	"strings"

	"github.com/agentchat-lan/agentchat/internal/protocol"
)

func (a *App) cmdSend(args []string) int {
	fs := a.newFlagSet("send")
	room := fs.String("room", protocol.DefaultRoom, "room ID")
	text := fs.String("text", "", "message text (required)")
	replyTo := fs.String("reply-to", "", "message ID being replied to")
	clientMsgID := fs.String("client-msg-id", "", "idempotency key")
	intent := fs.String("intent", "", "ask, answer, notify, or handoff")
	conversation := fs.String("conversation", "", "conversation ID")
	hops := fs.Int("hops", 0, "agent-to-agent hop count")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")
	var to stringList
	fs.Var(&to, "to", "mention an agent by name (repeatable)")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if strings.TrimSpace(*text) == "" {
		return a.fail(fmt.Errorf("--text is required"))
	}
	if *hops < 0 || *hops > protocol.MaxHops {
		return a.fail(fmt.Errorf("--hops must be between 0 and %d", protocol.MaxHops))
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := requestContext(30)
	defer cancel()

	msg, err := newClient(cfg).SendMessage(ctx, *room, protocol.SendMessageRequest{
		ClientMsgID: *clientMsgID,
		Text:        *text,
		ReplyTo:     *replyTo,
		To:          to,
		Meta: protocol.MessageMeta{
			Intent:         *intent,
			ConversationID: *conversation,
			Hops:           *hops,
		},
	})
	if err != nil {
		return a.fail(err)
	}

	if *jsonOut {
		return a.printJSON(msg)
	}
	fmt.Fprintf(a.Stdout, "sent %s room=%s seq=%d\n", msg.ID, msg.Room, msg.Seq)
	return 0
}
