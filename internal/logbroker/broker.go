// Package logbroker owns bounded JSONL files. Producers never depend on a UI;
// readers reopen files by stable segment/offset cursors and never collect stdout.
package logbroker

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultBytes int64 = 10 << 20
const DefaultFiles = 5
const maxLine = 8192

var ErrCursor = errors.New("invalid log cursor")
var ErrPath = errors.New("unsafe log directory or segment")
var ErrOptions = errors.New("invalid log options")
var publicAgentID = regexp.MustCompile(`^agt_[0-9A-HJKMNP-TV-Z]{26}$`)
var tokenLike = regexp.MustCompile(`^[A-Za-z]{2,12}_[A-Za-z0-9_-]{40,}$`)
var identifier = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)
var route = regexp.MustCompile(`^(GET|POST|DELETE|PUT|PATCH|HEAD|OPTIONS) /[A-Za-z0-9_/{}/.-]*$`)
var events = map[string]bool{"started": true, "stopped": true, "request": true, "internal_error": true, "inbox_failed": true, "received": true, "hook_started": true, "hook_failed": true, "hook_done": true, "ack_failed": true, "queue_started": true, "queue_failed": true, "queue_pending": true, "queue_confirmed": true, "state_failed": true, "app_start": true, "app_failed": true, "service_started": true, "service_stopped": true, "child_failed": true, "unknown": true}

// No arbitrary message/error/body/URL/header field exists. Body counts are an
// explicit opt-in; neither content nor executable/child output is accepted.
type Record struct {
	Version    int       `json:"v"`
	Time       time.Time `json:"time"`
	Level      string    `json:"level"`
	Component  string    `json:"component"`
	Event      string    `json:"event"`
	RequestID  string    `json:"request_id,omitempty"`
	Room       string    `json:"room,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Route      string    `json:"route,omitempty"`
	Status     int       `json:"status,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	Seq        int64     `json:"seq,omitempty"`
	BodyChars  *int      `json:"body_chars,omitempty"`
	BodyLines  *int      `json:"body_lines,omitempty"`
}
type Options struct {
	Dir, Component string
	MaxBytes       int64
	Files          int
	BodySummary    bool
	OnError        func()
}
type Producer struct {
	opts Options
	mu   sync.Mutex
	err  error
}

func validComponent(s string) bool { return s == "hub" || s == "bridge" || s == "service" }
func ValidLevel(s string) bool     { return s == "debug" || s == "info" || s == "warn" || s == "error" }
func cleanID(s string) string {
	if s == "[redacted]" {
		return s
	}
	if publicAgentID.MatchString(s) {
		return s
	}
	if tokenLike.MatchString(s) {
		return "[redacted]"
	}
	if !identifier.MatchString(s) {
		return ""
	}
	lower := strings.ToLower(s)
	for _, p := range []string{"agt_", "adm_", "invite_", "sk-", "eps_", "epb_", "bearer", "secret", "token"} {
		if strings.HasPrefix(lower, p) {
			return "[redacted]"
		}
	}
	return s
}
func sanitize(r Record, component string) Record {
	r.Version = 1
	r.Component = component
	if !ValidLevel(r.Level) {
		r.Level = "info"
	}
	if !events[r.Event] {
		r.Event = "unknown"
	}
	r.RequestID = cleanID(r.RequestID)
	r.Room = cleanID(r.Room)
	r.Agent = cleanID(r.Agent)
	if !route.MatchString(r.Route) || !knownRoutes[r.Route] {
		r.Route = ""
	}
	// Route comes from the mux template, never the actual URL or query.
	if strings.ContainsAny(strings.ToLower(r.Route), "?=\r\n") {
		r.Route = ""
	}
	r.Status = max(0, min(r.Status, 599))
	r.DurationMS = max(0, min(r.DurationMS, 86400000))
	r.Seq = max(0, r.Seq)
	clamp := func(p *int) *int {
		if p == nil {
			return nil
		}
		v := max(0, min(*p, 1<<30))
		return &v
	}
	r.BodyChars = clamp(r.BodyChars)
	r.BodyLines = clamp(r.BodyLines)
	return r
}
func rootOK(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrPath
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if filepath.Clean(abs) != filepath.Clean(real) && (runtime.GOOS != "windows" || !strings.EqualFold(filepath.Clean(abs), filepath.Clean(real))) {
		if runtime.GOOS == "windows" {
			// Windows temp paths are commonly 8.3 aliases (RUNNER~1) that
			// EvalSymlinks expands to a long name. Permit that only when both
			// names resolve to the same directory object.
			a, aerr := os.Stat(abs)
			b, berr := os.Stat(real)
			if aerr == nil && berr == nil && os.SameFile(a, b) {
				return nil
			}
		}
		return ErrPath
	}
	return nil
}
func regular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrPath
	}
	return nil
}

