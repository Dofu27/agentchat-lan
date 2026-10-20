package hub_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func operatorRequest(t *testing.T, base, token, method, path, key string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func TestFederationOperatorDisabledRoutes(t *testing.T) {
	_, ts := openTestHub(t, filepath.Join(t.TempDir(), "db"), hub.Options{})
	for _, p := range []string{"/v1/admin/federation", "/v1/admin/federation/outbox", "/v1/admin/federation/outbox/x/retry"} {
		for _, method := range []string{"GET", "POST"} {
			code, _ := operatorRequest(t, ts.URL, "", method, p, "key")
			if code != 404 {
				t.Errorf("disabled %s %s returned%d", method, p, code)
			}
		}
	}
}

func TestFederationOperatorAuthRedactionAndRetry(t *testing.T) {
	ctx := context.Background()
	a, b := newFederationNode(t), newFederationNode(t)
	a.start(t, b)
	admin, _, err := a.st.EnsureAdminToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sender := nodeReader(t, a, "operator-sender")
	if _, err = sender.SendMessage(ctx, "general", protocol.SendMessageRequest{Text: "sensitive payload never return"}); err != nil {
		t.Fatal(err)
	}
	job, err := a.st.ClaimFederationJob(ctx, time.Now(), time.Minute)
	if err != nil || job == nil {
		t.Fatalf("job %v %v", job, err)
	}
	if err = a.st.FinishFederationJob(ctx, *job, false, true, time.Now(), "peer transport failure"); err != nil {
		t.Fatal(err)
	}
	retryPath := "/v1/admin/federation/outbox/" + job.ID + "/retry"
	for _, tc := range []struct{ method, path string }{{"GET", "/v1/admin/federation"}, {"GET", "/v1/admin/federation/outbox"}, {"POST", retryPath}} {
		for _, token := range []string{"", "not-admin", sender.Token, federationTestSecret} {
			code, _ := operatorRequest(t, a.url, token, tc.method, tc.path, "key")
			if code != 401 {
				t.Fatalf("unauthorized %s %s returned%d", tc.method, tc.path, code)
			}
		}
	}
	for _, path := range []string{"/v1/admin/federation", "/v1/admin/federation/outbox?state=dead&limit=50"} {
		code, raw := operatorRequest(t, a.url, admin, "GET", path, "")
		if code != 200 {
			t.Fatalf("%d %s", code, raw)
		}
		for _, bad := range []string{federationTestSecret, "sensitive payload never return", "secret_hash", "payload\"", "lease_token"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("leak %q", bad)
			}
		}
	}
	for _, path := range []string{"/v1/admin/federation/outbox?state=oops", "/v1/admin/federation/outbox?limit=0", "/v1/admin/federation/outbox?limit=101", "/v1/admin/federation/outbox?limit=x"} {
		code, _ := operatorRequest(t, a.url, admin, "GET", path, "")
		if code != 400 {
			t.Fatalf("invalid query %s returned%d", path, code)
		}
	}
	code, _ := operatorRequest(t, a.url, admin, "POST", retryPath, "")
	if code != 400 {
		t.Fatalf("missing key %d", code)
	}
	for i := 0; i < 2; i++ {
		code, raw := operatorRequest(t, a.url, admin, "POST", retryPath, "stable-request")
		var out struct {
			Replayed bool `json:"replayed"`
		}
		if code != 200 || json.Unmarshal([]byte(raw), &out) != nil || out.Replayed != (i == 1) {
			t.Fatalf("retry%d %d %s", i, code, raw)
		}
	}
	code, _ = operatorRequest(t, a.url, admin, "POST", retryPath, "different-request")
	if code != 409 {
		t.Fatalf("pending retry %d", code)
	}
	code, _ = operatorRequest(t, a.url, admin, "POST", "/v1/admin/federation/outbox/unknown/retry", "key")
	if code != 404 {
		t.Fatalf("unknown %d", code)
	}
	events, err := a.st.Audit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Action == "federation.outbox_retried" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("audit count%d", count)
	}
	// Persisted peers do not register operations after a default-off restart.
	ts := httptest.NewServer(hub.New(a.st, hub.Options{}).Handler())
	defer ts.Close()
	code, _ = operatorRequest(t, ts.URL, admin, "POST", retryPath, "stable-request")
	if code != 404 {
		t.Fatalf("persisted config enabled route %d", code)
	}
	for _, path := range []string{"/admin", "/static/admin.js"} {
		code, raw := operatorRequest(t, a.url, "", "GET", path, "")
		if code != 200 || strings.Contains(raw, federationTestSecret) || strings.Contains(raw, admin) || strings.Contains(raw, "sensitive payload never return") {
			t.Fatal("UI leaked runtime secret or failed")
		}
	}
}
