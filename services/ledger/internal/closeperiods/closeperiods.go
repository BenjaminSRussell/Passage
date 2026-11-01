package closeperiods

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
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	Timeout         = 2 * time.Second
	resolvePath     = "/v1/close/periods:resolve"
	audience        = "close"
	dutyPeriodCheck = "period.check"
	codeNoPeriod    = "CLOSE-0005"
	maxResponseSize = 1 << 20
	statusOpen      = "open"
	CacheTTL        = 30 * time.Second
	maxCacheEntries = 10000
)

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	status  string
	expires time.Time
}

func New(baseURL string, httpClient *http.Client, signer *tenant.ServiceSigner) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer, now: time.Now, cache: map[string]cached{}}
}

func (c *Client) Status(ctx context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error) {
	key := ledgerID + "|" + sourceSystem + "|" + postingDate.Format(time.DateOnly)
	c.mu.Lock()
	hit, ok := c.cache[key]
	c.mu.Unlock()
	if ok && c.now().Before(hit.expires) {
		return hit.status, nil
	}
	status, err := c.fetch(ctx, legalEntityID, ledgerID, postingDate, sourceSystem)
	if err == nil && status != statusOpen {
		c.mu.Lock()
		if len(c.cache) >= maxCacheEntries {
			c.cache = map[string]cached{}
		}
		c.cache[key] = cached{status: status, expires: c.now().Add(CacheTTL)}
		c.mu.Unlock()
	}
	return status, err
}

func (c *Client) fetch(ctx context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("close: no principal in context")
	}
	if c.signer == nil {
		return "", errors.New("close: no service signing key configured")
	}
	query := url.Values{"ledgerId": {ledgerID}, "postingDate": {postingDate.Format(time.DateOnly)}, "sourceSystem": {sourceSystem}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+resolvePath+"?"+query.Encode(), nil)
	if err != nil {
		return "", fmt.Errorf("close: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyPeriodCheck}); err != nil {
		return "", fmt.Errorf("close: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("close: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("close: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.Status == "" {
			return "", errors.New("close: response has no status")
		}
		return body.Status, nil
	case http.StatusNotFound:
		var problem struct {
			Code        string `json:"code"`
			ServiceCode string `json:"serviceCode"`
		}
		if json.Unmarshal(raw, &problem) == nil && (problem.Code == codeNoPeriod || problem.ServiceCode == codeNoPeriod) {
			return "", store.ErrNoFiscalPeriod
		}
	}
	slog.ErrorContext(ctx, "close rejected period resolve", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
	return "", fmt.Errorf("close: unexpected status %d", resp.StatusCode)
}
