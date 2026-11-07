package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mcbulazs/restash/internal/proto"
)

// Client talks to the restash API.
type Client struct {
	Server string
	PCID   string
	Token  string
	HTTP   *http.Client
}

// NewClient makes a client for a joined PC (or, with empty id and token, for
// joining).
func NewClient(server, pcID, token string) *Client {
	return &Client{Server: strings.TrimSuffix(server, "/"), PCID: pcID, Token: token, HTTP: &http.Client{Timeout: time.Minute}}
}

// APIError is an error reply from the server.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// ErrUnlinked means the server no longer knows this PC's token.
var ErrUnlinked = errors.New("the server doesn't recognise this PC anymore: link it again with `restash-agent join <code> -force`")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Server+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.PCID != "" {
		req.Header.Set("Authorization", "Bearer "+c.PCID+"."+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("can't reach %s: %w", c.Server, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&e)
		if resp.StatusCode == http.StatusUnauthorized && c.PCID != "" {
			return ErrUnlinked
		}
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Join links this PC with a code from the website.
func (c *Client) Join(ctx context.Context, req proto.JoinRequest) (*proto.JoinResponse, error) {
	var out proto.JoinResponse
	return &out, c.do(ctx, "POST", "/api/agent/join", req, &out)
}

// Account fetches what's needed to unlock the account key.
func (c *Client) Account(ctx context.Context) (*proto.Account, error) {
	var out proto.Account
	return &out, c.do(ctx, "GET", "/api/agent/account", nil, &out)
}

// SetKey stores the PC's locked restic key.
func (c *Client) SetKey(ctx context.Context, sealed string) error {
	return c.do(ctx, "PUT", "/api/agent/key", proto.SetKey{PCKey: sealed}, nil)
}

// Entries fetches this PC's entries (settings still locked).
func (c *Client) Entries(ctx context.Context) ([]proto.Entry, error) {
	var out []proto.Entry
	return out, c.do(ctx, "GET", "/api/agent/entries", nil, &out)
}

// ReportBackup sends a backup result.
func (c *Client) ReportBackup(ctx context.Context, r proto.BackupReport) error {
	return c.do(ctx, "POST", "/api/agent/backups", r, nil)
}
