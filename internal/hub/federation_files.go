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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dofu27/agentchat-lan/internal/blobguard"
	"github.com/Dofu27/agentchat-lan/internal/store"
)

func (s *Server) federationPeer(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	f := s.opts.Federation
	sum := sha256.Sum256([]byte(bearer(r)))
	if f == nil || r.Header.Get("X-AgentChat-Node") != f.opts.Peer.NodeID || subtle.ConstantTimeCompare(sum[:], f.secretHash[:]) != 1 {
		http.Error(w, "unauthorized", 401)
		return false
	}
	return true
}
func (s *Server) handleFederationCapabilities(w http.ResponseWriter, r *http.Request) {
	if !s.federationPeer(w, r) {
		return
	}
	writeJSON(w, 200, map[string]any{"version": 2, "attachments": len(s.opts.Federation.opts.FileLinks) > 0, "max_file_bytes": store.FederationFileSize, "max_message_bytes": store.FederationMessageSize, "max_attachments": 8, "chunk_bytes": store.FederationChunkSize})
}
func (s *Server) handleFederationFiles(w http.ResponseWriter, r *http.Request) {
	if !s.federationPeer(w, r) {
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(s.opts.Federation.opts.RequestTimeout))
	defer controller.SetReadDeadline(time.Time{})
	if !s.allow("federation-files:"+s.opts.Federation.opts.Peer.NodeID, 240, 40) {
		http.Error(w, "rate limited", 429)
		return
	}
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
	dec.DisallowUnknownFields()
	var e store.FederationFileEnvelope
	if dec.Decode(&e) != nil || dec.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid envelope", 400)
		return
	}
	generation := int64(1)
	if raw := r.Header.Get("X-AgentChat-Retry-Generation"); raw != "" {
		var err error
		generation, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			http.Error(w, "invalid retry generation", 400)
			return
		}
	}
	receipt, err := s.store.StageFederationFiles(r.Context(), s.opts.Federation.opts.Peer.NodeID, e, generation)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrFederationConflict):
			http.Error(w, "conflicting delivery", 409)
		case errors.Is(err, store.ErrInvalidTarget):
			http.Error(w, "invalid manifest", 400)
		case errors.Is(err, store.ErrForbidden):
			http.Error(w, "unregistered link", 403)
		case errors.Is(err, store.ErrFederationExpired):
			http.Error(w, "transfer expired", 410)
		case errors.Is(err, store.ErrFederationQuota):
			http.Error(w, "receive budget exhausted", 429)
		default:
			s.internalError(w, err)
		}
		return
	}
	switch receipt.State {
	case "committed":
		writeJSON(w, 200, receipt)
	case "dead":
		writeJSON(w, 410, receipt)
	default:
		writeJSON(w, 202, receipt)
	}
}

// safeFederationPath resolves existing parents as well as the final file. No
// supplied filename/path is ever used; even a planted directory junction must
// remain inside the operator's real blob root. Operators own this directory.
func safeFederationPath(root, relative string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(relative) {
		return "", errors.New("absolute blob path")
	}
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("blob path escape")
	}
	current := realRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			if e = os.Mkdir(current, 0700); e != nil && !os.IsExist(e) {
				return "", e
			}
		} else if e != nil {
			return "", e
		} else if !info.IsDir() {
			break
		}
		resolved, e := filepath.EvalSymlinks(current)
		if e != nil {
			return "", e
		}
		r, e := filepath.Rel(realRoot, resolved)
		if e != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", errors.New("blob symlink escape")
		}
		current = resolved
	}
	// The loop above treats the last nonexistent component as a directory. Use
	// only parent paths here; callers append an internally generated basename.
	return current, nil
}
func federationBlobPath(root, sha string) (string, string, error) {
	if !store.ValidFederationSHA(sha) {
		return "", "", errors.New("invalid hash")
	}
	parent, err := safeFederationPath(root, sha[:2])
	if err != nil {
		return "", "", err
	}
	full := filepath.Join(parent, sha)
	if info, e := os.Lstat(full); e == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", "", errors.New("unsafe blob")
	} else if e != nil && !os.IsNotExist(e) {
		return "", "", e
	}
	return full, filepath.ToSlash(filepath.Join(sha[:2], sha)), nil
}
func (s *Server) handleFederationBlob(w http.ResponseWriter, r *http.Request) {
	if !s.federationPeer(w, r) {
		return
	}
	f := s.opts.Federation
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(f.opts.RequestTimeout))
	defer controller.SetWriteDeadline(time.Time{})
	select {
	case f.blobSlots <- struct{}{}:
		defer func() { <-f.blobSlots }()
	default:
		http.Error(w, "blob concurrency limit", 429)
		return
	}
	epoch, _ := strconv.ParseInt(r.URL.Query().Get("epoch"), 10, 64)
	generation := int64(1)
	if raw := r.URL.Query().Get("generation"); raw != "" {
		generation, _ = strconv.ParseInt(raw, 10, 64)
	}
	b, err := s.store.FederationBlobGrant(r.Context(), f.opts.Peer.NodeID, r.URL.Query().Get("origin_node_id"), r.URL.Query().Get("origin_message_id"), r.URL.Query().Get("link_id"), epoch, r.PathValue("sha"), generation)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, _, err := federationBlobPath(f.opts.FileDir, b.Ref.SHA256)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != b.Ref.Size {
		http.NotFound(w, r)
		return
	}
	tag := "\"" + b.Ref.SHA256 + "\""
	w.Header().Set("ETag", tag)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == "HEAD" {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		w.WriteHeader(200)
		return
	}
	start, end, ok := federationRange(r.Header.Get("Range"), info.Size())
	if !ok || r.Header.Get("If-Range") != tag {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size()))
		w.WriteHeader(416)
		return
	}
	// Bound each authenticated request, independently of HTTP client's behavior.
	if !s.allow("federation-blob:"+f.opts.Peer.NodeID, 240, 20) {
		http.Error(w, "rate limited", 429)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, info.Size()))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(206)
	_, _ = io.CopyN(w, io.NewSectionReader(file, start, end-start+1), end-start+1)
}
func federationRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") || strings.Contains(header, ",") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		return 0, 0, false
	}
	b, e := strconv.ParseInt(parts[1], 10, 64)
	return a, b, e == nil && a >= 0 && b >= a && b < size && b-a < store.FederationChunkSize
}

