package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestTLSTrustRoundTrip(t *testing.T) {
	for _, trust := range []string{"ca", "pin"} {
		t.Run(trust, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			cfg := &Config{HubURL: "https://localhost:8787", Token: "secret", AgentName: "test"}
			if trust == "ca" {
				cfg.TLSCAFile = "ca.pem"
			} else {
				cfg.TLSFingerprint = "sha256:" + strings.Repeat("a", 64)
			}
			if err := Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if trust == "ca" && got.TLSCAFile != filepath.Join(dir, "ca.pem") {
				t.Fatalf("CA not relative to config: %s", got.TLSCAFile)
			}
			if got.TLSFingerprint != cfg.TLSFingerprint {
				t.Fatal("pin lost")
			}
			if got.Redacted().Token != "***redacted***" {
				t.Fatal("token not redacted")
			}
		})
	}
}

func TestInvalidTLSConfigLoad(t *testing.T) {
	for _, fields := range []string{
		`"hub_url":"http://localhost","tls_ca_file":"ca.pem"`,
		`"hub_url":"http://localhost","tls_fingerprint":"sha256:` + strings.Repeat("a", 64) + `"`,
		`"hub_url":"https://localhost","tls_fingerprint":"bad"`,
		`"hub_url":"https://localhost","tls_ca_file":"ca.pem","tls_fingerprint":"sha256:` + strings.Repeat("a", 64) + `"`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(`{"token":"secret","agent_name":"test",`+fields+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("invalid config accepted: %s", fields)
		}
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
