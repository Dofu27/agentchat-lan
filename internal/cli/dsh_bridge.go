package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

type dshEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

// cmdDshBridge is the runtime adapter for DSH. It invokes DSH's headless
// runner once per AgentChat message, reusing a persistent DSH session id, then
// posts the final text back to the originating room. The AgentChat bridge owns
// delivery/acknowledgement; this command only runs after a mention is accepted.
func (a *App) cmdDshBridge(args []string) int {
	fs := a.newFlagSet("dsh-bridge")
	cfgPath := fs.String("config", "", "agent config file path")
	dsh := fs.String("dsh", "dsh", "DSH executable or wrapper")
	profile := fs.String("profile", "headless", "DSH profile")
	sessionFile := fs.String("session-file", "", "persistent DSH session id file")
	workdir := fs.String("workdir", "", "DSH working directory")
	timeout := fs.Int("timeout", 300, "DSH invocation timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print the DSH reply instead of sending it")
	if codeExit, ok := a.parse(fs, args); !ok {
		return codeExit
	}
	if strings.TrimSpace(*cfgPath) == "" {
		return a.fail(fmt.Errorf("--config is required"))
	}
	if *timeout < 1 || *timeout > 3600 {
		return a.fail(fmt.Errorf("--timeout must be between 1 and 3600"))
	}
	var msg protocol.Message
	if err := json.NewDecoder(os.Stdin).Decode(&msg); err != nil {
		return a.fail(fmt.Errorf("decode AgentChat message: %w", err))
	}
	if strings.TrimSpace(msg.Text) == "" {
		return a.fail(fmt.Errorf("message has no text"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	configAbs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	sessionPath := *sessionFile
	if sessionPath == "" {
		sessionPath = filepath.Join(filepath.Dir(configAbs), "dsh-session")
	}
	sessionID := readDSHSession(sessionPath)
	prompt := dshPrompt(cfg.AgentName, msg)
	runArgs := []string{"--profile", *profile, "--json"}
	if sessionID != "" {
		runArgs = append(runArgs, "--session-id", sessionID)
	}
	runArgs = append(runArgs, prompt)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, *dsh, runArgs...)
	if *workdir != "" {
		cmd.Dir = *workdir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	final, newSession := parseDSHJSON(stdout.Bytes())
	if newSession != "" {
		sessionID = newSession
		if err := writeDSHSession(sessionPath, sessionID); err != nil {
			return a.fail(fmt.Errorf("save DSH session: %w", err))
		}
	}
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = runErr.Error()
		}
		return a.fail(fmt.Errorf("dsh failed: %s", truncateText(detail, 500)))
	}
	if strings.TrimSpace(final) == "" {
		return a.fail(fmt.Errorf("dsh produced no final text"))
	}
	if *dryRun {
		fmt.Fprintln(a.Stdout, final)
		return 0
	}
	c, err := a.newClient(cfg)
	if err != nil {
		return a.fail(err)
	}
	defer c.HTTP.CloseIdleConnections()
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer sendCancel()
	if _, err := c.SendMessage(sendCtx, msg.Room, protocol.SendMessageRequest{Text: final}); err != nil {
		return a.fail(fmt.Errorf("send DSH reply: %w", err))
	}
	return 0
}

func dshPrompt(agentName string, msg protocol.Message) string {
	name := agentName
	if name == "" {
		name = "the agent"
	}
	return fmt.Sprintf("You are %s, a participant in AgentChat LAN room %q. "+
		"Reply to this message from %s. Return only the reply text; do not include a transcript or tool log.\n\n%s",
		name, msg.Room, msg.From.Name, msg.Text)
}

func readDSHSession(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeDSHSession(path, id string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseDSHJSON(data []byte) (string, string) {
	var final, session string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var event dshEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "session":
			if event.SessionID != "" {
				session = event.SessionID
			}
		case "final":
			final = event.Text
		}
	}
	return final, session
}

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