func (f *Federation) Run(ctx context.Context) error {
	if len(f.opts.FileLinks) > 0 {
		release, err := blobguard.Acquire(f.opts.FileDir)
		if err != nil {
			return errors.New("attachment runtime already active or maintenance in progress")
		}
		defer release()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	run := func(fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := fn(ctx)
			if err != nil && ctx.Err() == nil {
				errs <- err
				cancel()
			}
		}()
	}
	run(f.runV1)
	if len(f.opts.FileLinks) > 0 {
		run(func(ctx context.Context) error { return f.fileLoop(ctx, false) })
		for i := 0; i < 2; i++ {
			run(func(ctx context.Context) error { return f.fileLoop(ctx, true) })
		}
	}
	wg.Wait()
	select {
	case err := <-errs:
		return err
	default:
		return nil
	}
}
func (f *Federation) fileLoop(ctx context.Context, inbound bool) error {
	timer := time.NewTicker(f.opts.PollInterval)
	defer timer.Stop()
	for ctx.Err() == nil {
		j, err := f.store.ClaimFederationFileJob(ctx, inbound, f.opts.LeaseDuration)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if j == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-timer.C:
			}
			continue
		}
		state, reason := "pending", ""
		if inbound {
			reason = f.receiveFiles(ctx, *j)
			if reason == "" {
				continue
			}
		} else {
			state, reason = f.deliverFiles(ctx, *j)
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := f.opts.PollInterval
		if reason != "" {
			delay = f.opts.RetryBase * time.Duration(1<<min(j.Attempts, 8))
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
		if err = f.store.FinishFederationFileJob(ctx, *j, inbound, state, reason, time.Now().Add(delay)); err != nil {
			return err
		}
	}
	return nil
}
func (f *Federation) peerRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, f.opts.RequestTimeout)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(f.opts.Peer.BaseURL, "/")+path, body)
	if err != nil {
		cancel()
		return nil, func() {}, err
	}
	req.Header.Set("Authorization", "Bearer "+f.opts.Peer.Secret)
	req.Header.Set("X-AgentChat-Node", f.nodeID)
	req.Header.Set("Accept-Encoding", "identity")
	return req, cancel, nil
}
func (f *Federation) deliverFiles(ctx context.Context, j store.FederationFileJob) (string, string) {
	var e store.FederationFileEnvelope
	if json.Unmarshal([]byte(j.Payload), &e) != nil {
		return "dead", "invalid envelope"
	}
	if f.store.FederationFileAuthorized(ctx, j.PeerID, e, false) != nil {
		return "dead", "link disabled"
	}
	// Explicit negotiation precedes v2 delivery. There is never a v1 fallback.
	req, cancel, err := f.peerRequest(ctx, "GET", "/federation/v2/capabilities", nil)
	if err != nil {
		return "dead", "invalid endpoint"
	}
	resp, err := f.http.Do(req)
	if err != nil {
		cancel()
		return "pending", "peer transport failure"
	}
	var cap struct {
		Version     int  `json:"version"`
		Attachments bool `json:"attachments"`
	}
	ok := resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&cap) == nil && cap.Version == 2 && cap.Attachments
	resp.Body.Close()
	cancel()
	if !ok {
		return "pending", "peer capability unavailable"
	}
	req, cancel, err = f.peerRequest(ctx, "POST", "/federation/v2/messages", bytes.NewBufferString(j.Payload))
	if err != nil {
		return "dead", "invalid endpoint"
	}
	defer cancel()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-AgentChat-Retry-Generation", strconv.FormatInt(j.Generation, 10))
	resp, err = f.http.Do(req)
	if err != nil {
		return "pending", "peer transport failure"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 202 {
		state := "pending"
		if resp.StatusCode < 500 && resp.StatusCode != 408 && resp.StatusCode != 429 {
			state = "dead"
		}
		return state, "peer rejected delivery"
	}
	var r store.FederationFileReceipt
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r) != nil || r.OriginNodeID != e.OriginNodeID || r.OriginMessageID != e.OriginMessageID || r.TransferID == "" {
		return "pending", "invalid receipt"
	}
	if resp.StatusCode == 200 {
		if r.State != "committed" || r.LocalMessageID == "" {
			return "pending", "invalid receipt"
		}
		return "delivered", ""
	}
	if r.State != "pending" && r.State != "inflight" {
		return "pending", "invalid receipt"
	}
	return "waiting_receipt", ""
}
func (f *Federation) receiveFiles(ctx context.Context, j store.FederationFileJob) string {
	// The root's OS lock excludes other runtimes. Within this runtime, a late
	// lease and its replacement cannot sweep or write each other's temp files.
	key := sha256.Sum256([]byte(j.ID))
	mu := &f.fileLocks[int(key[0])%len(f.fileLocks)]
	mu.Lock()
	defer mu.Unlock()
	var e store.FederationFileEnvelope
	if json.Unmarshal([]byte(j.Payload), &e) != nil || e.Validate(j.PeerID) != nil {
		return "invalid envelope"
	}
	if f.store.FederationFileAuthorized(ctx, j.PeerID, e, true) != nil {
		return "link disabled"
	}
	p, err := f.store.FederationFileProgress(ctx, j)
	if err != nil {
		return "storage unavailable"
	}
	for p.Index < len(e.Files) {
		if ctx.Err() != nil {
			return "cancelled"
		}
		b := e.Files[p.Index]
		if err = f.fetchBlob(ctx, j, e, b, &p); err != nil {
			return "blob transfer failure"
		}
		_, relative, err := federationBlobPath(f.opts.FileDir, b.SHA256)
		if err != nil {
			return "blob path failure"
		}
		if err = f.store.FederationFileReady(ctx, j, b, relative, p.Index, f.opts.LeaseDuration); err != nil {
			return "storage unavailable"
		}
		p.Index++
		p.Offset = 0
		p.Temp = ""
	}
	// A recovered "ready" row is not evidence that bytes still exist intact.
	// Recheck every installed blob before making the message visible.
	for _, b := range e.Files {
		if err = f.store.FederationFileCheckpoint(ctx, j, &p, f.opts.LeaseDuration); err != nil {
			return "lease expired"
		}
		path, _, er := federationBlobPath(f.opts.FileDir, b.SHA256)
		if er != nil {
			return "blob path failure"
		}
		file, er := os.Open(path)
		if er != nil {
			return "blob integrity failure"
		}
		h := sha256.New()
		n, er := io.Copy(h, file)
		file.Close()
		if er != nil || n != b.Size || hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
			return "blob integrity failure"
		}
	}
	if err = f.store.CommitFederationFiles(ctx, j, e); err != nil {
		return "commit failure"
	}
	if f.opts.notify != nil {
		f.opts.notify()
	}
	return ""
}
func (f *Federation) fetchBlob(ctx context.Context, j store.FederationFileJob, e store.FederationFileEnvelope, b store.FederationBlob, p *store.FederationFileProgress) error {
	if !store.ValidFederationTransferID(j.ID) {
		return errors.New("invalid transfer identifier")
	}
	if err := f.store.FederationFileCheckpoint(ctx, j, p, f.opts.LeaseDuration); err != nil {
		return err
	}
	dir, err := safeFederationPath(f.opts.FileDir, filepath.Join(".federation", j.ID))
	if err != nil {
		return err
	}
	// Checkpoint names are generated internally. Reject persisted paths outside
	// this transfer, including a malicious/corrupt restored database.
	old := ""
	if p.Temp != "" {
		if filepath.Base(p.Temp) != p.Temp {
			return errors.New("invalid checkpoint")
		}
		old = filepath.Join(dir, p.Temp)
		info, er := os.Lstat(old)
		if er != nil || !info.Mode().IsRegular() || info.Size() < p.Offset {
			p.Offset = 0
			old = ""
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, v := range entries {
		path := filepath.Join(dir, v.Name())
		if path != old {
			if err = os.Remove(path); err != nil {
				return err
			}
		}
	}
	tmp, err := os.CreateTemp(dir, j.Lease+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer tmp.Close()
	// A new lease uses a different inode. A stale writer cannot corrupt it.
	if old != "" && p.Offset > 0 {
		src, er := os.Open(old)
		if er != nil {
			return er
		}
		_, er = io.CopyN(tmp, src, p.Offset)
		src.Close()
		if er != nil {
			return er
		}
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	p.Temp = filepath.Base(name)
	if err = f.store.FederationFileCheckpoint(ctx, j, p, f.opts.LeaseDuration); err != nil {
		os.Remove(name)
		return err
	}
	// Reclaim abandoned lease files before any more bytes are downloaded. Refuse
	// further allocation if Windows still has a stale writer holding a file.
	entries, err = os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, v := range entries {
		if v.Name() != p.Temp {
			if v.IsDir() {
				return errors.New("unexpected staging directory")
			}
			if err = os.Remove(filepath.Join(dir, v.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if p.Offset < 0 || p.Offset > b.Size {
		return errors.New("invalid checkpoint offset")
	}
	for p.Offset < b.Size {
		end := min(p.Offset+store.FederationChunkSize, b.Size) - 1
		q := url.Values{"origin_node_id": {e.OriginNodeID}, "origin_message_id": {e.OriginMessageID}, "link_id": {e.LinkID}, "epoch": {strconv.FormatInt(e.Epoch, 10)}, "generation": {strconv.FormatInt(j.Generation, 10)}}
		req, cancel, er := f.peerRequest(ctx, "GET", "/federation/v2/blobs/"+b.SHA256+"?"+q.Encode(), nil)
		if er != nil {
			return er
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", p.Offset, end))
		req.Header.Set("If-Range", "\""+b.SHA256+"\"")
		resp, er := f.http.Do(req)
		if er != nil {
			cancel()
			return er
		}
		valid := resp.StatusCode == 206 && resp.Header.Get("ETag") == "\""+b.SHA256+"\"" && resp.Header.Get("Content-Range") == fmt.Sprintf("bytes %d-%d/%d", p.Offset, end, b.Size) && resp.Header.Get("Content-Encoding") == "" && resp.ContentLength == end-p.Offset+1
		// A peer may ignore Range. A bounded 200 can only restart the entire
		// file; it must never be appended to a saved prefix.
		if resp.StatusCode == 200 && resp.Header.Get("ETag") == "\""+b.SHA256+"\"" && resp.Header.Get("Content-Encoding") == "" && resp.ContentLength == b.Size {
			if er = tmp.Truncate(0); er != nil {
				resp.Body.Close()
				cancel()
				return er
			}
			if _, er = tmp.Seek(0, io.SeekStart); er != nil {
				resp.Body.Close()
				cancel()
				return er
			}
			p.Offset = 0
			end = b.Size - 1
			valid = true
		}
		if !valid {
			resp.Body.Close()
			cancel()
			return errors.New("invalid range response")
		}
		n, er := io.CopyN(tmp, resp.Body, end-p.Offset+1)
		var extra [1]byte
		extraN, extraErr := resp.Body.Read(extra[:])
		resp.Body.Close()
		cancel()
		if er != nil || extraN != 0 || extraErr != io.EOF {
			return errors.New("incomplete blob range")
		}
		if er = tmp.Sync(); er != nil {
			return er
		}
		p.Offset += n
		if er = f.store.FederationFileCheckpoint(ctx, j, p, f.opts.LeaseDuration); er != nil {
			return er
		}
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, tmp)
	if err != nil || n != b.Size || hex.EncodeToString(hash.Sum(nil)) != b.SHA256 {
		// A corrupted prefix or peer body must restart from zero, never attach.
		p.Offset = 0
		_ = f.store.FederationFileCheckpoint(ctx, j, p, f.opts.LeaseDuration)
		return errors.New("blob integrity failure")
	}
	if err = f.store.FederationFileCheckpoint(ctx, j, p, f.opts.LeaseDuration); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	final, _, err := federationBlobPath(f.opts.FileDir, b.SHA256)
	if err != nil {
		return err
	}
	if err = installBlob(name, final, b.Size, b.SHA256); err != nil {
		return err
	}
	return os.Remove(name)
}
