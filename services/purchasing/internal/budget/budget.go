// Package budget is purchasing's port to budget control: when a PO line is created from a requisition line, the
// requisition line's pre-encumbrance becomes the encumbrance of the PO line (FR-38.5).
package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const (
	audience    = "budgeting"
	dutyConvert = "budget.encumbrance.convert"
	convertPath = "/v1/budget/encumbrances:convert"
	maxBody     = 64 << 10
)

// Client calls budgeting with a signed service token that carries only the convert duty.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client for the budgeting service at baseURL. A nil httpClient gets a 10 second timeout.
func New(baseURL string, httpClient *http.Client, signer *tenant.ServiceSigner) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}
}

// Convert asks budgeting to turn the pre-encumbrance of a requisition line into the encumbrance of a PO line.
// A requisition line that was never budget-checked (404) or whose pre-encumbrance is already converted (409
// BUD-0071) has nothing to convert and is not an error. The Idempotency-Key is derived from the PO line, so
// repeating the call is safe.
func (c *Client) Convert(ctx context.Context, legalEntityID, requisitionLineID, orderID, orderLineID string) error {
	what := "budget conversion of requisition line " + requisitionLineID
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return fmt.Errorf("%s: no principal in context", what)
	}
	if c.signer == nil {
		return fmt.Errorf("%s: no service signing key configured", what)
	}
	raw, err := json.Marshal(map[string]string{"requisitionLineId": requisitionLineID, "purchaseOrderId": orderID, "purchaseOrderLineId": orderLineID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+convertPath, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "po-line-convert-"+orderLineID)
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyConvert}); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return nil // nothing was reserved for the line
	case resp.StatusCode == http.StatusConflict && bytes.Contains(body, []byte("BUD-0071")):
		return nil // already converted
	}
	return fmt.Errorf("%s: status %d: %s", what, resp.StatusCode, strings.TrimSpace(string(body)))
}

// Converter is what the hook needs from the client.
type Converter interface {
	Convert(ctx context.Context, legalEntityID, requisitionLineID, orderID, orderLineID string) error
}

// Hook returns the store hook that converts the pre-encumbrance of every requisition line the created orders
// consume. A failure is logged with the ids and does not fail the order, which already exists; the call is idempotent
// per PO line, so it can be repeated.
func Hook(c Converter) store.OrderHook {
	return func(ctx context.Context, orders []store.PurchaseOrder) {
		for _, o := range orders {
			for _, l := range o.Lines {
				if l.RequisitionLineID == "" || l.ID == "" {
					continue
				}
				if err := c.Convert(ctx, o.LegalEntityID, l.RequisitionLineID, o.ID, l.ID); err != nil {
					slog.ErrorContext(ctx, "budget pre-encumbrance conversion failed", "err", err, "purchaseOrderId", o.ID, "purchaseOrderLineId", l.ID,
						"requisitionLineId", l.RequisitionLineID, "correlationId", middleware.GetReqID(ctx))
				}
			}
		}
	}
}
