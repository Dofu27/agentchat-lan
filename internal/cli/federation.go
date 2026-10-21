package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/hub"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (a *App) hubFederationID(args []string) int {
	fs := a.newFlagSet("hub federation-id")
	data := fs.String("data", "./data", "data directory")
	if code, ok := a.parse(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		return a.fail(errors.New("unexpected federation-id arguments"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(err)
	}
	defer st.Close()
	id, err := st.FederationIdentity(ctx)
	if err != nil {
		return a.fail(err)
	}
	return a.printJSON(map[string]string{"node_id": id})
}

func (a *App) hubFederationOperations(args []string) int {
	if len(args) == 0 || args[0] != "status" {
		return a.fail(errors.New("usage: agentchat hub federation status --data <dir>"))
	}
	fs := a.newFlagSet("hub federation status")
	data := fs.String("data", "./data", "data directory (must already exist)")
	if code, ok := a.parse(fs, args[1:]); !ok {
		return code
	}
	if fs.NArg() != 0 {
		return a.fail(errors.New("unexpected federation status arguments"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := store.ReadFederationOperatorStatus(ctx, hubDBPath(*data))
	if err != nil {
		return a.fail(errors.New("cannot read federation state; initialize or migrate this data directory with hub first"))
	}
	return a.printJSON(struct {
		Source string `json:"source"`
		store.FederationOperatorStatus
	}{"persisted_state_only", status})
}

// Credentials are read only from an operator-owned file, not stored in SQLite
// or accepted as CLI arguments. All paths are relative to this config file.
func loadFederation(ctx context.Context, st *store.Store, path string, fileDirs ...string) (*hub.Federation, error) {
	var cfg struct {
		Peer struct {
			NodeID     string `json:"node_id"`
			BaseURL    string `json:"base_url"`
			SecretFile string `json:"secret_file"`
			CAFile     string `json:"tls_ca_file"`
		} `json:"peer"`
		Peers []struct {
			NodeID     string `json:"node_id"`
			BaseURL    string `json:"base_url"`
			SecretFile string `json:"secret_file"`
			CAFile     string `json:"tls_ca_file"`
		} `json:"peers"`
		Links []struct {
			PeerID      string `json:"peer_id"`
			Typing      bool   `json:"typing"`
			Attachments bool   `json:"attachments"`
			LinkID      string `json:"link_id"`
			Epoch       int64  `json:"epoch"`
			LocalRoom   string `json:"local_room"`
			RemoteRoom  string `json:"remote_room"`
		} `json:"links"`
		MaxOutbox       int                              `json:"max_outbox"`
		DMConversations []store.FederationDMConversation `json:"dm_conversations"`
		MaxAttempts     int                              `json:"max_attempts"`
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, errors.New("federation config must be a regular file of at most 64 KiB")
	}
	dec := json.NewDecoder(io.LimitReader(file, 64<<10))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&cfg); err != nil {
		return nil, errors.New("invalid federation configuration")
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid federation configuration suffix")
	}
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(filepath.Dir(path), p)
	}
	if len(cfg.Peers) > 0 {
		if cfg.Peer.NodeID != "" || cfg.Peer.BaseURL != "" || cfg.Peer.SecretFile != "" {
			return nil, errors.New("use either peer or peers, not both")
		}
		if len(cfg.DMConversations) > 0 {
			return nil, errors.New("multi-peer federation supports text room links only")
		}
		opts := hub.FederationOptions{MaxOutbox: cfg.MaxOutbox, MaxAttempts: cfg.MaxAttempts}
		known := make(map[string]bool)
		for _, p := range cfg.Peers {
			if p.NodeID == "" || p.BaseURL == "" || p.SecretFile == "" {
				return nil, errors.New("each peer needs node_id, base_url, and secret_file")
			}
			if known[p.NodeID] {
				return nil, errors.New("duplicate federation peer node_id")
			}
			known[p.NodeID] = true
			raw, err := readFederationSecret(resolve(p.SecretFile))
			if err != nil {
				return nil, err
			}
			opts.Peers = append(opts.Peers, hub.FederationPeerOptions{NodeID: p.NodeID, BaseURL: p.BaseURL, Secret: strings.TrimSpace(raw), TLS: client.TLSOptions{CAFile: resolve(p.CAFile)}})
		}
		links := make(map[string]bool)
		for _, l := range cfg.Links {
			if l.Attachments || l.Typing || l.LinkID != "" || l.Epoch != 0 {
				return nil, errors.New("multi-peer federation supports text links only")
			}
			if l.PeerID == "" || !known[l.PeerID] {
				return nil, errors.New("link references an unknown peer")
			}
			key := l.PeerID + "\x00" + l.LocalRoom
			if links[key] {
				return nil, errors.New("duplicate peer/local_room link")
			}
			links[key] = true
			opts.Links = append(opts.Links, store.FederationLink{PeerID: l.PeerID, LocalRoom: l.LocalRoom, RemoteRoom: l.RemoteRoom})
		}
		return hub.NewFederation(ctx, st, opts)
	}
	if cfg.Peer.SecretFile == "" {
		return nil, errors.New("federation peer secret_file is required")
	}
	secret, err := os.Open(resolve(cfg.Peer.SecretFile))
	if err != nil {
		return nil, err
	}
	info, statErr := secret.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > 4098 {
		secret.Close()
		return nil, errors.New("federation secret file is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(secret, 4099))
	secret.Close()
	if err != nil || len(raw) > 4098 {
		return nil, errors.New("cannot read federation secret")
	}
	opts := hub.FederationOptions{Peer: hub.FederationPeerOptions{NodeID: cfg.Peer.NodeID, BaseURL: cfg.Peer.BaseURL, Secret: strings.TrimSpace(string(raw)), TLS: client.TLSOptions{CAFile: resolve(cfg.Peer.CAFile)}}, MaxOutbox: cfg.MaxOutbox, MaxAttempts: cfg.MaxAttempts}
	opts.DMConversations = cfg.DMConversations
	if len(fileDirs) > 0 {
		opts.FileDir = fileDirs[0]
	}
	for _, l := range cfg.Links {
		if l.Attachments {
			opts.FileLinks = append(opts.FileLinks, store.FederationFileLink{PeerID: cfg.Peer.NodeID, LocalRoom: l.LocalRoom, RemoteRoom: l.RemoteRoom, LinkID: l.LinkID, Epoch: l.Epoch})
		} else if !l.Typing && (l.LinkID != "" || l.Epoch != 0) {
			return nil, errors.New("link identity requires attachments=true or typing=true")
		}
		if l.Typing {
			opts.TypingLinks = append(opts.TypingLinks, store.FederationTypingLink{Kind: "room", LinkID: l.LinkID, Epoch: l.Epoch, LocalRoom: l.LocalRoom, RemoteRoom: l.RemoteRoom})
		}
		opts.Links = append(opts.Links, store.FederationLink{PeerID: cfg.Peer.NodeID, LocalRoom: l.LocalRoom, RemoteRoom: l.RemoteRoom})
	}
	return hub.NewFederation(ctx, st, opts)
}

func readFederationSecret(path string) (string, error) {
	secret, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer secret.Close()
	info, err := secret.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4098 {
		return "", errors.New("federation secret file is not a bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(secret, 4099))
	if err != nil || len(raw) > 4098 {
		return "", errors.New("cannot read federation secret")
	}
	return string(raw), nil
}
