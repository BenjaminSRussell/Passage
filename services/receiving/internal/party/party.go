package party

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout         = 2 * time.Second
	maxResponseSize = 1 << 20
	vendorPathTmpl  = "/v1/party/vendors/%s"

	audience      = "party"
	dutyPartyRead = "party.read"
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

func (c *Client) VendorGroupID(ctx context.Context, legalEntityID, vendorID string) (string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("party vendor lookup: no principal in context")
	}
	path := fmt.Sprintf(vendorPathTmpl, url.PathEscape(vendorID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("party get %s: %w", path, err)
	}
	if c.signer == nil {
		identity.Forward(req, principal)
	} else if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyPartyRead}); err != nil {
		return "", fmt.Errorf("party get %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "party get transport failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("party get %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", fmt.Errorf("party get %s: %w", path, domain.ErrVendorNotFound)
	default:
		return "", fmt.Errorf("party get %s: unexpected status %d", path, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("party get %s: %w", path, err)
	}
	var body struct {
		VendorGroupID string `json:"vendorGroupId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("party get %s: response is not a vendor", path)
	}
	return body.VendorGroupID, nil
}
