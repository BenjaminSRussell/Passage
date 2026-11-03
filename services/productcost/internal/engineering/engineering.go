package engineering

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
)

const (
	Timeout         = 10 * time.Second
	audience        = "engineering"
	dutyRead        = "engineering.read"
	bomPath         = "/v1/engineering/bom-versions"
	routeActivePath = "/v1/engineering/route-versions:active"
	maxResponseSize = 8 << 20
)

type BOMLine struct {
	ComponentProductID string `json:"componentProductId"`
	Quantity           string `json:"quantity"`
	Series             string `json:"series"`
	LineType           string `json:"lineType"`
	ScrapConst         string `json:"scrapConst"`
	ScrapVarPct        string `json:"scrapVarPct"`
	AltGroup           string `json:"altGroup"`
}

type BOM struct {
	ID    string    `json:"id"`
	Lines []BOMLine `json:"lines"`
}

type Operation struct {
	OperationID  string `json:"operationId"`
	Priority     string `json:"priority"`
	SetupH       string `json:"setupH"`
	RunH         string `json:"runH"`
	ProcessQty   string `json:"processQty"`
	ScrapPercent string `json:"scrapPercent"`
}

type Route struct {
	ID         string      `json:"id"`
	Operations []Operation `json:"operations"`
}

// Reader returns the active BOM and route version of a product at a site on a
// date, or nil when there is none.
type Reader interface {
	ActiveBOM(ctx context.Context, legalEntityID, productID, siteID, asOf string) (*BOM, error)
	ActiveRoute(ctx context.Context, legalEntityID, productID, siteID, asOf string) (*Route, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("engineering client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("engineering client needs a service signing key: the end user's headers are never forwarded")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from ENGINEERING_URL and the service signing key
// variables. It returns nil, nil when ENGINEERING_URL is unset.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("ENGINEERING_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return New(baseURL, signer, nil)
}

var bomUsageRank = map[string]int{"costing": 0, "production": 1, "": 1, "universal": 2}

type bomHeader struct {
	ID       string `json:"id"`
	Usage    string `json:"usage"`
	Category string `json:"category"`
}

func (c *Client) ActiveBOM(ctx context.Context, legalEntityID, productID, siteID, asOf string) (*BOM, error) {
	var page struct {
		Items      []bomHeader `json:"items"`
		NextCursor string      `json:"nextCursor"`
	}
	query := url.Values{"productId": {productID}, "siteId": {siteID}, "status": {"active"}, "asOf": {asOf}, "limit": {"100"}}
	if found, err := c.get(ctx, legalEntityID, bomPath+"?"+query.Encode(), &page); err != nil || !found {
		return nil, err
	}
	if page.NextCursor != "" {
		return nil, fmt.Errorf("engineering: more than 100 active BOM versions of %s", productID)
	}
	best := -1
	for i, h := range page.Items {
		rank, ok := bomUsageRank[h.Usage]
		if !ok || (h.Category != "" && h.Category != "material") {
			continue
		}
		if best < 0 || rank < bomUsageRank[page.Items[best].Usage] {
			best = i
		}
	}
	if best < 0 {
		return nil, nil
	}
	var bom BOM
	found, err := c.get(ctx, legalEntityID, bomPath+"/"+url.PathEscape(page.Items[best].ID), &bom)
	if err != nil || !found {
		return nil, err
	}
	return &bom, nil
}

func (c *Client) ActiveRoute(ctx context.Context, legalEntityID, productID, siteID, asOf string) (*Route, error) {
	var route Route
	query := url.Values{"productId": {productID}, "siteId": {siteID}, "asOf": {asOf}}
	found, err := c.get(ctx, legalEntityID, routeActivePath+"?"+query.Encode(), &route)
	if err != nil || !found {
		return nil, err
	}
	return &route, nil
}

func (c *Client) get(ctx context.Context, legalEntityID, path string, out any) (bool, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return false, errors.New("engineering read: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return false, fmt.Errorf("engineering read: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRead}); err != nil {
		return false, fmt.Errorf("engineering read: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "engineering read failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return false, fmt.Errorf("engineering read: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "engineering response unreadable", "err", err, "correlationId", middleware.GetReqID(ctx))
		return false, fmt.Errorf("engineering read: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return false, nil
	default:
		slog.ErrorContext(ctx, "engineering refused the read", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return false, fmt.Errorf("engineering read: status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		slog.ErrorContext(ctx, "engineering response unusable", "err", err, "correlationId", middleware.GetReqID(ctx))
		return false, fmt.Errorf("engineering read: %w", err)
	}
	return true, nil
}