// Permission preparation never changes arbitrary files in a chosen directory.
// Existing managed files receive the same restricted ACL as newly created ones.
func permissionPaths(dir string) ([]string, error) {
	if err := rootOK(dir); err != nil {
		return nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(513)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 512 {
		return nil, ErrPath
	}
	result := []string{dir}
	for _, e := range entries {
		n := e.Name()
		managed := n == ".writer.lock"
		for _, c := range []string{"hub", "bridge", "service"} {
			prefix := c + "-"
			if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".jsonl") {
				v := strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".jsonl")
				_, err := strconv.ParseUint(v, 10, 64)
				managed = managed || len(v) == 20 && err == nil
			}
		}
		if !managed {
			return nil, ErrPath
		}
		path := filepath.Join(dir, n)
		if err := regular(path); err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return result, nil
}
func New(opts Options) (*Producer, error) {
	if opts.MaxBytes == 0 {
		opts.MaxBytes = DefaultBytes
	}
	if opts.Files == 0 {
		opts.Files = DefaultFiles
	}
	if opts.Dir == "" || !validComponent(opts.Component) || opts.MaxBytes < 64<<10 || opts.MaxBytes > 64<<20 || opts.Files < 2 || opts.Files > 32 {
		return nil, ErrOptions
	}
	abs, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	opts.Dir = abs
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	if err = rootOK(abs); err != nil {
		return nil, err
	}
	p := &Producer{opts: opts}
	if err = p.Write(Record{Level: "info", Event: "started"}); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *Producer) Err() error { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
func (p *Producer) fail(err error) error {
	if err != nil {
		p.mu.Lock()
		first := p.err == nil
		if first {
			p.err = err
		}
		p.mu.Unlock()
		if first && p.opts.OnError != nil {
			p.opts.OnError()
		}
	}
	return err
}
func (p *Producer) Summary(text string) (*int, *int) {
	if !p.opts.BodySummary {
		return nil, nil
	}
	chars := len([]rune(text))
	lines := strings.Count(text, "\n") + 1
	return &chars, &lines
}
func segmentName(component string, n uint64) string {
	return fmt.Sprintf("%s-%020d.jsonl", component, n)
}
func segments(dir, component string) ([]uint64, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(513)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 512 {
		return nil, ErrPath
	}
	result := []uint64{}
	prefix := component + "-"
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, prefix) || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		digits := strings.TrimSuffix(strings.TrimPrefix(n, prefix), ".jsonl")
		if len(digits) != 20 {
			continue
		}
		seq, err := strconv.ParseUint(digits, 10, 64)
		if err != nil || seq == 0 {
			continue
		}
		if err = regular(filepath.Join(dir, n)); err != nil {
			return nil, err
		}
		result = append(result, seq)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}
func (p *Producer) Write(r Record) error {
	if p == nil {
		return nil
	}
	if err := rootOK(p.opts.Dir); err != nil {
		return p.fail(err)
	}
	lockPath := filepath.Join(p.opts.Dir, ".writer.lock")
	if _, err := os.Lstat(lockPath); err == nil {
		if err = regular(lockPath); err != nil {
			return p.fail(err)
		}
	}
	unlock, err := acquire(lockPath)
	if err != nil {
		return p.fail(err)
	}
	defer unlock()
	ids, err := segments(p.opts.Dir, p.opts.Component)
	if err != nil {
		return p.fail(err)
	}
	n := uint64(1)
	if len(ids) > 0 {
		n = ids[len(ids)-1]
	}
	path := filepath.Join(p.opts.Dir, segmentName(p.opts.Component, n))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return p.fail(err)
	}
	size, err := repair(f)
	if err != nil {
		f.Close()
		return p.fail(err)
	}
	r = sanitize(r, p.opts.Component)
	r.Time = time.Now().UTC()
	if !p.opts.BodySummary {
		r.BodyChars = nil
		r.BodyLines = nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		f.Close()
		return p.fail(err)
	}
	data = append(data, '\n')
	if len(data) > maxLine {
		f.Close()
		return p.fail(ErrOptions)
	}
	if size+int64(len(data)) > p.opts.MaxBytes {
		f.Close()
		if n == ^uint64(0) {
			return p.fail(ErrOptions)
		}
		n++
		path = filepath.Join(p.opts.Dir, segmentName(p.opts.Component, n))
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return p.fail(err)
		}
		size = 0
	}
	if _, err = f.Seek(size, io.SeekStart); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return p.fail(err)
	}
	ids, err = segments(p.opts.Dir, p.opts.Component)
	if err != nil {
		return p.fail(err)
	}
	for len(ids) > p.opts.Files {
		if err = os.Remove(filepath.Join(p.opts.Dir, segmentName(p.opts.Component, ids[0]))); err != nil {
			return p.fail(err)
		}
		ids = ids[1:]
	}
	return nil
}

