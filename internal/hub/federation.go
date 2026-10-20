package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/client"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

type FederationPeerOptions struct {
	NodeID  string
	BaseURL string
	Secret  string // operator-provisioned peer secret, never an agent/admin token
	TLS     client.TLSOptions
}
type FederationOptions struct {
	Peer            FederationPeerOptions
	FileLinks       []store.FederationFileLink
	DMConversations []store.FederationDMConversation
	FileDir         string
	notify          func()
	Links           []store.FederationLink
	MaxOutbox       int
	MaxAttempts     int
	RetryBase       time.Duration
	PollInterval    time.Duration
	RequestTimeout  time.Duration
	LeaseDuration   time.Duration
}
type Federation struct {
	store      *store.Store
	nodeID     string
	opts       FederationOptions
	http       *http.Client
	secretHash [32]byte
	blobSlots  chan struct{}
	fileLocks  [64]sync.Mutex
}

// NewFederation is explicit opt-in; the ordinary hub does not call it.
func NewFederation(ctx context.Context, st *store.Store, opts FederationOptions) (*Federation, error) {
	if len(opts.FileLinks) > 0 && opts.FileDir == "" {
		return nil, errors.New("federation files require file directory")
	}
	if opts.MaxOutbox == 0 {
		opts.MaxOutbox = 1000
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 5
	}
	if opts.RetryBase == 0 {
		opts.RetryBase = time.Second
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 250 * time.Millisecond
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 5 * time.Second
	}
	if opts.LeaseDuration == 0 {
		opts.LeaseDuration = 30 * time.Second
	}
	if opts.RetryBase < 10*time.Millisecond || opts.RetryBase > 30*time.Second || opts.PollInterval < 10*time.Millisecond || opts.PollInterval > time.Minute || opts.RequestTimeout < 10*time.Millisecond || opts.RequestTimeout > 10*time.Second {
		return nil, errors.New("invalid bounded federation timing settings")
	}
	if opts.LeaseDuration < 2*opts.RequestTimeout || opts.LeaseDuration > time.Minute {
		return nil, errors.New("federation lease must cover two request timeouts and be at most one minute")
	}
	p := opts.Peer
	if len(p.Secret) < 32 || len(p.Secret) > 4096 || strings.ContainsAny(p.Secret, " \r\n\t") || strings.HasPrefix(p.Secret, "agt_") || strings.HasPrefix(p.Secret, "adm_") {
		return nil, errors.New("federation requires a dedicated peer secret of 32-4096 non-whitespace bytes")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid federation peer base URL")
	}
	if u.Scheme == "http" && !net.ParseIP(u.Hostname()).IsLoopback() {
		return nil, errors.New("federation HTTP is limited to literal loopback addresses; use HTTPS")
	}
	c, err := client.NewWithTLS(p.BaseURL, "", p.TLS)
	if err != nil {
		return nil, err
	}
	c.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sum := sha256.Sum256([]byte(p.Secret))
	id, err := st.ConfigureFederation(ctx, []store.FederationPeer{{NodeID: p.NodeID, BaseURL: p.BaseURL, SecretHash: hex.EncodeToString(sum[:])}}, opts.Links, store.FederationLimits{MaxOutbox: opts.MaxOutbox, MaxAttempts: opts.MaxAttempts})
	if err != nil {
		c.HTTP.CloseIdleConnections()
		return nil, err
	}
	if err = st.ConfigureFederationFiles(ctx, opts.FileLinks); err != nil {
		c.HTTP.CloseIdleConnections()
		return nil, err
	}
	if err = st.ConfigureFederationDM(ctx, p.NodeID, opts.DMConversations); err != nil {
		c.HTTP.CloseIdleConnections()
		return nil, err
	}
	return &Federation{store: st, nodeID: id, opts: opts, http: c.HTTP, secretHash: sum, blobSlots: make(chan struct{}, 2)}, nil
}
func (f *Federation) NodeID() string { return f.nodeID }

func (s *Server) handleFederation(w http.ResponseWriter, r *http.Request) {
	f := s.opts.Federation
	// A node declaration is bound to its independently provisioned secret.
	sum := sha256.Sum256([]byte(bearer(r)))
	if r.Header.Get("X-AgentChat-Node") != f.opts.Peer.NodeID || subtle.ConstantTimeCompare(sum[:], f.secretHash[:]) != 1 {
		writeError(w, 401, "unauthorized", "unknown federation peer or credential", nil)
		return
	}
	if !s.allow("federation:"+f.opts.Peer.NodeID, 120, 20) {
		writeError(w, 429, "rate_limited", "federation peer rate limit", nil)
		return
	}
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	var e store.FederationEnvelope
	if err := dec.Decode(&e); err != nil {
		writeError(w, 400, "invalid_request", "invalid federation envelope", nil)
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "invalid_request", "expected one federation envelope", nil)
		return
	}
	receipt, err := f.store.ImportFederation(r.Context(), f.opts.Peer.NodeID, e)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrFederationConflict):
			writeError(w, 409, "conflict", "conflicting federation delivery", nil)
		case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrInvalidTarget):
			writeError(w, 403, "forbidden", "unregistered origin, room link or invalid payload", nil)
		default:
			s.internalError(w, err)
		}
		return
	}
	if !receipt.Replayed {
		s.broadcast()
	}
	writeJSON(w, 200, receipt)
}

// Run owns a single bounded worker. It can be restarted: inflight work is
// recovered only after its persisted lease expires. Multiple claimers are safe.
func (f *Federation) runV1(ctx context.Context) error {
	defer f.http.CloseIdleConnections()
	timer := time.NewTicker(f.opts.PollInterval)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		j, err := f.store.ClaimFederationJob(ctx, time.Now(), f.opts.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if j != nil {
			delivered, permanent, reason := f.deliver(ctx, *j)
			// Preserve a lease on abrupt cancellation; a restart safely retries it.
			if ctx.Err() != nil {
				return nil
			}
			delay := f.opts.RetryBase * time.Duration(1<<min(j.Attempts-1, 10))
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			if err := f.store.FinishFederationJob(ctx, *j, delivered, permanent, time.Now().Add(delay), reason); err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
}

func (f *Federation) deliver(ctx context.Context, j store.FederationJob) (bool, bool, string) {
	var e store.FederationEnvelope
	if json.Unmarshal([]byte(j.Payload), &e) != nil {
		return false, true, "invalid stored envelope"
	}
	allowed, err := f.store.FederationJobAuthorized(ctx, j, e)
	if err != nil {
		return false, false, "storage unavailable"
	}
	if !allowed || j.PeerID != f.opts.Peer.NodeID || e.OriginNodeID != f.nodeID {
		return false, true, "peer or room link disabled"
	}
	requestCtx, cancel := context.WithTimeout(ctx, f.opts.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, "POST", strings.TrimRight(f.opts.Peer.BaseURL, "/")+"/federation/v1/messages", bytes.NewBufferString(j.Payload))
	if err != nil {
		return false, true, "invalid configured endpoint"
	}
	req.Header.Set("Authorization", "Bearer "+f.opts.Peer.Secret)
	req.Header.Set("X-AgentChat-Node", f.nodeID)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.http.Do(req)
	if err != nil {
		return false, false, "peer transport failure"
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		permanent := resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429
		return false, permanent, fmt.Sprintf("peer HTTP %d", resp.StatusCode)
	}
	var receipt store.FederationReceipt
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&receipt) != nil || receipt.OriginNodeID != e.OriginNodeID || receipt.OriginMessageID != e.OriginMessageID || receipt.LocalMessageID == "" {
		return false, false, "invalid peer receipt"
	}
	return true, false, ""
}
