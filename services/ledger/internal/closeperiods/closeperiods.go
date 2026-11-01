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
)

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, httpClient *http.Client, signer *tenant.ServiceSigner) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}
}

func (c *Client) Status(ctx context.Context, legalEntityID, ledgerID string, postingDate time.Time, sourceSystem string) (string, error) {
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
