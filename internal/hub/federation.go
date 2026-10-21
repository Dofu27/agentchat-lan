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
	Peers           []FederationPeerOptions
	FileLinks       []store.FederationFileLink
	DMConversations []store.FederationDMConversation
	TypingLinks     []store.FederationTypingLink
	ReadReceipts    bool
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
	ephemeral     *ephemeralTyping
	store         *store.Store
	nodeID        string
	opts          FederationOptions
	peers         map[string]FederationPeerOptions
	clients       map[string]*http.Client
	secretHashes  map[string][32]byte
	http          *http.Client
	secretHash    [32]byte
	blobSlots     chan struct{}
	deliverySlots chan struct{}
	readSlots     chan struct{}
	fileLocks     [64]sync.Mutex
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
	peers := opts.Peers
	if len(peers) > 0 && opts.Peer.NodeID != "" {
		if len(peers) != 1 || peers[0].NodeID != opts.Peer.NodeID {
			return nil, errors.New("configure either peer or peers, not both")
		}
	}
	if len(peers) == 0 && opts.Peer.NodeID != "" {
		peers = []FederationPeerOptions{opts.Peer}
	}
	if len(peers) == 0 {
		return nil, errors.New("federation requires at least one peer")
	}
	if len(peers) > 1 && (len(opts.DMConversations) > 0 || len(opts.TypingLinks) > 0) {
		return nil, errors.New("multi-peer federation supports text and attachments only")
	}
	peerRows := make([]store.FederationPeer, 0, len(peers))
	clients := make(map[string]*http.Client, len(peers))
	hashes := make(map[string][32]byte, len(peers))
	peerMap := make(map[string]FederationPeerOptions, len(peers))
	for _, p := range peers {
		if p.NodeID == "" || p.BaseURL == "" {
			return nil, errors.New("each federation peer needs node_id and base_url")
		}
		if _, exists := peerMap[p.NodeID]; exists {
			return nil, errors.New("duplicate federation peer node_id")
		}
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
		clients[p.NodeID] = c.HTTP
		hashes[p.NodeID] = sum
		peerMap[p.NodeID] = p
		peerRows = append(peerRows, store.FederationPeer{NodeID: p.NodeID, BaseURL: p.BaseURL, SecretHash: hex.EncodeToString(sum[:])})
	}
	cleanup := func() {
		for _, c := range clients {
			c.CloseIdleConnections()
		}
	}
	id, err := st.ConfigureFederation(ctx, peerRows, opts.Links, store.FederationLimits{MaxOutbox: opts.MaxOutbox, MaxAttempts: opts.MaxAttempts})
	if err != nil {
		cleanup()
		return nil, err
	}
	if err = st.ConfigureFederationFiles(ctx, opts.FileLinks); err != nil {
		cleanup()
		return nil, err
	}
	if err = st.ConfigureFederationDM(ctx, peers[0].NodeID, opts.DMConversations); err != nil {
		cleanup()
		return nil, err
	}
	opts.Peer = peers[0]
	opts.Peers = peers
	f := &Federation{store: st, nodeID: id, opts: opts, peers: peerMap, clients: clients, secretHashes: hashes, http: clients[peers[0].NodeID], secretHash: hashes[peers[0].NodeID], blobSlots: make(chan struct{}, 2), deliverySlots: make(chan struct{}, 8), readSlots: make(chan struct{}, 8)}
	if err = f.initTyping(ctx); err != nil {
		cleanup()
		return nil, err
	}
	return f, nil
}

func (f *Federation) peer(nodeID string) (FederationPeerOptions, bool) {
	p, ok := f.peers[nodeID]
	return p, ok
}
func (f *Federation) NodeID() string { return f.nodeID }

func (s *Server) handleFederation(w http.ResponseWriter, r *http.Request) {
	f := s.opts.Federation
	nodeID := strings.TrimSpace(r.Header.Get("X-AgentChat-Node"))
	peerCfg, ok := f.peer(nodeID)
	if !ok {
		writeError(w, 401, "unauthorized", "unknown federation peer or credential", nil)
		return
	}
	// A node declaration is bound to its independently provisioned secret.
	sum := sha256.Sum256([]byte(bearer(r)))
	expected := f.secretHashes[nodeID]
	if subtle.ConstantTimeCompare(sum[:], expected[:]) != 1 || peerCfg.NodeID != nodeID {
		writeError(w, 401, "unauthorized", "unknown federation peer or credential", nil)
		return
	}
	if !s.allow("federation:"+nodeID, 120, 20) {
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
	receipt, err := f.store.ImportFederation(r.Context(), nodeID, e)
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
	defer func() {
		for _, c := range f.clients {
			c.CloseIdleConnections()
		}
	}()
	peerIDs := make([]string, 0, len(f.peers))
	for peerID := range f.peers {
		peerIDs = append(peerIDs, peerID)
	}
	if len(peerIDs) == 0 {
		peerIDs = append(peerIDs, "")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, len(peerIDs))
	for _, peerID := range peerIDs {
		peerID := peerID
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.runV1Worker(ctx, peerID); err != nil && ctx.Err() == nil {
				select {
				case errs <- err:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}

func (f *Federation) runV1Worker(ctx context.Context, peerID string) error {
	timer := time.NewTicker(f.opts.PollInterval)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Admission precedes Claim: queueing for capacity must not consume a lease.
		select {
		case f.deliverySlots <- struct{}{}:
		case <-ctx.Done():
			return nil
		}
		var j *store.FederationJob
		var err error
		if peerID == "" {
			j, err = f.store.ClaimFederationJob(ctx, time.Now(), f.opts.LeaseDuration)
		} else {
			j, err = f.store.ClaimFederationJobForPeer(ctx, peerID, time.Now(), f.opts.LeaseDuration)
		}
		if err != nil {
			<-f.deliverySlots
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if j != nil {
			delivered, permanent, reason := f.deliver(ctx, *j)
			<-f.deliverySlots
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
		<-f.deliverySlots
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
	peer, ok := f.peer(j.PeerID)
	if !allowed || !ok || e.OriginNodeID != f.nodeID {
		return false, true, "peer or room link disabled"
	}
	requestCtx, cancel := context.WithTimeout(ctx, f.opts.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, "POST", strings.TrimRight(peer.BaseURL, "/")+"/federation/v1/messages", bytes.NewBufferString(j.Payload))
	if err != nil {
		return false, true, "invalid configured endpoint"
	}
	req.Header.Set("Authorization", "Bearer "+peer.Secret)
	req.Header.Set("X-AgentChat-Node", f.nodeID)
	req.Header.Set("Content-Type", "application/json")
	client := f.clients[j.PeerID]
	if client == nil {
		return false, true, "peer client missing"
	}
	resp, err := client.Do(req)
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
