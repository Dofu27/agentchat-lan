package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdAgent(args []string) (exit int) {
	fs := a.newFlagSet("agent")
	logging := logFlags(fs)
	cfgPath := fs.String("config", "", "config file path")
	wait := fs.Int("wait", 30, "long-poll seconds per inbox request")
	limit := fs.Int("limit", 50, "maximum messages per request")
	execHook := fs.String("exec", "", "optional command run for each message; receives JSON on stdin")
	ack := fs.Bool("ack", true, "advance the server cursor after processing")
	once := fs.Bool("once", false, "process one inbox batch and exit")
	mentionOnly := fs.Bool("mention-only", false, "only invoke the hook for @me, @all, and DM messages")
	typing := fs.Bool("typing", false, "publish typing while the hook runs")
	startNow := fs.Bool("start-now", false, "ack the existing inbox before serving new messages")
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
	closeLog, err := a.openProcessLog(logging, "bridge", *cfgPath)
	if err != nil {
		return a.fail(err)
	}
	defer func() {
		producer := a.logs
		closeLog()
		if producer.Err() != nil {
			exit = 1
		}
	}()
	c, err := a.newClient(cfg)
	if err != nil {
		return a.fail(err)
	}
	defer c.HTTP.CloseIdleConnections()
	ctx, stop := signalContext()
	defer stop()
	if *startNow {
		if !*ack {
			return a.fail(fmt.Errorf("--start-now requires --ack"))
		}
		marker := startNowMarker(*cfgPath, cfg)
		state, exists, err := loadStartNowState(marker)
		if err != nil {
			return a.fail(fmt.Errorf("read start-now state: %w", err))
		}
		if !exists {
			state.Cutoffs, err = captureRoomCutoffs(ctx, c)
			if err != nil {
				return a.fail(fmt.Errorf("capture start-now cutoffs: %w", err))
			}
			if err := saveStartNowState(marker, state); err != nil {
				return a.fail(fmt.Errorf("record start-now cutoffs: %w", err))
			}
		}
		if !state.Done {
			if err := drainCutoffs(ctx, c, *limit, state.Cutoffs); err != nil {
				return a.fail(fmt.Errorf("start-now: %w", err))
			}
			state.Done = true
			if err := saveStartNowState(marker, state); err != nil {
				return a.fail(fmt.Errorf("complete start-now: %w", err))
			}
		}
	}

	for {
		if a.logs.Err() != nil {
			return a.fail(fmt.Errorf("structured bridge logging failed"))
		}
		resp, err := c.Inbox(ctx, *wait, *limit)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			a.logEvent("error", "inbox_failed", "", cfg.AgentID, 0, "")
			fmt.Fprintln(a.Stderr, "agent: inbox failed")
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
		hookFailed := false
		for _, msg := range resp.Messages {
			if cfg.AgentID != "" && msg.From.ID == cfg.AgentID {
				advanceAck(acks, msg)
				continue
			}
			if *mentionOnly && !agentMentioned(cfg.AgentName, msg) {
				advanceAck(acks, msg)
				continue
			}
			a.logEvent("info", "received", msg.Room, msg.From.ID, msg.Seq, msg.Text)
			if a.logs.Err() != nil {
				return a.fail(fmt.Errorf("structured bridge logging failed"))
			}
			hookOK := true
			if strings.TrimSpace(*execHook) == "" {
				if *jsonOut {
					if code := a.printJSON(msg); code != 0 {
						return code
					}
				} else {
					printMessage(a, msg)
				}
			} else if err := runAgentHookWithTyping(ctx, c, *execHook, msg, a, *typing); err != nil {
				hookOK = false
				hookFailed = true
				a.logEvent("error", "hook_failed", msg.Room, msg.From.ID, msg.Seq, "")
				fmt.Fprintf(a.Stderr, "agent: hook failed for seq %d (message remains unacknowledged)\n", msg.Seq)
				if *ack {
					// Do not process later messages in this batch: a later
					// cursor advance would implicitly acknowledge this one.
					break
				}
			}
			if *ack && hookOK {
				advanceAck(acks, msg)
			}
		}
		if a.logs.Err() != nil {
			return a.fail(fmt.Errorf("structured bridge logging failed"))
		}
		if *ack && len(acks) > 0 {
			ackCtx, cancel := requestContext(10)
			err := c.Ack(ackCtx, acks)
			cancel()
			if err != nil {
				a.logEvent("error", "ack_failed", "", cfg.AgentID, 0, "")
				fmt.Fprintln(a.Stderr, "agent: ack failed")
			}
		}
		if *once {
			if hookFailed {
				return 1
			}
			return 0
		}
		if hookFailed && !sleepContext(ctx, 2*time.Second) {
			return 0
		}
	}
}

