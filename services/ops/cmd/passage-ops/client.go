package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client calls the ops API through the gateway with the operator's bearer token.
type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(getenv func(string) string, h *http.Client) (*client, error) {
	base := strings.TrimSuffix(getenv("OPS_API_URL"), "/")
	if base == "" {
		return nil, errors.New("OPS_API_URL is required (the gateway or ops service URL)")
	}
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return &client{base: base, token: getenv("OPS_TOKEN"), http: h}, nil
}

// apiError is a problem answer.
type apiError struct {
	Status int
	Code   string
	Detail string
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Detail)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail)
}

// do sends a JSON request and decodes a 2xx body into out (when non-nil). It returns the status code.
func (c *client) do(ctx context.Context, method, path string, headers map[string]string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		var p struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Detail == "" {
			p.Detail = strings.TrimSpace(string(raw[:min(len(raw), 200)]))
		}
		return resp.StatusCode, &apiError{Status: resp.StatusCode, Code: p.Code, Detail: p.Detail}
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func esc(s string) string { return url.PathEscape(s) }