// Recover an interrupted final append; no partial JSON line becomes a record.
func repair(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := info.Size()
	if size == 0 {
		return 0, nil
	}
	var last [1]byte
	if _, err = f.ReadAt(last[:], size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	start := max(int64(0), size-maxLine)
	b := make([]byte, size-start)
	if _, err = f.ReadAt(b, start); err != nil {
		return 0, err
	}
	i := strings.LastIndexByte(string(b), '\n')
	end := start + int64(i+1)
	if i < 0 && start > 0 {
		return 0, ErrPath
	}
	return end, f.Truncate(end)
}

type Entry struct {
	Cursor string `json:"cursor"`
	Record Record `json:"record"`
}
type Batch struct {
	Entries []Entry `json:"entries"`
	Cursor  string  `json:"cursor"`
	Gap     bool    `json:"gap"`
	More    bool    `json:"more"`
}
type Query struct {
	Component, Level, After string
	Limit                   int
}
type Reader struct{ Dir string }

func cursor(c string, n uint64, offset int64) string {
	return fmt.Sprintf("v1:%s:%020d:%d", c, n, offset)
}
func parseCursor(c, component string) (uint64, int64, error) {
	parts := strings.Split(c, ":")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != component {
		return 0, 0, ErrCursor
	}
	n, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || n == 0 {
		return 0, 0, ErrCursor
	}
	off, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || off < 0 {
		return 0, 0, ErrCursor
	}
	return n, off, nil
}

// Read is bounded (100 records returned, 256 lines/2 MiB scanned), read-only,
// and tolerates retention deleting segments while a reader is disconnected.
func (reader Reader) Read(q Query) (Batch, error) {
	b := Batch{Entries: []Entry{}}
	if !validComponent(q.Component) || q.Level != "" && !ValidLevel(q.Level) || q.Limit < 1 || q.Limit > 100 {
		return b, ErrOptions
	}
	if err := rootOK(reader.Dir); err != nil {
		return b, err
	}
	ids, err := segments(reader.Dir, q.Component)
	if err != nil {
		return b, err
	}
	var n uint64
	var off int64
	if q.After != "" {
		n, off, err = parseCursor(q.After, q.Component)
		if err != nil {
			return b, err
		}
	}
	if len(ids) == 0 {
		if n != 0 {
			return b, ErrCursor
		}
		return b, nil
	}
	if n == 0 {
		n = ids[0]
	}
	if n < ids[0] {
		b.Gap = true
		n = ids[0]
		off = 0
	}
	if n > ids[len(ids)-1] {
		return b, ErrCursor
	}
	found := false
	scanned := 0
	budget := int64(2 << 20)
	for _, id := range ids {
		if id < n {
			continue
		}
		if id == n {
			found = true
		} else {
			if !found {
				return b, ErrCursor
			}
			off = 0
		}
		path := filepath.Join(reader.Dir, segmentName(q.Component, id))
		if err = regular(path); os.IsNotExist(err) {
			b.Gap = true
			found = true
			off = 0
			continue
		} else if err != nil {
			return b, err
		}
		f, err := openRead(path)
		if os.IsNotExist(err) {
			b.Gap = true
			continue
		}
		if err != nil {
			return b, err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return b, err
		}
		if off > info.Size() {
			f.Close()
			return b, ErrCursor
		}
		if off > 0 {
			var prev [1]byte
			if _, err = f.ReadAt(prev[:], off-1); err != nil || prev[0] != '\n' {
				f.Close()
				return b, ErrCursor
			}
		}
		f.Seek(off, io.SeekStart)
		r := bufio.NewReaderSize(f, maxLine)
		b.Cursor = cursor(q.Component, id, off)
		for {
			line, readErr := r.ReadSlice('\n')
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				f.Close()
				return b, ErrPath
			}
			off += int64(len(line))
			budget -= int64(len(line))
			scanned++
			b.Cursor = cursor(q.Component, id, off)
			var record Record
			if json.Unmarshal(line, &record) == nil && record.Version == 1 && record.Component == q.Component && !record.Time.IsZero() {
				record = sanitize(record, q.Component)
				if q.Level == "" || record.Level == q.Level {
					b.Entries = append(b.Entries, Entry{Cursor: b.Cursor, Record: record})
				}
			}
			if len(b.Entries) >= q.Limit || scanned >= 256 || budget <= 0 {
				b.More = off < info.Size() || id < ids[len(ids)-1]
				f.Close()
				return b, nil
			}
		}
		f.Close()
	}
	return b, nil
}
