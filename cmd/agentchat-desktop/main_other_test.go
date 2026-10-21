//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeHubURL(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"https://hub.example:8788/", "https://hub.example:8788", true},
		{"http://127.0.0.1:8787", "http://127.0.0.1:8787", true},
		{"javascript:alert(1)", "", false},
		{"ftp://hub.example", "", false},
		{"https://user:pass@hub.example", "", false},
		{"", "", false},
	} {
		got, err := normalizeHubURL(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Fatalf("normalizeHubURL(%q)=%q,%v want %q,%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestLoadLauncherConfigRejectsUnsafeURL(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"hub_url":"javascript:alert(1)","role":"bridge"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadLauncherConfig(bad)
	if cfg.HubURL != "http://127.0.0.1:8787" || cfg.Role != "bridge" {
		t.Fatalf("unsafe config not rejected: %+v", cfg)
	}
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"hub_url":"https://hub.example:8788/","role":"hub"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = loadLauncherConfig(good)
	if cfg.HubURL != "https://hub.example:8788" || cfg.Role != "hub" {
		t.Fatalf("valid config changed: %+v", cfg)
	}
}
