package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSaveNewIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	cfg := Config{HubURL: "http://127.0.0.1:8787", Token: "token", AgentName: "agent"}
	if err := SaveNew(path, &cfg); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	second := Config{HubURL: "http://127.0.0.1:8787", Token: "other", AgentName: "other"}
	if err := SaveNew(path, &second); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second SaveNew err=%v, want ErrExist", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatal("existing config was overwritten")
	}
}

func TestSaveNewConcurrentOnlyOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	var wg sync.WaitGroup
	var ok, exists int
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := Config{HubURL: "http://127.0.0.1:8787", Token: "token", AgentName: "agent"}
			err := SaveNew(path, &cfg)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if errors.Is(err, os.ErrExist) {
				exists++
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || exists != 7 {
		t.Fatalf("ok=%d exists=%d, want 1/7", ok, exists)
	}
}
