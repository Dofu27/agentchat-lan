package protocol

import (
	"strings"
	"testing"
)

func TestNewIDIsULIDShaped(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 26 {
			t.Fatalf("len(NewID()) = %d, want 26", len(id))
		}
		for _, r := range id {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("NewID() = %q contains invalid character %q", id, r)
			}
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestValidateAgentName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"codex-creative", true},
		{"dsh.main_pc", true},
		{"a", true},
		{"", false},
		{"has space", false},
		{"slash/name", false},
		{strings.Repeat("a", 65), false},
	}
	for _, tt := range tests {
		err := ValidateAgentName(tt.name)
		if (err == nil) != tt.ok {
			t.Fatalf("ValidateAgentName(%q) error = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestMentionNames(t *testing.T) {
	got := MentionNames("@codex-creative and @dsh @codex-creative @missing.")
	want := []string{"codex-creative", "dsh", "missing"}
	if len(got) != len(want) {
		t.Fatalf("MentionNames() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("MentionNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestActivityValidate(t *testing.T) {
	progress := 40
	valid := Activity{RunID: "tls-design", Phase: ActivityWorking, Title: "TLS design", Progress: &progress}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid activity rejected: %v", err)
	}
	if valid.Terminal() {
		t.Fatal("working activity reported terminal")
	}
	done := Activity{RunID: "tls-design", Phase: ActivityDone, Title: "TLS design"}
	if !done.Terminal() {
		t.Fatal("done activity did not report terminal")
	}

	tests := []Activity{
		{RunID: "bad run", Phase: ActivityWorking, Title: "x"},
		{RunID: "run", Phase: "unknown", Title: "x"},
		{RunID: "run", Phase: ActivityWorking, Title: ""},
		{RunID: "run", Phase: ActivityWorking, Title: strings.Repeat("x", MaxActivityTitleBytes+1)},
		{RunID: "run", Phase: ActivityWorking, Title: "x", Progress: intPtr(101)},
	}
	for _, tt := range tests {
		if err := tt.Validate(); err == nil {
			t.Fatalf("invalid activity accepted: %#v", tt)
		}
	}
}

func intPtr(v int) *int { return &v }

func TestNewTokenPrefix(t *testing.T) {
	tok := NewToken("agt")
	if !strings.HasPrefix(tok, "agt_") {
		t.Fatalf("NewToken() = %q, want agt_ prefix", tok)
	}
	if len(tok) < 20 {
		t.Fatalf("NewToken() = %q, too short", tok)
	}
}