type startNowState struct {
	Cutoffs map[string]int64 `json:"cutoffs"`
	Done    bool             `json:"done"`
}

func startNowMarker(cfgPath string, cfg *config.Config) string {
	sum := sha256.Sum256([]byte(cfg.HubURL + "\x00" + cfg.AgentID))
	base := filepath.Dir(configPathOrEmpty(cfgPath))
	return filepath.Join(base, ".agentchat-start-now-"+hex.EncodeToString(sum[:8]))
}

func loadStartNowState(path string) (startNowState, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return startNowState{}, false, nil
	}
	if err != nil {
		return startNowState{}, false, err
	}
	var state startNowState
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		// v0.21.8 wrote a plain completion timestamp.
		return startNowState{Done: true}, true, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return startNowState{}, false, err
	}
	if state.Cutoffs == nil {
		state.Cutoffs = map[string]int64{}
	}
	return state, true, nil
}

func saveStartNowState(path string, state startNowState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".start-now-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err = tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err = os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func captureRoomCutoffs(ctx context.Context, c *client.Client) (map[string]int64, error) {
	rooms, err := c.Rooms(ctx)
	if err != nil {
		return nil, err
	}
	cutoffs := make(map[string]int64, len(rooms))
	for _, room := range rooms {
		if room.LatestSeq > 0 {
			cutoffs[room.ID] = room.LatestSeq
			continue
		}
		// Older hubs do not expose latest_seq. Walk history once to find the
		// room's current high-water mark.
		var after int64
		for {
			page, err := c.History(ctx, room.ID, after, 200)
			if err != nil {
				return nil, err
			}
			if len(page.Messages) == 0 {
				break
			}
			after = page.Messages[len(page.Messages)-1].Seq
			if !page.HasMore {
				break
			}
		}
		cutoffs[room.ID] = after
	}
	return cutoffs, nil
}

// drainCutoffs acknowledges only messages at or below the durable per-room
// cutoff. Anything newer stays in the inbox for normal hook processing.
func drainCutoffs(ctx context.Context, c *client.Client, limit int, cutoffs map[string]int64) error {
	for {
		resp, err := c.Inbox(ctx, 0, limit)
		if err != nil {
			return err
		}
		if len(resp.Messages) == 0 {
			return nil
		}
		acks := make(map[string]int64)
		for _, msg := range resp.Messages {
			cutoff, ok := cutoffs[msg.Room]
			if ok && msg.Seq <= cutoff {
				advanceAck(acks, msg)
			}
		}
		if len(acks) == 0 {
			return nil
		}
		if err := c.Ack(ctx, acks); err != nil {
			return err
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

func agentMentioned(name string, msg protocol.Message) bool {
	if strings.HasPrefix(msg.Room, "dm_") || strings.HasPrefix(msg.Room, "fdm_") {
		return true
	}
	for _, mentioned := range protocol.MentionNames(msg.Text) {
		if strings.EqualFold(mentioned, name) || strings.EqualFold(mentioned, "all") || strings.EqualFold(mentioned, "everyone") {
			return true
		}
	}
	return false
}

func sendTyping(c *client.Client, room string, typing bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Typing(ctx, room, typing)
}

func runAgentHookWithTyping(ctx context.Context, c *client.Client, command string, msg protocol.Message, a *App, typing bool) error {
	if !typing {
		return runAgentHook(ctx, command, msg, a)
	}
	sendTyping(c, msg.Room, true)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sendTyping(c, msg.Room, true)
			}
		}
	}()
	err := runAgentHook(ctx, command, msg, a)
	close(done)
	sendTyping(c, msg.Room, false)
	return err
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
	a.logEvent("info", "hook_started", msg.Room, msg.From.ID, msg.Seq, "")
	if a.logs != nil && a.logs.Err() != nil {
		return fmt.Errorf("structured bridge logging failed")
	}
	cmd.Env = append(os.Environ(),
		"AGENTCHAT_MESSAGE_JSON="+string(payload),
		"AGENTCHAT_ROOM="+msg.Room,
		"AGENTCHAT_SEQ="+fmt.Sprint(msg.Seq),
		"AGENTCHAT_FROM="+msg.From.Name,
	)
	if a.logs != nil {
		cmd.Env = append(cmd.Env, "AGENTCHAT_LOG_DIR="+a.logSettings.Dir, "AGENTCHAT_LOG_MAX_BYTES="+fmt.Sprint(a.logSettings.MaxBytes), "AGENTCHAT_LOG_FILES="+fmt.Sprint(a.logSettings.Files), "AGENTCHAT_LOG_BODY_SUMMARY="+fmt.Sprint(a.logSettings.BodySummary))
	}
	err = cmd.Run()
	if err == nil {
		a.logEvent("info", "hook_done", msg.Room, msg.From.ID, msg.Seq, "")
	}
	return err
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
