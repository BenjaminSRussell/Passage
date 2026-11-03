package compliance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout         = 2 * time.Second
	maxResponseSize = 1 << 20
	holdCheckPath   = "/v1/privacy/holds:check"
	audience        = "compliance"
	dutyHoldCheck   = "compliance.legal_hold.check"
	candidateClass  = "CANDIDATE"
)

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

// CoveringCandidate returns the ids of the active legal holds that cover one candidate.
func (c *Client) CoveringCandidate(ctx context.Context, legalEntityID, candidateID string) ([]string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("compliance hold check: no principal in context")
	}
	if c.signer == nil {
		return nil, errors.New("compliance hold check: no service signing key configured")
	}
	params := url.Values{"legalEntityId": {legalEntityID}, "dataClass": {candidateClass}, "recordRef": {candidateID}, "subjectRef": {candidateID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+holdCheckPath+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("compliance get %s: %w", holdCheckPath, err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyHoldCheck}); err != nil {
		return nil, fmt.Errorf("compliance get %s: %w", holdCheckPath, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("compliance get %s: %w", holdCheckPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("compliance get %s: unexpected status %d", holdCheckPath, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("compliance get %s: %w", holdCheckPath, err)
	}
	var body struct {
		Held    *bool    `json:"held"`
		HoldIDs []string `json:"holdIds"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Held == nil {
		return nil, fmt.Errorf("compliance get %s: response is not a hold check", holdCheckPath)
	}
	if *body.Held && len(body.HoldIDs) == 0 {
		return []string{"unnamed"}, nil
	}
	return body.HoldIDs, nil
}
