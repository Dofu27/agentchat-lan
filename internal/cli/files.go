package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentchat-lan/agentchat/internal/protocol"
)

func (a *App) cmdPush(args []string) int {
	fs := a.newFlagSet("push")
	room := fs.String("room", protocol.DefaultRoom, "room ID")
	text := fs.String("text", "", "optional message text")
	cfgPath := fs.String("config", "", "config file path")
	clientMsgID := fs.String("client-msg-id", "", "idempotency key")
	intent := fs.String("intent", "", "ask, answer, notify, or handoff")
	conversation := fs.String("conversation", "", "conversation ID")
	hops := fs.Int("hops", 0, "agent-to-agent hop count")
	jsonOut := fs.Bool("json", false, "print JSON")
	var fileFlags stringList
	fs.Var(&fileFlags, "file", "file to upload (repeatable)")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	files := append([]string(fileFlags), fs.Args()...)
	for _, arg := range fs.Args() {
		if strings.HasPrefix(arg, "-") {
			return a.fail(fmt.Errorf("put flags before file paths, or use --file: %s", arg))
		}
	}
	if len(files) == 0 {
		return a.fail(fmt.Errorf("at least one file is required"))
	}
	if len(files) > protocol.MaxFilesPerMessage {
		return a.fail(fmt.Errorf("at most %d files per message", protocol.MaxFilesPerMessage))
	}
	if *hops < 0 || *hops > protocol.MaxHops {
		return a.fail(fmt.Errorf("--hops must be between 0 and %d", protocol.MaxHops))
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	c := newClient(cfg)
	fileIDs := make([]string, 0, len(files))
	refs := make([]protocol.FileRef, 0, len(files))
	for _, path := range files {
		ref, err := c.UploadFile(ctx, path)
		if err != nil {
			return a.fail(fmt.Errorf("upload %s: %w", path, err))
		}
		fileIDs = append(fileIDs, ref.ID)
		refs = append(refs, *ref)
	}

	msg, err := c.SendMessage(ctx, *room, protocol.SendMessageRequest{
		ClientMsgID: *clientMsgID,
		Text:        *text,
		Files:       fileIDs,
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
		return a.printJSON(map[string]any{
			"message": msg,
			"files":   refs,
		})
	}
	fmt.Fprintf(a.Stdout, "sent %s room=%s seq=%d files=%d\n", msg.ID, msg.Room, msg.Seq, len(refs))
	for _, ref := range refs {
		fmt.Fprintf(a.Stdout, "  %s %s (%d bytes, sha256 %s)\n", ref.ID, ref.Name, ref.Size, ref.SHA256)
	}
	return 0
}

func (a *App) cmdPull(args []string) int {
	args = moveFileIDArgToEnd(args)
	fs := a.newFlagSet("pull")
	out := fs.String("out", "", "destination file or directory (required)")
	force := fs.Bool("force", false, "overwrite an existing file")
	cfgPath := fs.String("config", "", "config file path")
	jsonOut := fs.Bool("json", false, "print JSON")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(a.Stderr, "usage: agentchat pull <file-id> --out <file>")
		return 2
	}
	fileID := fs.Arg(0)
	if *out == "" {
		return a.fail(fmt.Errorf("--out is required"))
	}

	dest := *out
	if info, err := os.Stat(dest); err == nil && info.IsDir() {
		dest = filepath.Join(dest, fileID+".bin")
	}
	if _, err := os.Stat(dest); err == nil {
		if !*force {
			return a.fail(fmt.Errorf("%s already exists; pass --force to overwrite", dest))
		}
		if err := os.Remove(dest); err != nil {
			return a.fail(fmt.Errorf("remove %s: %w", dest, err))
		}
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	info, err := newClient(cfg).DownloadFile(ctx, fileID, dest)
	if err != nil {
		return a.fail(err)
	}
	if *jsonOut {
		return a.printJSON(info)
	}
	fmt.Fprintf(a.Stdout, "downloaded %s -> %s\n", fileID, info.Path)
	fmt.Fprintf(a.Stdout, "  name=%s size=%d sha256=%s\n", info.Name, info.Size, info.SHA256)
	return 0
}

func moveFileIDArgToEnd(args []string) []string {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "fil_") {
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
