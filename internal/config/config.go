// Package config manages the per-agent client configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
)

// Config is stored locally on each agent machine. It contains a bearer token,
// so the file must be readable only by the agent's OS user.
type Config struct {
	HubURL         string           `json:"hub_url"`
	Token          string           `json:"token"`
	AgentID        string           `json:"agent_id"`
	AgentName      string           `json:"agent_name"`
	AgentKind      string           `json:"agent_kind"`
	DownloadDir    string           `json:"download_dir,omitempty"`
	TLSCAFile      string           `json:"tls_ca_file,omitempty"`
	TLSFingerprint string           `json:"tls_fingerprint,omitempty"`
	Rooms          []string         `json:"rooms,omitempty"`
	Cursors        map[string]int64 `json:"cursors,omitempty"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

// DownloadDirectory returns the configured download directory. A relative
// download_dir is resolved against the directory containing the config file;
// when unset, downloads are placed in a sibling "downloads" directory.
func DownloadDirectory(configPath string, cfg *Config) (string, error) {
	if configPath == "" {
		var err error
		configPath, err = DefaultPath()
		if err != nil {
			return "", err
		}
	}
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	base := filepath.Dir(configPath)
	if cfg != nil && cfg.DownloadDir != "" {
		if filepath.IsAbs(cfg.DownloadDir) {
			return filepath.Clean(cfg.DownloadDir), nil
		}
		return filepath.Abs(filepath.Join(base, cfg.DownloadDir))
	}
	return filepath.Join(base, "downloads"), nil
}

// DefaultPath returns the OS-specific config path, or AGENTCHAT_CONFIG.
func DefaultPath() (string, error) {
	if p := os.Getenv("AGENTCHAT_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config dir: %w", err)
	}
	return filepath.Join(dir, "agentchat", "config.json"), nil
}

// Load reads and validates a config file. An empty path uses DefaultPath.
func Load(path string) (*Config, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.TLSCAFile != "" && !filepath.IsAbs(cfg.TLSCAFile) {
		cfg.TLSCAFile, err = filepath.Abs(filepath.Join(filepath.Dir(path), cfg.TLSCAFile))
		if err != nil {
			return nil, fmt.Errorf("resolve TLS CA file: %w", err)
		}
	}
	return &cfg, nil
}

// Save writes the config atomically with user-only permissions.
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	cfg.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentchat-config-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		// On Windows, Rename does not replace an existing destination.
		if runtime.GOOS != "windows" {
			return fmt.Errorf("install config: %w", err)
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace config: %w", removeErr)
		}
		if err := os.Rename(tmpName, path); err != nil {
			return fmt.Errorf("install config: %w", err)
		}
	}
	return nil
}

// Validate checks the fields required by all client commands.
func (c *Config) Validate() error {
	if c.HubURL == "" {
		return errors.New("config: hub_url is required")
	}
	if c.Token == "" {
		return errors.New("config: token is required")
	}
	if c.AgentName == "" {
		return errors.New("config: agent_name is required")
	}
	return client.ValidateTLSOptions(c.HubURL, c.TLSOptions())
}

// Redacted returns a copy safe to print.
func (c *Config) Redacted() Config {
	out := *c
	if out.Token != "" {
		out.Token = "***redacted***"
	}
	return out
}

// TLSOptions returns the trust settings used by every client command.
func (c *Config) TLSOptions() client.TLSOptions {
	return client.TLSOptions{CAFile: c.TLSCAFile, Fingerprint: c.TLSFingerprint}
}
