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
	"net/url"
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
	resolvePath     = "/v1/close/periods:resolve"
	dutyPeriodCheck = "period.check"
	sourceAP        = "ap"
	codeClosed      = "LEDGER-0013"
	codeOnHold      = "LEDGER-0014"
	codeNoPeriod    = "CLOSE-0005"
	codeAmbiguous   = "CLOSE-0006"
	maxResponseSize = 1 << 20
	dateFormat      = "2006-01-02"
)

const audience = "close"

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

type resolveBody struct {
	FiscalPeriodID string              `json:"fiscalPeriodId"`
	Status         domain.PeriodStatus `json:"status"`
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

func (c *Client) call(ctx context.Context, op, method, target string, payload []byte, legalEntityID string, attr slog.Attr) (int, []byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, fmt.Errorf("%s: no principal in context", op)
	}
	legalEntity := legalEntityID
	if legalEntity == "" {
		legalEntity = principal.LegalEntityID
	}
	if !ids.Valid(legalEntity) {
		return 0, nil, fmt.Errorf("%s: a single legal entity is required", op)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+target, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", op, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	signed, err := c.signer.Apply(req, principal, audience, legalEntity)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", op, err)
	}
	if !signed {
		req.Header.Set(tenant.HeaderDuties, dutyPeriodCheck)
		req.Header.Set(tenant.HeaderTenant, principal.TenantID)
		req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
		if id := middleware.GetReqID(ctx); id != "" {
			req.Header.Set(middleware.RequestIDHeader, id)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, op+" transport failed", "err", err, attr, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("%s: %w", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, op+" read failed", "err", err, attr, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("%s: %w", op, err)
	}
	return resp.StatusCode, raw, nil
}

func rejected(ctx context.Context, op string, status int, raw []byte, attr slog.Attr) error {
	var problem problemBody
	_ = json.Unmarshal(raw, &problem)
	err := fmt.Errorf("%s: unexpected status %d", op, status)
	slog.ErrorContext(ctx, op+" rejected", "err", err, "status", status, "problemCode", problem.id(), attr, "correlationId", middleware.GetReqID(ctx))
	return err
}

func (c *Client) AP(ctx context.Context, q domain.PeriodQuery) (domain.PeriodStatus, error) {
	const op = "period check"
	attr := slog.String("fiscalPeriodId", q.FiscalPeriodID)
	payload, err := json.Marshal(requestBody{LedgerID: q.LedgerID, FiscalPeriodID: q.FiscalPeriodID, PostingDate: q.PostingDate.Format(dateFormat), SourceSystem: sourceAP})
	if err != nil {
		return "", fmt.Errorf("%s: %w", op, err)
	}
	status, raw, err := c.call(ctx, op, http.MethodPost, checkPath, payload, q.LegalEntityID, attr)
	if err != nil {
		return "", err
	}
	switch status {
	case http.StatusOK:
		var out responseBody
		if err := json.Unmarshal(raw, &out); err != nil || !out.Allowed || out.Status == "" {
			slog.ErrorContext(ctx, "period check response unusable", "err", err, attr, "correlationId", middleware.GetReqID(ctx))
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
	return "", rejected(ctx, op, status, raw, attr)
}

func (c *Client) Resolve(ctx context.Context, q domain.PeriodQuery) (domain.ResolvedPeriod, error) {
	const op = "period resolve"
	attr := slog.String("postingDate", q.PostingDate.Format(dateFormat))
	target := resolvePath + "?" + url.Values{"ledgerId": {q.LedgerID}, "postingDate": {q.PostingDate.Format(dateFormat)}, "sourceSystem": {sourceAP}}.Encode()
	status, raw, err := c.call(ctx, op, http.MethodGet, target, nil, q.LegalEntityID, attr)
	if err != nil {
		return domain.ResolvedPeriod{}, err
	}
	switch status {
	case http.StatusOK:
		var out resolveBody
		if err := json.Unmarshal(raw, &out); err != nil || !ids.Valid(out.FiscalPeriodID) || out.Status == "" {
			slog.ErrorContext(ctx, "period resolve response unusable", "err", err, attr, "correlationId", middleware.GetReqID(ctx))
			return domain.ResolvedPeriod{}, errors.New("period resolve: response has no usable period")
		}
		return domain.ResolvedPeriod{FiscalPeriodID: out.FiscalPeriodID, Status: out.Status}, nil
	case http.StatusNotFound, http.StatusConflict:
		var problem problemBody
		if json.Unmarshal(raw, &problem) == nil {
			switch problem.id() {
			case codeNoPeriod:
				return domain.ResolvedPeriod{}, domain.ErrFiscalPeriodNotFound
			case codeAmbiguous:
				return domain.ResolvedPeriod{}, domain.ErrFiscalPeriodAmbiguous
			}
		}
	}
	return domain.ResolvedPeriod{}, rejected(ctx, op, status, raw, attr)
}
