package stack

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Principal struct {
	User   string
	Duties []string
}

type Client struct {
	Base      string
	Principal Principal
	Headers   map[string]string
}

type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

func (r Response) JSON() map[string]any {
	var out map[string]any
	_ = json.Unmarshal(r.Body, &out)
	return out
}

func (r Response) String(key string) string {
	v, _ := r.JSON()[key].(string)
	return v
}

func NewKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c Client) Do(ctx context.Context, method, path string, body any, headers map[string]string) (Response, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Response{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, reader)
	if err != nil {
		return Response{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Principal.User != "" {
		req.Header.Set("X-Passage-Tenant", Tenant)
		req.Header.Set("X-Passage-Legal-Entity", LegalEntity)
		req.Header.Set("X-Passage-User", c.Principal.User)
		req.Header.Set("X-Passage-Duties", strings.Join(c.Principal.Duties, ","))
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if method != http.MethodGet && req.Header.Get("Idempotency-Key") == "" {
		req.Header.Set("Idempotency-Key", NewKey())
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	return Response{Status: resp.StatusCode, Body: raw, Header: resp.Header}, err
}

// Must is Do that fails unless the status is one of want.
func (c Client) Must(ctx context.Context, method, path string, body any, headers map[string]string, want ...int) (Response, error) {
	resp, err := c.Do(ctx, method, path, body, headers)
	if err != nil {
		return resp, err
	}
	for _, status := range want {
		if resp.Status == status {
			return resp, nil
		}
	}
	return resp, fmt.Errorf("%s %s: status %d, want %v: %s", method, path, resp.Status, want, strings.TrimSpace(string(resp.Body)))
}
