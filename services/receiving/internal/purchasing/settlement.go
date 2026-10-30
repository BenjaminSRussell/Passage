package purchasing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const audience = "purchasing"

type settlementBody struct {
	SettlementRef string `json:"settlementRef"`
	Amount        string `json:"amount"`
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

func (c *Client) MarkInvoiced(ctx context.Context, legalEntityID, orderID string, lineNo, seq int, settlementRef string, amount decimal.Decimal) error {
	what := fmt.Sprintf("plan row mark %s/%d/%d", orderID, lineNo, seq)
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return fmt.Errorf("%s: no principal in context", what)
	}
	if c.signer == nil {
		return fmt.Errorf("%s: no service signing key configured", what)
	}
	raw, err := json.Marshal(settlementBody{SettlementRef: settlementRef, Amount: amount.String()})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	path := fmt.Sprintf(domain.PlanRowSettlementPathTmpl, url.PathEscape(orderID), lineNo, seq)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.Apply(req, principal, audience, legalEntityID); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, what+" transport failed", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseSize))
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests:
		return fmt.Errorf("%s: status %d: %w", what, resp.StatusCode, domain.ErrPlanRowMarkRejected)
	}
	err = fmt.Errorf("%s: unexpected status %d", what, resp.StatusCode)
	slog.ErrorContext(ctx, what+" rejected", "err", err, "status", resp.StatusCode, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
	return err
}
