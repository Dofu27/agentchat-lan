package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdCodexBridge(args []string) int {
	fs := a.newFlagSet("codex-bridge")
	cfgPath := fs.String("config", "", "agent config file path")
	thread := fs.String("thread", "", "Codex session/thread ID (optional; auto-detect current thread)")
	codexHome := fs.String("codex-home", "", "Codex home containing thread_history_1.sqlite (default CODEX_HOME or ~/.codex)")
	codex := fs.String("codex", envOr("CODEX_BIN", "codex"), "Codex executable")
	messageText := fs.String("message", "", "message text for a manual test (otherwise read message JSON from stdin)")
	waitTurn := fs.Duration("wait-turn", 15*time.Second, "wait for a new Codex turn after queueing; 0 disables the check")
	dryRun := fs.Bool("dry-run", false, "print the prompt and command without invoking Codex")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
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
	cfgAbs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	prompt := codexBridgePrompt(agentchat, cfgAbs, msg)
	if *dryRun {
		fmt.Fprintln(a.Stdout, prompt)
		return 0
	}

	before, beforeErr := latestTurnStarted(*codexHome, *thread)
	if beforeErr != nil {
		fmt.Fprintf(a.Stderr, "codex-bridge: cannot inspect turn history: %v\n", beforeErr)
	}
	cmd := exec.Command(*codex, "queue", "--thread", *thread, "--message", prompt)
	cmd.Stdout = a.Stdout
	cmd.Stderr = a.Stderr
	if err := cmd.Run(); err != nil {
		return a.fail(fmt.Errorf("codex queue: %w", err))
	}
	if *waitTurn <= 0 || beforeErr != nil {
		return 0
	}
	deadline := time.Now().Add(*waitTurn)
	for time.Now().Before(deadline) {
		after, err := latestTurnStarted(*codexHome, *thread)
		if err == nil && after > before {
			return 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	return a.fail(fmt.Errorf("codex queue accepted but no new turn started within %s; Codex may not be running", *waitTurn))
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
