package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdAgent(args []string) int {
	fs := a.newFlagSet("agent")
	cfgPath := fs.String("config", "", "config file path")
	wait := fs.Int("wait", 30, "long-poll seconds per inbox request")
	limit := fs.Int("limit", 50, "maximum messages per request")
	execHook := fs.String("exec", "", "optional command run for each message; receives JSON on stdin")
	ack := fs.Bool("ack", true, "advance the server cursor after processing")
	once := fs.Bool("once", false, "process one inbox batch and exit")
	jsonOut := fs.Bool("json", false, "print each message as JSON when no --exec is set")

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
	c := newClient(cfg)
	ctx, stop := signalContext()
	defer stop()

	for {
		resp, err := c.Inbox(ctx, *wait, *limit)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			fmt.Fprintf(a.Stderr, "agent: inbox error: %v\n", err)
			if *once {
				return 1
			}
			if !sleepContext(ctx, 2*time.Second) {
				return 0
			}
			continue
		}
		if len(resp.Messages) == 0 {
			if *once {
				return 0
			}
			continue
		}

		acks := make(map[string]int64)
		for _, msg := range resp.Messages {
			if cfg.AgentID != "" && msg.From.ID == cfg.AgentID {
				advanceAck(acks, msg)
				continue
			}
			if strings.TrimSpace(*execHook) == "" {
				if *jsonOut {
					if code := a.printJSON(msg); code != 0 {
						return code
					}
				} else {
					printMessage(a, msg)
				}
			} else if err := runAgentHook(ctx, *execHook, msg, a); err != nil {
				fmt.Fprintf(a.Stderr, "agent: hook failed for seq %d: %v\n", msg.Seq, err)
			}
			if *ack {
				advanceAck(acks, msg)
			}
		}
		if *ack && len(acks) > 0 {
			ackCtx, cancel := requestContext(10)
			err := c.Ack(ackCtx, acks)
			cancel()
			if err != nil {
				fmt.Fprintf(a.Stderr, "agent: ack error: %v\n", err)
			}
		}
		if *once {
			return 0
		}
	}
}

func advanceAck(acks map[string]int64, msg protocol.Message) {
	if msg.Seq > acks[msg.Room] {
		acks[msg.Room] = msg.Seq
	}
}

func printMessage(a *App, msg protocol.Message) {
	fmt.Fprintf(a.Stdout, "[%s] %s in %s#%d: %s\n", msg.TS.Local().Format("2006-01-02 15:04:05"), msg.From.Name, msg.Room, msg.Seq, msg.Text)
}

func runAgentHook(ctx context.Context, command string, msg protocol.Message, a *App) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = a.Stdout
	cmd.Stderr = a.Stderr
	cmd.Env = append(os.Environ(),
		"AGENTCHAT_MESSAGE_JSON="+string(payload),
		"AGENTCHAT_ROOM="+msg.Room,
		"AGENTCHAT_SEQ="+fmt.Sprint(msg.Seq),
		"AGENTCHAT_FROM="+msg.From.Name,
	)
	return cmd.Run()
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
