package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdCodexBridge(args []string) (exit int) {
	fs := a.newFlagSet("codex-bridge")
	logging := logFlags(fs)
	cfgPath := fs.String("config", "", "agent config file path")
	thread := fs.String("thread", "", "Codex session/thread ID (optional; auto-detect current thread)")
	codexHome := fs.String("codex-home", "", "Codex home containing thread_history_1.sqlite (default CODEX_HOME or ~/.codex)")
	codex := fs.String("codex", envOr("CODEX_BIN", "codex"), "Codex executable")
	messageText := fs.String("message", "", "message text for a manual test (otherwise read message JSON from stdin)")
	waitTurn := fs.Duration("wait-turn", 15*time.Second, "wait for a new Codex turn after queueing; 0 disables the check")
	dedupeWindow := fs.Duration("dedupe-window", 10*time.Minute, "keep a pending message from being queued repeatedly while Codex is slow")
	startApp := fs.Bool("start-app", false, "opt in to starting Codex Desktop if queueing did not produce a turn")
	dryRun := fs.Bool("dry-run", false, "print the prompt and command without invoking Codex")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	cfg, err := config.Load(*cfgPath)
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
	if strings.TrimSpace(*thread) == "" {
		*thread, err = currentCodexThread(*codexHome)
		if err != nil {
			return a.fail(fmt.Errorf("resolve current Codex thread: %w (pass --thread explicitly)", err))
		}
	}

	var msg protocol.Message
	if strings.TrimSpace(*messageText) != "" {
		msg = protocol.Message{Room: protocol.DefaultRoom, Text: *messageText}
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return a.fail(fmt.Errorf("read stdin: %w", err))
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			return a.fail(fmt.Errorf("decode message JSON: %w", err))
		}
	}
	if msg.Meta.Activity != nil {
		return 0
	}
	if cfg.AgentID != "" && msg.From.ID == cfg.AgentID {
		return 0
	}

	agentchat, err := os.Executable()
	if err != nil {
		return a.fail(err)
	}
	cfgAbs, err := filepath.Abs(configPathOrEmpty(*cfgPath))
	if err != nil {
		return a.fail(err)
	}
	prompt := codexBridgePrompt(agentchat, cfgAbs, msg)
	if *dryRun {
		fmt.Fprintln(a.Stdout, prompt)
		return 0
	}

	statePath := filepath.Join(filepath.Dir(cfgAbs), ".agentchat-bridge-state.json")
	state := loadBridgeState(statePath)
	key := ""
	entry := bridgeEntry{}
	alreadyQueued := false
	if msg.ID != "" && *dedupeWindow > 0 && *waitTurn > 0 {
		key = cfg.HubURL + "|" + cfg.AgentID + "|" + *thread + "|" + msg.ID
		entry = state[key]
		if entry.Confirmed {
			a.logEvent("info", "queue_confirmed", msg.Room, cfg.AgentID, msg.Seq, "")
			return 0
		}
		if !entry.Updated.IsZero() && time.Since(entry.Updated) < *dedupeWindow {
			alreadyQueued = true
		}
	}

	var before int64
	if *waitTurn > 0 {
		if alreadyQueued {
			before = entry.Before
		} else {
			before, err = latestTurnStarted(*codexHome, *thread)
			if err != nil {
				return a.fail(fmt.Errorf("inspect Codex turn history before queue: %w", err))
			}
		}
	}
	if !alreadyQueued {
		if key != "" {
			state[key] = bridgeEntry{Before: before, Updated: time.Now()}
			if err := saveBridgeState(statePath, state); err != nil {
				a.logEvent("error", "state_failed", msg.Room, cfg.AgentID, msg.Seq, "")
				fmt.Fprintf(a.Stderr, "codex-bridge: save pending state: %v\n", err)
			}
		}
		a.logEvent("info", "queue_started", msg.Room, cfg.AgentID, msg.Seq, "")
		if a.logs.Err() != nil {
			return a.fail(fmt.Errorf("structured bridge logging failed"))
		}
		cmd := exec.Command(*codex, "queue", "--thread", *thread, "--message", prompt)
		cmd.Stdout = a.Stdout
		cmd.Stderr = a.Stderr
		if err := cmd.Run(); err != nil {
			a.logEvent("error", "queue_failed", msg.Room, cfg.AgentID, msg.Seq, "")
			if key != "" {
				delete(state, key)
				if saveErr := saveBridgeState(statePath, state); saveErr != nil {
					a.logEvent("error", "state_failed", msg.Room, cfg.AgentID, msg.Seq, "")
					fmt.Fprintf(a.Stderr, "codex-bridge: clear failed pending state: %v\n", saveErr)
				}
			}
			return a.fail(fmt.Errorf("codex queue failed"))
		}
	}
	a.logEvent("info", "queue_pending", msg.Room, cfg.AgentID, msg.Seq, "")
	if a.logs.Err() != nil {
		return a.fail(fmt.Errorf("structured bridge logging failed"))
	}
	if *waitTurn <= 0 {
		return 0
	}
	confirmed := func() bool {
		if waitForNewTurn(*codexHome, *thread, before, *waitTurn) {
			if key != "" {
				entry = state[key]
				entry.Confirmed = true
				entry.Updated = time.Now()
				state[key] = entry
				if err := saveBridgeState(statePath, state); err != nil {
					a.logEvent("error", "state_failed", msg.Room, cfg.AgentID, msg.Seq, "")
					fmt.Fprintln(a.Stderr, "codex-bridge: save confirmed state failed")
				}
			}
			a.logEvent("info", "queue_confirmed", msg.Room, cfg.AgentID, msg.Seq, "")
			return true
		}
		return false
	}
	if confirmed() {
		return 0
	}
	if *startApp {
		a.logEvent("info", "app_start", msg.Room, cfg.AgentID, msg.Seq, "")
		if a.logs.Err() != nil {
			return a.fail(fmt.Errorf("structured bridge logging failed"))
		}
		fmt.Fprintln(a.Stderr, "codex-bridge: no new turn yet; starting Codex Desktop")
		if err := startCodexApp(*codex); err != nil {
			a.logEvent("error", "app_failed", msg.Room, cfg.AgentID, msg.Seq, "")
			fmt.Fprintf(a.Stderr, "codex-bridge: start Codex Desktop: %v\n", err)
		} else if confirmed() {
			return 0
		}
	}
	a.logEvent("warn", "queue_pending", msg.Room, cfg.AgentID, msg.Seq, "")
	return a.fail(fmt.Errorf("codex queue accepted but no new turn started within %s; Codex may not be running", *waitTurn))
}

func waitForNewTurn(codexHome, thread string, before int64, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		after, err := latestTurnStarted(codexHome, thread)
		if err == nil && after > before {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func startCodexApp(codex string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("automatic Codex Desktop start is only implemented on Windows")
	}
	cmd := exec.Command(codex, "app")
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func codexBridgePrompt(agentchat, cfgPath string, msg protocol.Message) string {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = "(no text; inspect the message JSON and files)"
	}
	return fmt.Sprintf(`AgentChat message from %s in #%s, seq %d:

%s

Reply in AgentChat LAN, not Feishu. Use this exact binary and config:
"%s" send --room %s --text "..." --config "%s"
For progress use:
"%s" activity --room %s --run-id codex-bridge-%d --phase working --title "..." --config "%s"
Do not echo secrets or tokens. Finish the task and report the result in #%s.`,
		msg.From.Name, msg.Room, msg.Seq, text,
		agentchat, msg.Room, cfgPath,
		agentchat, msg.Room, msg.Seq, cfgPath,
		msg.Room)
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
