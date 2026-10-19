package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func (a *App) cmdCodexBridge(args []string) int {
	fs := a.newFlagSet("codex-bridge")
	cfgPath := fs.String("config", "", "agent config file path")
	thread := fs.String("thread", "", "Codex session/thread ID (required)")
	codex := fs.String("codex", envOr("CODEX_BIN", "codex"), "Codex executable")
	messageText := fs.String("message", "", "message text for a manual test (otherwise read message JSON from stdin)")
	dryRun := fs.Bool("dry-run", false, "print the prompt and command without invoking Codex")

	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}
	if strings.TrimSpace(*thread) == "" {
		return a.fail(fmt.Errorf("--thread is required"))
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return a.fail(err)
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

	cmd := exec.Command(*codex, "queue", "--thread", *thread, "--message", prompt)
	cmd.Stdout = a.Stdout
	cmd.Stderr = a.Stderr
	if err := cmd.Run(); err != nil {
		return a.fail(fmt.Errorf("codex queue: %w", err))
	}
	return 0
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
