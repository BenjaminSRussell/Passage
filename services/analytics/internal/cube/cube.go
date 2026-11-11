// Package cube runs governed queries against the Cube semantic layer on behalf of the
// signed-in user (46-3). The caller's tenant and legal-entity scope travel in a short-lived
// token signed with the Cube API secret (HS256), which data/cube/cube.js turns into
// filters on every cube of the query. The nightly reconciliation has its own reader with a
// fixed service token (internal/recon).
package cube

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const (
	loadPath      = "/cubejs-api/v1/load"
	maxBody       = 16 << 20
	httpBudget    = 30 * time.Second
	tokenLifetime = 2 * time.Minute
	continueWait  = "Continue wait"
)

// ErrUnavailable means Cube did not give an answer: it is down, slow or refused the query.
var ErrUnavailable = errors.New("semantic layer unavailable")

// Client talks to one Cube deployment.
type Client struct {
	baseURL string
	secret  []byte
	http    *http.Client
	now     func() time.Time
	// retries and wait govern Cube's "Continue wait" answer while it builds a result.
	retries int
	wait    time.Duration
}

// New returns a client for the Cube at baseURL, signing with secret.
func New(baseURL, secret string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: httpBudget}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), secret: []byte(secret), http: client, now: time.Now, retries: 8, wait: 500 * time.Millisecond}
}

// WithWait sets how often and how long to wait for a "Continue wait" answer.
func (c *Client) WithWait(retries int, wait time.Duration) *Client {
	c.retries, c.wait = retries, wait
	return c
}

// Token signs the security context for one query: the tenant, and either every entity of
// the tenant (unrestricted) or the entities the caller's policy grants.
func (c *Client) Token(tenantID string, scope domain.Scope) string {
	ctx := map[string]any{"tenantId": tenantID, "unrestricted": scope.Unrestricted, "legalEntityIds": scope.LegalEntityIDs}
	if scope.LegalEntityIDs == nil {
		ctx["legalEntityIds"] = []string{}
	}
	ctx["iat"] = c.now().Unix()
	ctx["exp"] = c.now().Add(tokenLifetime).Unix()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(ctx)
	signing := header + "." + enc.EncodeToString(payload)
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(signing))
	return signing + "." + enc.EncodeToString(mac.Sum(nil))
}

// Load runs q and returns its rows keyed by member name. Numbers stay as Cube sent them
// (decimal strings), nulls stay nil.
func (c *Client) Load(ctx context.Context, tenantID string, scope domain.Scope, q domain.CubeQuery) ([]map[string]any, error) {
	body, err := json.Marshal(map[string]any{"query": q})
	if err != nil {
		return nil, err
	}
	token := c.Token(tenantID, scope)
	for attempt := 0; ; attempt++ {
		rows, wait, err := c.once(ctx, token, body)
		if err != nil {
			return nil, err
		}
		if !wait {
			return rows, nil
		}
		if attempt >= c.retries {
			return nil, fmt.Errorf("%w: the result is still being built", ErrUnavailable)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.wait):
		}
	}
}

func (c *Client) once(ctx context.Context, token string, body []byte) (rows []map[string]any, wait bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+loadPath, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var out struct {
		Error string           `json:"error"`
		Data  []map[string]any `json:"data"`
	}
	if jerr := json.Unmarshal(raw, &out); jerr != nil && resp.StatusCode == http.StatusOK {
		return nil, false, fmt.Errorf("%w: %v", ErrUnavailable, jerr)
	}
	if out.Error == continueWait {
		return nil, true, nil
	}
	if resp.StatusCode != http.StatusOK || out.Error != "" {
		return nil, false, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if out.Data == nil {
		out.Data = []map[string]any{}
	}
	return out.Data, false, nil
}

// Text renders a Cube value (string, number or nil) as a decimal string, or nil.
func Text(v any) *string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return &x
	case float64:
		s := fmt.Sprintf("%v", x)
		return &s
	}
	s := fmt.Sprint(v)
	return &s
}
