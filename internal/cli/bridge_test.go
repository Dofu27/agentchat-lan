package cli

import (
	"testing"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

func TestAgentMentionedForBridge(t *testing.T) {
	tests := []struct {
		name string
		text string
		room string
		want bool
	}{
		{name: "plain room message", text: "hello", room: "general", want: false},
		{name: "name mention", text: "@dsh-main please review", room: "general", want: true},
		{name: "broadcast mention", text: "@all status", room: "general", want: true},
		{name: "local dm", text: "hello", room: "dm_123", want: true},
		{name: "federated dm", text: "hello", room: "fdm_123", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agentMentioned("dsh-main", protocol.Message{Room: tc.room, Text: tc.text})
			if got != tc.want {
				t.Fatalf("agentMentioned = %v, want %v", got, tc.want)
			}
		})
	}
}
