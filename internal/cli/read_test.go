package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/config"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func TestCLIExplicitRead(t *testing.T) {
	var calls, status atomic.Int64
	status.Store(http.StatusOK)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/v1/rooms/test-room/read" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("unexpected read request", r.Method, r.URL.Path)
		}
		if code := int(status.Load()); code != 200 {
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(protocol.ErrorResponse{Error: protocol.ErrorBody{Code: "forbidden", Message: "fixture rejection"}})
			return
		}
		var body protocol.MarkReadRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Seq != 0 {
			t.Error("zero read request", body, err)
		}
		json.NewEncoder(w).Encode(protocol.ReadReceipt{Room: "test-room", AgentID: "fixture", ReadSeq: 0})
	}))
	defer ts.Close()
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := config.Save(path, &config.Config{HubURL: ts.URL, AgentName: "fixture", AgentID: "fixture", Token: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := &App{Stdout: &stdout, Stderr: &stderr}
	if c := a.Run([]string{"read", "--config", path, "--room", "test-room", "--seq", "0"}); c != 0 {
		t.Fatal(c, stderr.String())
	}
	if got := stdout.String(); got != "{\"room\":\"test-room\",\"agent_id\":\"fixture\",\"read_seq\":0}\n" {
		t.Fatal("compact JSON response", got)
	}
	for _, args := range [][]string{{}, {"--seq", "-1"}, {"--seq", "0", "unexpected"}, {"--seq", "0", "--room", ""}} {
		before := calls.Load()
		if c := a.Run(append([]string{"read", "--config", path}, args...)); c == 0 || calls.Load() != before {
			t.Fatal("invalid command reached hub", args, c)
		}
	}
	for _, code := range []int64{400, 401, 403, 404} {
		status.Store(code)
		stdout.Reset()
		stderr.Reset()
		if c := a.Run([]string{"read", "--config", path, "--room", "test-room", "--seq", "0"}); c != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "HTTP "+strconv.FormatInt(code, 10)) {
			t.Fatal("HTTP failures use the existing exit code 1", c, stdout.String(), stderr.String())
		}
	}
}
