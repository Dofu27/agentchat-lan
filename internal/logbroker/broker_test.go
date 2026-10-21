package logbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestProducer(t *testing.T, dir string) *Producer {
	t.Helper()
	p, e := New(Options{Dir: dir, Component: "hub", MaxBytes: 64 << 10, Files: 3})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func readAll(t *testing.T, dir, cursor string) Batch {
	t.Helper()
	result := Batch{}
	for i := 0; i < 100; i++ {
		b, e := (Reader{Dir: dir}).Read(Query{Component: "hub", After: cursor, Limit: 100})
		if e != nil {
			t.Fatal(e)
		}
		result.Entries = append(result.Entries, b.Entries...)
		result.Gap = result.Gap || b.Gap
		result.Cursor = b.Cursor
		cursor = b.Cursor
		if !b.More {
			return result
		}
	}
	t.Fatal("unbounded scan")
	return result
}
func TestRotationResumeRestartAndRetentionGap(t *testing.T) {
	dir := t.TempDir()
	p := newTestProducer(t, dir)
	first := readAll(t, dir, "")
	for i := 0; i < 500; i++ {
		if e := p.Write(Record{Event: "request", RequestID: strings.Repeat("x", 80), Agent: strings.Repeat("a", 80), Room: strings.Repeat("r", 80), Route: "POST /v1/rooms/{room}/messages", Seq: int64(i + 1)}); e != nil {
			t.Fatal(e)
		}
	}
	ids, e := segments(dir, "hub")
	if e != nil || len(ids) != 3 {
		t.Fatal(ids, e)
	}
	for _, id := range ids {
		info, e := os.Stat(filepath.Join(dir, segmentName("hub", id)))
		if e != nil || info.Size() > 64<<10 {
			t.Fatal(info, e)
		}
	}
	after := readAll(t, dir, first.Cursor)
	if !after.Gap || len(after.Entries) == 0 {
		t.Fatal("missing retention gap")
	}
	if after.Entries[len(after.Entries)-1].Record.Seq != 500 {
		t.Fatal("lost latest record")
	}
	// Reader/UI is absent while a new producer appends. Its saved cursor resumes.
	p = newTestProducer(t, dir)
	if e = p.Write(Record{Event: "request", Seq: 501}); e != nil {
		t.Fatal(e)
	}
	resumed := readAll(t, dir, after.Cursor)
	if resumed.Gap || len(resumed.Entries) != 2 || resumed.Entries[1].Record.Seq != 501 {
		t.Fatalf("resume %+v", resumed)
	}
}
func TestFilteringReadOnlyPartialRecoveryAndRedaction(t *testing.T) {
	dir := t.TempDir()
	p := newTestProducer(t, dir)
	if e := p.Write(Record{Level: "error", Event: "request", Agent: "agt_SYNTHETIC_NEVER_LOG", RequestID: "adm_SYNTHETIC_NEVER_LOG", Route: "GET /?token=SYNTHETIC_NEVER_LOG"}); e != nil {
		t.Fatal(e)
	}
	ids, _ := segments(dir, "hub")
	path := filepath.Join(dir, segmentName("hub", ids[0]))
	raw, _ := os.ReadFile(path)
	if bytes.Contains(raw, []byte("SYNTHETIC_NEVER_LOG")) {
		t.Fatal("credential leaked")
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString(`{"partial":"raw-secret`)
	f.Close()
	before, _ := os.ReadFile(path)
	b, e := (Reader{Dir: dir}).Read(Query{Component: "hub", Level: "error", Limit: 50})
	if e != nil || len(b.Entries) != 1 {
		t.Fatal(b, e)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(before, again) {
		t.Fatal("reader wrote log")
	}
	newTestProducer(t, dir)
	repaired, _ := os.ReadFile(path)
	if bytes.Contains(repaired, []byte("partial")) {
		t.Fatal("incomplete append not repaired")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(repaired), []byte("\n")) {
		if !json.Valid(line) {
			t.Fatal("invalid JSONL")
		}
	}
	// Reader ignores fields outside the file schema and revalidates metadata.
	var forged map[string]any
	json.Unmarshal(bytes.Split(repaired, []byte("\n"))[0], &forged)
	forged["message"] = "SYNTHETIC_NEVER_LOG"
	forged["token"] = "SYNTHETIC_NEVER_LOG"
	forged["event"] = "SYNTHETIC_NEVER_LOG"
	forged["agent"] = "agt_SYNTHETIC_NEVER_LOG"
	line, _ := json.Marshal(forged)
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write(append(line, '\n'))
	f.Close()
	all := readAll(t, dir, "")
	encoded, _ := json.Marshal(all)
	if bytes.Contains(encoded, []byte("SYNTHETIC_NEVER_LOG")) {
		t.Fatal("reader exposed unsafe fields")
	}
	for _, cursor := range []string{"../../secret", "v1:bridge:00000000000000000001:0", "v1:hub:00000000000000000001:1", "v1:hub:99999999999999999999:0"} {
		if _, e := (Reader{Dir: dir}).Read(Query{Component: "hub", After: cursor, Limit: 50}); !errors.Is(e, ErrCursor) {
			t.Fatalf("cursor %s: %v", cursor, e)
		}
	}
	absent := filepath.Join(dir, "absent")
	if _, e = (Reader{Dir: absent}).Read(Query{Component: "hub", Limit: 50}); e == nil {
		t.Fatal("missing logs accepted")
	}
	if _, e = os.Stat(absent); !os.IsNotExist(e) {
		t.Fatal("reader created directory")
	}
}
func TestBodySummaryIsExplicitAndNeverContent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			dir := t.TempDir()
			p, e := New(Options{Dir: dir, Component: "hub", BodySummary: enabled})
			if e != nil {
				t.Fatal(e)
			}
			chars, lines := p.Summary("secret\nPRIVATE KEY")
			if e = p.Write(Record{Event: "received", BodyChars: chars, BodyLines: lines}); e != nil {
				t.Fatal(e)
			}
			b := readAll(t, dir, "")
			r := b.Entries[1].Record
			if enabled != (r.BodyChars != nil) {
				t.Fatal("summary opt-in ignored")
			}
			raw, _ := json.Marshal(b)
			if bytes.Contains(raw, []byte("PRIVATE KEY")) || bytes.Contains(raw, []byte("secret")) {
				t.Fatal("message body persisted")
			}
		})
	}
}
func TestLogProducerProcess(t *testing.T) {
	if os.Getenv("AGENTCHAT_LOG_TEST_CHILD") != "1" {
		return
	}
	p, e := New(Options{Dir: os.Getenv("AGENTCHAT_LOG_TEST_DIR"), Component: "hub"})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 40; i++ {
		if e = p.Write(Record{Event: "request", Seq: int64(i + 1)}); e != nil {
			t.Fatal(e)
		}
	}
}
func TestMultipleProcessesShareRotationLock(t *testing.T) {
	dir := t.TempDir()
	var children []*exec.Cmd
	var outputs []*bytes.Buffer
	for i := 0; i < 4; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLogProducerProcess$", "-test.timeout=20s")
		cmd.Env = append(os.Environ(), "AGENTCHAT_LOG_TEST_CHILD=1", "AGENTCHAT_LOG_TEST_DIR="+dir)
		out := new(bytes.Buffer)
		cmd.Stdout = out
		cmd.Stderr = out
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		children = append(children, cmd)
		outputs = append(outputs, out)
	}
	for i, cmd := range children {
		if e := cmd.Wait(); e != nil {
			t.Fatalf("producer: %v %s", e, outputs[i].String())
		}
	}
	b := readAll(t, dir, "")
	if len(b.Entries) != 164 {
		t.Fatalf("interleaved/lost writes: %d", len(b.Entries))
	}
}
func TestPathBoundaryAndPrepare(t *testing.T) {
	dir := t.TempDir()
	p := newTestProducer(t, dir)
	if e := PreparePermissions(dir, ""); e != nil {
		t.Fatal(e)
	}
	if e := p.Write(Record{Event: "request"}); e != nil {
		t.Fatal(e)
	}
	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, "unrelated.secret"), []byte("synthetic"), 0600)
	if e := PreparePermissions(bad, ""); e == nil {
		t.Fatal("permissions changed unrelated directory")
	}
	link := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(dir, link); e != nil {
		t.Log("symlink unavailable:", e)
		return
	}
	if _, e := New(Options{Dir: link, Component: "hub"}); e == nil {
		t.Fatal("symlink root accepted")
	}
	if _, e := (Reader{Dir: link}).Read(Query{Component: "hub", Limit: 50}); e == nil {
		t.Fatal("reader followed symlink")
	}
}

func TestProducerFailureIsStickyAndNotifiesOnce(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	p, err := New(Options{Dir: dir, Component: "hub", OnError: func() { calls++ }})
	if err != nil {
		t.Fatal(err)
	}
	// An invalid nonregular managed segment is a real filesystem write failure,
	// without modifying a system disk's capacity or another process's files.
	if err = os.Mkdir(filepath.Join(dir, segmentName("hub", 2)), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = p.Write(Record{Event: "request"}); err == nil {
			t.Fatal("unsafe log file accepted")
		}
	}
	if p.Err() == nil || calls != 1 {
		t.Fatal("failure was not observable", calls)
	}
}
