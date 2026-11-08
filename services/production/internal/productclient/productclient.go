// Package productclient reads a released product's site default (FR-8.4) from the product service: the
// minimum, multiple and standard order quantity that production orders are checked against (FR-21.1).
package productclient

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
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 3 * time.Second
	audience        = "product"
	dutyRead        = "product.read"
	maxResponseSize = 1 << 20
	codeNoDefault   = "PRD-RELEASE-DEFAULT-MISSING"
)

var (
	// ErrNotFound means the product service has no such released product.
	ErrNotFound = errors.New("released product not found")
	// ErrNoDefault means no site or warehouse default resolves for the site (PRD-RELEASE-DEFAULT-MISSING).
	ErrNoDefault = errors.New("released product has no default for the site")
	// ErrUnavailable means the product service could not answer; the caller may retry.
	ErrUnavailable = errors.New("product service unavailable")
)

// SiteDefault is the cascaded order setting of a released product at a site. A zero quantity is no rule.
type SiteDefault struct {
	MinQty      decimal.Decimal `json:"minQty"`
	MultipleQty decimal.Decimal `json:"multipleQty"`
	StdQty      decimal.Decimal `json:"stdQty"`
}

// Resolver reads site defaults. A nil Resolver means PRODUCT_URL is unset and the rules come in the request.
type Resolver interface {
	SiteDefault(ctx context.Context, legalEntityID, releasedProductID, siteID string) (SiteDefault, error)
}

// Client is the HTTP Resolver over a signed service token limited to product.read.
type Client struct {
	baseURL string
	http    *http.Client
	// sign adds the service token; tests replace it while the production to product grant is not yet seeded.
	sign func(req *http.Request, p tenant.Principal, legalEntityID string) error
}

// New returns a client of the product service at baseURL; a nil httpClient uses Timeout and refuses redirects.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("product client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("product client needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	sign := func(req *http.Request, p tenant.Principal, legalEntityID string) error {
		_, err := signer.ApplyDuties(req, p, audience, legalEntityID, []string{dutyRead})
		return err
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, sign: sign}, nil
}

// FromEnv builds the client from PRODUCT_URL and the service signing key. It returns an untyped nil Resolver
// when PRODUCT_URL is unset.
func FromEnv(getenv func(string) string) (Resolver, error) {
	baseURL := getenv("PRODUCT_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("PRODUCT_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	c, err := New(baseURL, signer, nil)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SiteDefault gets GET /v1/product/released-products/{id}/site-defaults/{siteId}.
func (c *Client) SiteDefault(ctx context.Context, legalEntityID, releasedProductID, siteID string) (SiteDefault, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return SiteDefault{}, errors.New("product site default: no principal in context")
	}
	if !ids.Valid(legalEntityID) || !ids.Valid(releasedProductID) || !ids.Valid(siteID) {
		return SiteDefault{}, ErrNotFound
	}
	target := c.baseURL + "/v1/product/released-products/" + url.PathEscape(releasedProductID) + "/site-defaults/" + url.PathEscape(siteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return SiteDefault{}, fmt.Errorf("product site default: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if err := c.sign(req, principal, legalEntityID); err != nil {
		return SiteDefault{}, fmt.Errorf("product site default: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "product site default failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return SiteDefault{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return SiteDefault{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var out SiteDefault
		if err := json.Unmarshal(body, &out); err != nil {
			return SiteDefault{}, fmt.Errorf("%w: unreadable answer", ErrUnavailable)
		}
		return out, nil
	case http.StatusNotFound:
		return SiteDefault{}, ErrNotFound
	case http.StatusUnprocessableEntity:
		var problem struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(body, &problem) == nil && problem.Code == codeNoDefault {
			return SiteDefault{}, ErrNoDefault
		}
	}
	slog.ErrorContext(ctx, "product refused the site default read", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
	return SiteDefault{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
}

// Fake is an in-memory Resolver for tests, keyed by released product id and site id.
type Fake struct {
	Defaults map[[2]string]SiteDefault
	Err      error
	Calls    int
}

// SiteDefault returns the default set for the pair, or ErrNotFound.
func (f *Fake) SiteDefault(_ context.Context, _, releasedProductID, siteID string) (SiteDefault, error) {
	f.Calls++
	if f.Err != nil {
		return SiteDefault{}, f.Err
	}
	d, ok := f.Defaults[[2]string{releasedProductID, siteID}]
	if !ok {
		return SiteDefault{}, ErrNotFound
	}
	return d, nil
}
