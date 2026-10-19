// Package client is the small HTTP client shared by the CLI and MCP server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/agentchat-lan/agentchat/internal/protocol"
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
			Timeout: 45 * time.Second,
		},
	}
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
