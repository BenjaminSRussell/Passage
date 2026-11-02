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
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout         = 2 * time.Second
	maxResponseSize = 1 << 20
	holdCheckPath   = "/v1/privacy/holds:check"

	audience      = "compliance"
	dutyHoldCheck = "compliance.legal_hold.check"
)

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

func (c *Client) Covering(ctx context.Context, q domain.HoldQuery) ([]string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("compliance hold check: no principal in context")
	}
	params := url.Values{"legalEntityId": {q.LegalEntityID}}
	for key, value := range map[string]string{"subjectRef": q.SubjectRef, "dataClass": q.DataClass, "recordRef": q.RecordRef} {
		if value != "" {
			params.Set(key, value)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+holdCheckPath+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("compliance get %s: %w", holdCheckPath, err)
	}
	if c.signer == nil {
		identity.Forward(req, principal)
	} else if _, err := c.signer.ApplyDuties(req, principal, audience, q.LegalEntityID, []string{dutyHoldCheck}); err != nil {
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
