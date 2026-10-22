//go:build windows

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAgentConfig(t *testing.T, hubURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.json")
	data, _ := json.Marshal(map[string]string{
		"hub_url": hubURL, "agent_name": "local-test", "agent_id": "agt_local", "token": "agent-token",
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalChatProxyUsesAgentTokenAndPagination(t *testing.T) {
	var gotPath, gotAuth string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.RequestURI(), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"messages":[{"id":"m1","room":"general","seq":7,"text":"hello"}]}`)
	}))
	defer hub.Close()
	app := &app{
		http: hub.Client(),
		cfg:  config{HubURL: hub.URL, AgentConfig: testAgentConfig(t, hub.URL)},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/chat/messages?room=general&before_seq=9&limit=50", nil)
	rec := httptest.NewRecorder()
	app.handleChatMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer agent-token" {
		t.Fatalf("authorization=%q", gotAuth)
	}
	if gotPath != "/v1/rooms/general/messages?before_seq=9&limit=50" {
		t.Fatalf("path=%q", gotPath)
	}
}

func TestLocalChatRoomsRejectsMissingConfig(t *testing.T) {
	app := &app{http: http.DefaultClient, cfg: config{HubURL: "https://127.0.0.1:1"}}
	rec := httptest.NewRecorder()
	app.handleChatRooms(rec, httptest.NewRequest(http.MethodGet, "/api/chat/rooms", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalAdminRequiresAdminToken(t *testing.T) {
	app := &app{http: http.DefaultClient, cfg: config{HubURL: "https://127.0.0.1:1"}}
	rec := httptest.NewRecorder()
	app.handleAdminAgents(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents", nil))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "admin token") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUninstallRequiresInstallMarker(t *testing.T) {
	app := &app{}
	req := httptest.NewRequest(http.MethodPost, "/api/uninstall", strings.NewReader(`{"confirm":"uninstall"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.handleUninstall(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalSearchProxy(t *testing.T) {
	var gotPath, gotAuth string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.RequestURI(), r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"messages":[]}`)
	}))
	defer hub.Close()
	app := &app{http: hub.Client(), cfg: config{HubURL: hub.URL, AgentConfig: testAgentConfig(t, hub.URL)}}
	rec := httptest.NewRecorder()
	app.handleChatSearch(rec, httptest.NewRequest(http.MethodGet, "/api/chat/search?room=general&q=hello%20world", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer agent-token" || gotPath != "/v1/rooms/general/search?q=hello+world&limit=50" {
		t.Fatalf("auth=%q path=%q", gotAuth, gotPath)
	}
}

func TestLocalUploadProxy(t *testing.T) {
	var gotPath, gotAuth, gotType, gotBody string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		_, _ = io.WriteString(w, `{"id":"fil_1"}`)
	}))
	defer hub.Close()
	app := &app{http: hub.Client(), cfg: config{HubURL: hub.URL, AgentConfig: testAgentConfig(t, hub.URL)}}
	req := httptest.NewRequest(http.MethodPost, "/api/chat/upload", strings.NewReader("file-bytes"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	app.handleChatUpload(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/files" || gotAuth != "Bearer agent-token" || gotType != "multipart/form-data; boundary=x" || gotBody != "file-bytes" {
		t.Fatalf("status=%d path=%q auth=%q type=%q body=%q resp=%s", rec.Code, gotPath, gotAuth, gotType, gotBody, rec.Body.String())
	}
}

func TestAPIMultipartUploadAllowed(t *testing.T) {
	app := &app{cap: "cap-test"}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/chat/upload", strings.NewReader("x"))
	req.Host = "127.0.0.1:18765"
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	req.Header.Set("X-AgentChat-Desktop-Capability", "cap-test")
	rec := httptest.NewRecorder()
	app.api(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalAdminAgentMutations(t *testing.T) {
	var seen []string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer hub.Close()
	app := &app{http: hub.Client(), cfg: config{HubURL: hub.URL}, token: "admin-token"}
	create := httptest.NewRequest(http.MethodPost, "/api/admin/agents", strings.NewReader(`{"name":"new"}`))
	create.Header.Set("Content-Type", "application/json")
	app.handleAdminCreateAgent(httptest.NewRecorder(), create)
	rotate := httptest.NewRequest(http.MethodPost, "/api/admin/agents/agt_1/token", nil)
	rotate.SetPathValue("id", "agt_1")
	app.handleAdminRotateAgent(httptest.NewRecorder(), rotate)
	revoke := httptest.NewRequest(http.MethodDelete, "/api/admin/agents/agt_1", nil)
	revoke.SetPathValue("id", "agt_1")
	app.handleAdminRevokeAgent(httptest.NewRecorder(), revoke)
	want := []string{"POST /v1/admin/agents", "POST /v1/admin/agents/agt_1/token", "DELETE /v1/admin/agents/agt_1"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("seen=%v want=%v", seen, want)
	}
}

func TestLocalAdminProxy(t *testing.T) {
	var gotAuth, gotPath string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.RequestURI()
		_, _ = io.WriteString(w, `{"agents":[]}`)
	}))
	defer hub.Close()
	app := &app{http: hub.Client(), cfg: config{HubURL: hub.URL}, token: "admin-token"}
	rec := httptest.NewRecorder()
	app.handleAdminAgents(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer admin-token" || gotPath != "/v1/admin/agents" {
		t.Fatalf("auth=%q path=%q", gotAuth, gotPath)
	}
}
