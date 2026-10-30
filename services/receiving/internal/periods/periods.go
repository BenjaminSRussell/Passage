package periods

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	Timeout         = 2 * time.Second
	checkPath       = "/v1/close/period-statuses:check"
	dutyPeriodCheck = "period.check"
	sourceAP        = "ap"
	codeClosed      = "LEDGER-0013"
	codeOnHold      = "LEDGER-0014"
	maxResponseSize = 1 << 20
	dateFormat      = "2006-01-02"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type requestBody struct {
	LedgerID       string `json:"ledgerId"`
	FiscalPeriodID string `json:"fiscalPeriodId"`
	PostingDate    string `json:"postingDate"`
	SourceSystem   string `json:"sourceSystem"`
}

type responseBody struct {
	Allowed bool                `json:"allowed"`
	Status  domain.PeriodStatus `json:"status"`
}

type problemBody struct {
	Code        string `json:"code"`
	ServiceCode string `json:"serviceCode"`
}

func (p problemBody) id() string {
	if p.ServiceCode != "" {
		return p.ServiceCode
	}
	return p.Code
}

func (c *Client) AP(ctx context.Context, q domain.PeriodQuery) (domain.PeriodStatus, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("period check: no principal in context")
	}
	legalEntity := q.LegalEntityID
	if legalEntity == "" {
		legalEntity = principal.LegalEntityID
	}
	if !ids.Valid(legalEntity) {
		return "", errors.New("period check: a single legal entity is required")
	}
	payload, err := json.Marshal(requestBody{LedgerID: q.LedgerID, FiscalPeriodID: q.FiscalPeriodID, PostingDate: q.PostingDate.Format(dateFormat), SourceSystem: sourceAP})
	if err != nil {
		return "", fmt.Errorf("period check: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+checkPath, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("period check: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderDuties, dutyPeriodCheck)
	req.Header.Set(tenant.HeaderTenant, principal.TenantID)
	req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
	if id := middleware.GetReqID(ctx); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "period check transport failed", "err", err, "fiscalPeriodId", q.FiscalPeriodID, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("period check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "period check read failed", "err", err, "fiscalPeriodId", q.FiscalPeriodID, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("period check: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out responseBody
		if err := json.Unmarshal(raw, &out); err != nil || !out.Allowed || out.Status == "" {
			slog.ErrorContext(ctx, "period check response unusable", "err", err, "fiscalPeriodId", q.FiscalPeriodID, "correlationId", middleware.GetReqID(ctx))
			return "", errors.New("period check: response has no usable status")
		}
		return out.Status, nil
	case http.StatusUnprocessableEntity, http.StatusForbidden:
		var problem problemBody
		if json.Unmarshal(raw, &problem) == nil {
			switch problem.id() {
			case codeClosed:
				return domain.PeriodClosed, nil
			case codeOnHold:
				return domain.PeriodOnHold, nil
			}
		}
	}
	var problem problemBody
	_ = json.Unmarshal(raw, &problem)
	err = fmt.Errorf("period check: unexpected status %d", resp.StatusCode)
	slog.ErrorContext(ctx, "period check rejected", "err", err, "status", resp.StatusCode, "problemCode", problem.id(), "fiscalPeriodId", q.FiscalPeriodID, "correlationId", middleware.GetReqID(ctx))
	return "", err
}
