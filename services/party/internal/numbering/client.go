package numbering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	CodeUnavailable    = "PTY-NUMBERING-UNAVAILABLE"
	CodeEntityRequired = "PTY-LEGAL-ENTITY-REQUIRED"
	timeout            = 3 * time.Second
	maxResponse        = 1 << 20
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client { return NewWithTimeout(baseURL, timeout) }

func NewWithTimeout(baseURL string, d time.Duration) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: d}}
}

type Unavailable struct{}

func (Unavailable) Next(context.Context, string, string) (string, error) {
	return "", unavailable()
}

func unavailable() error {
	return httpx.NewProblem(http.StatusServiceUnavailable, CodeUnavailable, "numbering service unavailable")
}

func (c *Client) Next(ctx context.Context, code, idempotencyKey string) (string, error) {
	if p, _ := tenant.FromContext(ctx); !ids.Valid(p.LegalEntityID) {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, CodeEntityRequired, "a single legal entity (X-Passage-Legal-Entity) is required to allocate a party number")
	}
	formatted, err := c.allocate(ctx, code, idempotencyKey)
	if err != nil {
		slog.Error("numbering allocation failed", "err", err, "code", code, "correlationId", middleware.GetReqID(ctx))
		return "", unavailable()
	}
	return formatted, nil
}

func (c *Client) allocate(ctx context.Context, code, idempotencyKey string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/numbering/sequences/"+url.PathEscape(code)+"/next", nil)
	if err != nil {
		return "", err
	}
	p, _ := tenant.FromContext(ctx)
	duties := make([]string, 0, len(p.Duties))
	for d := range p.Duties {
		duties = append(duties, d)
	}
	sort.Strings(duties)
	req.Header.Set(tenant.HeaderTenant, p.TenantID)
	req.Header.Set(tenant.HeaderLegalEntity, p.LegalEntityID)
	req.Header.Set(tenant.HeaderUser, p.UserID)
	req.Header.Set(tenant.HeaderDuties, strings.Join(duties, ","))
	req.Header.Set("Idempotency-Key", idempotencyKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("numbering answered %d", resp.StatusCode)
	}
	var issued struct {
		Formatted string `json:"formatted"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&issued); err != nil {
		return "", fmt.Errorf("decode numbering response: %w", err)
	}
	if issued.Formatted == "" {
		return "", fmt.Errorf("numbering response has no formatted value")
	}
	return issued.Formatted, nil
}
