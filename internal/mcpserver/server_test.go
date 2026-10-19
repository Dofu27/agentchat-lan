package mcpserver

import (
	"path/filepath"
	"testing"
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
