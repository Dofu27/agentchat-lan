package cli

import (
	"encoding/json"
	"os"
	"time"
)

type bridgeEntry struct {
	Before    int64     `json:"before"`
	Updated   time.Time `json:"updated"`
	Confirmed bool      `json:"confirmed"`
}

type bridgeState map[string]bridgeEntry

func loadBridgeState(path string) bridgeState {
	data, err := os.ReadFile(path)
	if err != nil {
		return bridgeState{}
	}
	var state bridgeState
	if json.Unmarshal(data, &state) != nil || state == nil {
		return bridgeState{}
	}
	return state
}

func saveBridgeState(path string, state bridgeState) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for key, entry := range state {
		if entry.Updated.Before(cutoff) {
			delete(state, key)
		}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
