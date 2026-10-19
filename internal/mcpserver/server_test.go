package mcpserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/mockhub"
	"github.com/Dofu27/agentchat-lan/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
)

func TestNormalizedLimit(t *testing.T) {
	if got, err := normalizedLimit(0); err != nil || got != 50 {
		t.Fatalf("normalizedLimit(0) = %d, %v", got, err)
	}
	if got, err := normalizedLimit(200); err != nil || got != 200 {
		t.Fatalf("normalizedLimit(200) = %d, %v", got, err)
	}
	for _, bad := range []int{-1, 201} {
		if _, err := normalizedLimit(bad); err == nil {
			t.Fatalf("normalizedLimit(%d) = nil error, want error", bad)
		}
	}
}

func TestEnsureInside(t *testing.T) {
	root := t.TempDir()
	if err := ensureInside(root, filepath.Join(root, "file.bin")); err != nil {
		t.Fatalf("inside path rejected: %v", err)
	}
	if err := ensureInside(root, root); err == nil {
		t.Fatal("root itself was accepted")
	}
	if err := ensureInside(root, filepath.Join(root, "..", "outside.bin")); err == nil {
		t.Fatal("outside path was accepted")
	}
}

func TestMCPToolsOverInMemoryTransport(t *testing.T) {
	hub := httptest.NewServer(mockhub.New().Handler())
	defer hub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	join, err := client.New(hub.URL, "").Join(ctx, protocol.JoinRequest{Name: "mcp-test", Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(client.New(hub.URL, join.Token), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agentchat-test", Version: "0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 8 {
		t.Fatalf("tool count = %d, want 8", len(tools.Tools))
	}

	rooms, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "chat_rooms"})
	if err != nil {
		t.Fatalf("chat_rooms: %v", err)
	}
	if rooms.IsError {
		t.Fatalf("chat_rooms returned tool error: %#v", rooms.Content)
	}

	who, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "chat_who"})
	if err != nil {
		t.Fatalf("chat_who: %v", err)
	}
	if who.IsError {
		t.Fatalf("chat_who returned tool error: %#v", who.Content)
	}

	activity, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "chat_activity",
		Arguments: map[string]any{
			"room": "general", "run_id": "mcp-activity", "phase": "done", "title": "MCP activity",
		},
	})
	if err != nil {
		t.Fatalf("chat_activity: %v", err)
	}
	if activity.IsError {
		t.Fatalf("chat_activity returned tool error: %#v", activity.Content)
	}
}
