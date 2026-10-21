package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/blobguard"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

type dshEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

type dshState struct {
	Status     string `json:"status"` // running | done | sent | failed
	Final      string `json:"final,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int
	err error
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.buf.Len()+len(p) > w.max {
		w.err = errors.New("DSH output exceeded bound")
		return 0, w.err
	}
	return w.buf.Write(p)
}

func (w *limitedBuffer) Bytes() []byte { return w.buf.Bytes() }

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
	if !safeMessageID(msg.ID) {
		return a.fail(fmt.Errorf("message id is missing or unsafe; idempotent DSH delivery is impossible"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return a.fail(err)
	}
	if strings.TrimSpace(cfg.AgentID) == "" {
		return a.fail(fmt.Errorf("config has no agent_id; refusing an ambiguous DSH scope"))
	}
	configAbs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return a.fail(err)
	}

	digest := sha256.Sum256([]byte(cfg.HubURL + "\x00" + cfg.AgentID + "\x00" + msg.Room))
	scopeDir := filepath.Join(filepath.Dir(configAbs), "dsh-state", hex.EncodeToString(digest[:8]))
	stateDir := filepath.Join(scopeDir, "messages")
	sessionPath := *sessionFile
	if sessionPath == "" {
		sessionPath = filepath.Join(scopeDir, "session")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return a.fail(err)
	}
	// One lock per hub/agent/room scope. It also protects the session file and
	// the per-message state across concurrent adapter processes.
	release, err := blobguard.Acquire(scopeDir)
	if err != nil {
		return a.fail(fmt.Errorf("lock DSH scope: %w", err))
	}
	defer release()

	statePath := filepath.Join(stateDir, msg.ID+".json")
	state, stateErr := readDSHState(statePath)
	if stateErr != nil {
		return a.fail(fmt.Errorf("read DSH state: %w", stateErr))
	}
	switch state.Status {
	case "sent":
		return 0
	case "running":
		return a.fail(fmt.Errorf("prior DSH run for message %s is incomplete; refusing duplicate tool side effects", msg.ID))
	case "failed":
		return a.fail(fmt.Errorf("prior DSH run for message %s failed (%s); operator must clear state before retry", msg.ID, state.ErrorClass))
	case "done":
		// Reply text is durable; retry only the AgentChat send.
	default:
		state = dshState{Status: "running"}
		if err := writeDSHState(statePath, state); err != nil {
			return a.fail(fmt.Errorf("record DSH run: %w", err))
		}
		sessionID := readDSHSession(sessionPath)
		final, newSession, parseErr := runDSH(*dsh, *profile, *workdir, *timeout, sessionID, dshPrompt(cfg.AgentName, msg))
		if parseErr != nil {
			_ = writeDSHState(statePath, dshState{Status: "failed", ErrorClass: parseErr.Error()})
			return a.fail(parseErr)
		}
		if newSession != "" {
			if err := writeDSHSession(sessionPath, newSession); err != nil {
				_ = writeDSHState(statePath, dshState{Status: "failed", ErrorClass: "session write failed"})
				return a.fail(fmt.Errorf("save DSH session: %w", err))
			}
		}
		state = dshState{Status: "done", Final: final, ClientID: "dsh:" + msg.ID}
		if err := writeDSHState(statePath, state); err != nil {
			return a.fail(fmt.Errorf("save DSH reply: %w", err))
		}
	}
	if *dryRun {
		fmt.Fprintln(a.Stdout, state.Final)
		return 0
	}
	c, err := a.newClient(cfg)
	if err != nil {
		return a.fail(err)
	}
	defer c.HTTP.CloseIdleConnections()
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer sendCancel()
	if _, err := c.SendMessage(sendCtx, msg.Room, protocol.SendMessageRequest{Text: state.Final, ClientMsgID: state.ClientID}); err != nil {
		return a.fail(fmt.Errorf("send DSH reply: %v", err))
	}
	state.Status = "sent"
	if err := writeDSHState(statePath, state); err != nil {
		return a.fail(fmt.Errorf("record DSH delivery: %w", err))
	}
	return 0
}

func runDSH(dsh, profile, workdir string, timeoutSeconds int, sessionID, prompt string) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	runArgs := []string{"--profile", profile, "--json"}
	if sessionID != "" {
		runArgs = append(runArgs, "--session-id", sessionID)
	}
	runArgs = append(runArgs, prompt)
	cmd := exec.CommandContext(ctx, dsh, runArgs...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	stdout := &limitedBuffer{max: 4 << 20}
	stderr := &limitedBuffer{max: 8 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 3 * time.Second
	cleanup, startErr := startDSHProcess(cmd)
	if startErr != nil {
		return "", "", fmt.Errorf("start DSH: %w", startErr)
	}
	runErr := cmd.Wait()
	cleanup()
	if ctx.Err() != nil {
		return "", "", fmt.Errorf("DSH timed out after %ds", timeoutSeconds)
	}
	if runErr != nil {
		return "", "", fmt.Errorf("DSH failed (%s)", safeExitError(runErr))
	}
	if stdout.err != nil {
		return "", "", stdout.err
	}
	return parseDSHJSON(stdout.Bytes())
}

func safeExitError(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit %d", exit.ExitCode())
	}
	return "execution error"
}

func safeMessageID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
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
	if err := regularDSHFile(path); err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writeDSHSession(path, id string) error {
	return writeDSHFile(path, []byte(id+"\n"))
}

func readDSHState(path string) (dshState, error) {
	if err := regularDSHFile(path); err != nil {
		if os.IsNotExist(err) {
			return dshState{}, nil
		}
		return dshState{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return dshState{}, err
	}
	var state dshState
	if err := json.Unmarshal(b, &state); err != nil {
		return dshState{}, err
	}
	return state, nil
}

func writeDSHState(path string, state dshState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeDSHFile(path, b)
}

func regularDSHFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("unsafe DSH state path")
	}
	return nil
}

func writeDSHFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe DSH state target")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func parseDSHJSON(data []byte) (string, string, error) {
	var final, session string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event dshEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return "", "", fmt.Errorf("invalid DSH JSON output")
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
	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("DSH output was truncated or unreadable")
	}
	if strings.TrimSpace(final) == "" {
		return "", "", fmt.Errorf("DSH produced no final text")
	}
	return final, session, nil
}
