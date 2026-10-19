package contract_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/agentchat-lan/agentchat/internal/protocol"
)

func readJSON(t *testing.T, name string, dst any) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func TestMessageFixture(t *testing.T) {
	var msg protocol.Message
	readJSON(t, "message.json", &msg)

	if msg.ID == "" || msg.Room != protocol.DefaultRoom {
		t.Fatalf("message identity = %#v", msg)
	}
	if msg.From.Name != "codex-creative" || msg.From.Kind != "codex" {
		t.Fatalf("sender = %#v", msg.From)
	}
	if msg.Seq != 42 || len(msg.Files) != 1 || msg.Files[0].Name != "report.md" {
		t.Fatalf("message content = %#v", msg)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0] != "dsh-main" {
		t.Fatalf("mentions = %#v", msg.Mentions)
	}
	if msg.Meta.Intent != protocol.IntentAsk || msg.Meta.Hops != 1 {
		t.Fatalf("meta = %#v", msg.Meta)
	}
}

func TestSendMessageFixture(t *testing.T) {
	var req protocol.SendMessageRequest
	readJSON(t, "send_message.json", &req)

	if req.ClientMsgID == "" || req.Text == "" {
		t.Fatalf("request = %#v", req)
	}
	if len(req.Files) != 1 || len(req.To) != 1 {
		t.Fatalf("attachments/recipients = %#v", req)
	}
}

func TestJoinResponseFixture(t *testing.T) {
	var resp protocol.JoinResponse
	readJSON(t, "join_response.json", &resp)

	if resp.Agent.Name != "dsh-main" || resp.Token == "" {
		t.Fatalf("join response = %#v", resp)
	}
	if len(resp.Rooms) != 1 || resp.Rooms[0].ID != protocol.DefaultRoom {
		t.Fatalf("rooms = %#v", resp.Rooms)
	}
}

func TestErrorFixture(t *testing.T) {
	var resp protocol.ErrorResponse
	readJSON(t, "error.json", &resp)

	if resp.Error.Code != "forbidden" || resp.Error.Message == "" {
		t.Fatalf("error = %#v", resp.Error)
	}
}
