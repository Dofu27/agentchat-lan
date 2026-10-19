package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg := &Config{
		HubURL:    "http://127.0.0.1:8787",
		Token:     "agt_test",
		AgentID:   "agt_123",
		AgentName: "dsh",
		AgentKind: "dsh",
	}
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.HubURL != cfg.HubURL || got.Token != cfg.Token || got.AgentName != cfg.AgentName {
		t.Fatalf("Load() = %#v, want %#v", got, cfg)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt was not set")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	// Windows reports synthetic 0666 for files; the real protection comes from
	// the user-profile ACL. Only assert POSIX permission bits on Unix.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %o, want no group/other bits", info.Mode().Perm())
	}
}

func TestRedacted(t *testing.T) {
	cfg := &Config{Token: "secret", HubURL: "http://x", AgentName: "a"}
	red := cfg.Redacted()
	if red.Token == "secret" || red.Token == "" {
		t.Fatalf("Redacted().Token = %q, want a redaction marker", red.Token)
	}
	if cfg.Token != "secret" {
		t.Fatal("Redacted() mutated the original token")
	}
}

func TestValidate(t *testing.T) {
	cases := []Config{
		{},
		{HubURL: "http://x"},
		{HubURL: "http://x", Token: "t"},
	}
	for i, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("case %d: Validate() = nil, want error", i)
		}
	}
}
