// Package client is the small HTTP client shared by the CLI and MCP server.
package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Dofu27/agentchat-lan/internal/protocol"
)

// APIError is returned for a non-2xx response.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	Details    map[string]any
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("agentchat: HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("agentchat: %s (HTTP %d): %s", e.Code, e.StatusCode, e.Message)
}

// IsUnauthorized reports whether the error is an authentication failure.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

// Client talks to one hub using one agent token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client with sane network timeouts.
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			// No global timeout: file transfers can be large. Every method
			// takes a context, and the CLI supplies a deadline.
			Timeout: 0,
		},
	}
}

// Health checks the hub without authentication.
func (c *Client) Health(ctx context.Context) (*protocol.HealthResponse, error) {
	var out protocol.HealthResponse
	if err := c.doJSON(ctx, http.MethodGet, "/healthz", "", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Join exchanges an invite code for a token. No bearer token is required.
func (c *Client) Join(ctx context.Context, req protocol.JoinRequest) (*protocol.JoinResponse, error) {
	var out protocol.JoinResponse
	if err := c.doJSON(ctx, http.MethodPost, "/v1/join", "", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMessage posts a message to a room.
func (c *Client) SendMessage(ctx context.Context, room string, req protocol.SendMessageRequest) (*protocol.Message, error) {
	var out protocol.Message
	path := "/v1/rooms/" + url.PathEscape(room) + "/messages"
	if err := c.doJSON(ctx, http.MethodPost, path, c.Token, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// History fetches room messages after a sequence number.
func (c *Client) History(ctx context.Context, room string, afterSeq int64, limit int) (*protocol.HistoryResponse, error) {
	q := url.Values{}
	q.Set("after_seq", strconv.FormatInt(afterSeq, 10))
	q.Set("limit", strconv.Itoa(limit))
	var out protocol.HistoryResponse
	path := "/v1/rooms/" + url.PathEscape(room) + "/messages?" + q.Encode()
	if err := c.doJSON(ctx, http.MethodGet, path, c.Token, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Inbox fetches unacknowledged messages, optionally long-polling.
func (c *Client) Inbox(ctx context.Context, waitSeconds, limit int) (*protocol.InboxResponse, error) {
	q := url.Values{}
	q.Set("wait", strconv.Itoa(waitSeconds))
	q.Set("limit", strconv.Itoa(limit))
	var out protocol.InboxResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/inbox?"+q.Encode(), c.Token, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ack advances the server-side cursor for the given rooms.
func (c *Client) Ack(ctx context.Context, acks map[string]int64) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/ack", c.Token, protocol.AckRequest{Acks: acks}, nil)
}

// Rooms lists rooms the agent can see.
func (c *Client) Rooms(ctx context.Context) ([]protocol.Room, error) {
	var out protocol.RoomsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/rooms", c.Token, nil, &out); err != nil {
		return nil, err
	}
	return out.Rooms, nil
}

// Agents lists known agents.
func (c *Client) Agents(ctx context.Context) ([]protocol.Agent, error) {
	var out protocol.AgentsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/agents", c.Token, nil, &out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

// Me returns the authenticated agent and effective limits.
func (c *Client) Me(ctx context.Context) (*protocol.MeResponse, error) {
	var out protocol.MeResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/me", c.Token, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DownloadInfo describes a completed download.
type DownloadInfo struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Mime   string `json:"mime,omitempty"`
	Size   int64  `json:"size"`
}

// UploadFile streams one file to POST /v1/files.
func (c *Client) UploadFile(ctx context.Context, path string) (*protocol.FileRef, error) {
	if c.Token == "" {
		return nil, errors.New("agentchat: upload requires an agent token")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open upload %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat upload %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("upload %s is a directory", path)
	}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(path))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		_ = pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/files", pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, fmt.Errorf("build upload request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, fmt.Errorf("upload %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeError(resp)
	}

	var ref protocol.FileRef
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ref); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}
	return &ref, nil
}

// DownloadFile streams GET /v1/files/{id} to dest and verifies the SHA-256.
func (c *Client) DownloadFile(ctx context.Context, fileID, dest string) (*DownloadInfo, error) {
	if c.Token == "" {
		return nil, errors.New("agentchat: download requires an agent token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/files/"+url.PathEscape(fileID), nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", fileID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, decodeError(resp)
	}

	name := fileID
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if v := params["filename"]; v != "" {
				name = v
			}
		}
	}
	name = sanitizeDownloadName(name)

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create download directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".agentchat-download-*")
	if err != nil {
		return nil, fmt.Errorf("create download temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()

	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body)
	if err != nil {
		return nil, fmt.Errorf("write download: %w", err)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if want := resp.Header.Get("X-Content-SHA256"); want != "" && !strings.EqualFold(want, sum) {
		return nil, fmt.Errorf("download checksum mismatch: got %s, want %s", sum, want)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return nil, fmt.Errorf("chmod download: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close download: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return nil, fmt.Errorf("install download %s: %w", dest, err)
	}
	return &DownloadInfo{
		Path:   dest,
		Name:   name,
		SHA256: sum,
		Mime:   resp.Header.Get("Content-Type"),
		Size:   size,
	}, nil
}

func sanitizeDownloadName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "download.bin"
	}
	return name
}

func (c *Client) doJSON(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func decodeError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var envelope protocol.ErrorResponse
	if err := json.Unmarshal(data, &envelope); err == nil && envelope.Error.Code != "" {
		return &APIError{
			StatusCode: resp.StatusCode,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
			Details:    envelope.Error.Details,
		}
	}
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{StatusCode: resp.StatusCode, Message: msg}
}
