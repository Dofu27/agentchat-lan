package cli

import (
	"strings"
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func TestParseDSHJSON(t *testing.T) {
	final, session := parseDSHJSON([]byte(`{"type":"session","sessionId":"session-abc"}
{"type":"text","text":"thinking"}
{"type":"final","text":"reply from dsh"}`))
	if final != "reply from dsh" || session != "session-abc" {
		t.Fatalf("final=%q session=%q", final, session)
	}
}

func TestDSHPromptIdentifiesRoomAndSender(t *testing.T) {
	got := dshPrompt("dsh-main", protocol.Message{
		Room: "general",
		From: protocol.AgentRef{Name: "alice"},
		Text: "@dsh-main please check",
	})
	for _, want := range []string{"dsh-main", "general", "alice", "please check"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q: %s", want, got)
		}
	}
}
