package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/store"
)

type federationConfig struct {
	Peers []federationPeerConfig `json:"peers"`
}

type federationPeerConfig struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Secret       string `json:"secret"`
	RemoteNodeID string `json:"remote_node_id"`
	LocalRoom    string `json:"local_room"`
	RemoteRoom   string `json:"remote_room"`
}

// loadFederationConfig applies the operator-only federation JSON. The plaintext
// secrets stay in memory for outbound requests; SQLite stores only their hash.
func loadFederationConfig(ctx context.Context, st *store.Store, path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read federation config: %w", err)
	}
	var cfg federationConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse federation config: %w", err)
	}
	secrets := map[string]string{}
	for _, peer := range cfg.Peers {
		peer.Name = strings.TrimSpace(peer.Name)
		peer.URL = strings.TrimRight(strings.TrimSpace(peer.URL), "/")
		peer.RemoteNodeID = strings.TrimSpace(peer.RemoteNodeID)
		if peer.Name == "" || peer.URL == "" || len(peer.Secret) < 16 {
			return nil, fmt.Errorf("federation peer %q requires name, url, and a 16+ byte secret", peer.Name)
		}
		u, err := url.Parse(peer.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("federation peer %q has an invalid url", peer.Name)
		}
		if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("federation peer %q must use https outside loopback", peer.Name)
		}
		if peer.RemoteNodeID == "" {
			return nil, fmt.Errorf("federation peer %q requires remote_node_id", peer.Name)
		}
		p, err := st.ConfigureFederationPeer(ctx, peer.Name, peer.URL, peer.Secret, peer.RemoteNodeID)
		if err != nil {
			return nil, fmt.Errorf("configure federation peer %q: %w", peer.Name, err)
		}
		if peer.LocalRoom != "" && peer.RemoteRoom != "" {
			if _, err := st.CreateFederationLink(ctx, peer.LocalRoom, p.ID, peer.RemoteRoom, "config"); err != nil && !errors.Is(err, store.ErrConflict) {
				return nil, fmt.Errorf("configure federation link %q: %w", peer.Name, err)
			}
		}
		secrets[peer.Name] = peer.Secret
	}
	return secrets, nil
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
