package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/logbroker"
)

type processLogFlags struct {
	dir   *string
	bytes *int64
	files *int
	body  *bool
}

func logFlags(fs *flag.FlagSet) processLogFlags {
	bytes := logbroker.DefaultBytes
	files := logbroker.DefaultFiles
	if s := os.Getenv("AGENTCHAT_LOG_MAX_BYTES"); s != "" {
		if v, e := strconv.ParseInt(s, 10, 64); e == nil {
			bytes = v
		}
	}
	if s := os.Getenv("AGENTCHAT_LOG_FILES"); s != "" {
		if v, e := strconv.Atoi(s); e == nil {
			files = v
		}
	}
	return processLogFlags{fs.String("log-dir", os.Getenv("AGENTCHAT_LOG_DIR"), "structured JSONL directory (independent of UI)"), fs.Int64("log-max-bytes", bytes, "bytes per log segment, 64 KiB-64 MiB"), fs.Int("log-files", files, "retained segments per component, 2-32"), fs.Bool("log-body-summary", os.Getenv("AGENTCHAT_LOG_BODY_SUMMARY") == "true", "opt in to character/line counts only; never log message text")}
}
func (f processLogFlags) options(component, fallback string) logbroker.Options {
	dir := *f.dir
	if dir == "" {
		dir = fallback
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		dir = abs
	}
	return logbroker.Options{Dir: dir, Component: component, MaxBytes: *f.bytes, Files: *f.files, BodySummary: *f.body}
}
func (a *App) openProcessLog(f processLogFlags, component, cfgPath string) (func(), error) {
	opts := f.options(component, filepath.Join(filepath.Dir(configPathOrEmpty(cfgPath)), "logs"))
	p, err := logbroker.New(opts)
	if err != nil {
		return nil, fmt.Errorf("structured log unavailable")
	}
	previous, settings := a.logs, a.logSettings
	a.logs = p
	a.logSettings = opts
	return func() {
		p.Write(logbroker.Record{Level: "info", Event: "stopped"})
		a.logs = previous
		a.logSettings = settings
	}, nil
}
func (a *App) logEvent(level, event, room, agent string, seq int64, text string) {
	if a.logs == nil {
		return
	}
	chars, lines := a.logs.Summary(text)
	if text == "" {
		chars = nil
		lines = nil
	}
	_ = a.logs.Write(logbroker.Record{Level: level, Event: event, Room: room, Agent: agent, Seq: seq, BodyChars: chars, BodyLines: lines})
}
func (a *App) cmdLogs(args []string) int {
	if len(args) == 0 {
		return a.fail(fmt.Errorf("usage: logs tail|prepare --dir <absolute path>"))
	}
	fs := a.newFlagSet("logs " + args[0])
	dir := fs.String("dir", "", "fixed log directory")
	component := fs.String("component", "hub", "hub, bridge, or service")
	level := fs.String("level", "", "exact level filter")
	after := fs.String("after", "", "resume cursor")
	limit := fs.Int("limit", 50, "batch size 1-100")
	follow := fs.Bool("follow", false, "continue reading without collecting producer stdout")
	sid := fs.String("user-sid", "", "installation: Windows writer SID (default current user)")
	if code, ok := a.parse(fs, args[1:]); !ok {
		return code
	}
	if !filepath.IsAbs(*dir) || fs.NArg() != 0 {
		return a.fail(fmt.Errorf("--dir must be absolute; no positional arguments"))
	}
	if args[0] == "prepare" {
		if err := os.MkdirAll(*dir, 0700); err != nil {
			return a.fail(fmt.Errorf("prepare logs failed"))
		}
		if err := logbroker.PreparePermissions(*dir, *sid); err != nil {
			return a.fail(fmt.Errorf("prepare log permissions failed"))
		}
		return 0
	}
	if args[0] != "tail" {
		return a.fail(fmt.Errorf("unknown logs command"))
	}
	ctx, cancel := signalContext()
	defer cancel()
	reader := logbroker.Reader{Dir: *dir}
	cursor := *after
	for {
		batch, err := reader.Read(logbroker.Query{Component: *component, Level: *level, After: cursor, Limit: *limit})
		if err != nil {
			return a.fail(fmt.Errorf("read logs failed: %w", err))
		}
		if batch.Cursor != cursor || batch.Gap || !*follow {
			if err := json.NewEncoder(a.Stdout).Encode(batch); err != nil {
				return 1
			}
		}
		cursor = batch.Cursor
		if !*follow {
			return 0
		}
		if !batch.More && !sleepContext(ctx, time.Second) {
			return 0
		}
		if ctx.Err() != nil {
			return 0
		}
	}
}
