// Package budget is purchasing's port to budget control (FR-38.4, FR-38.5): an approved requisition is checked against the
// budget and its lines reserve pre-encumbrances, the pre-encumbrance of a cancelled line is given back, and when a PO line is
// created from a requisition line the pre-encumbrance becomes the encumbrance of the PO line. Calls that did not get an answer
// are kept in purchasing's budget_call queue and repeated (Retry).
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
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const (
	audience           = "budgeting"
	dutyConvert        = "budget.encumbrance.convert"
	dutyCheck          = "budget.requisition.check"
	convertPath        = "/v1/budget/encumbrances:convert"
	checkPath          = "/v1/budget/requisition-checks"
	releaseLinePath    = "/v1/budget/requisition-checks:release-line"
	maxBody            = 64 << 10
	maxRetryPerCall    = 100
	checkResultFail    = "FAIL"
	checkNotControlled = "NOT_CONTROLLED"
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

// post sends one signed command to budgeting with only the named duty and returns the status and body. The key makes a
// repeat of the same command safe.
func (c *Client) post(ctx context.Context, what, path, key, duty, legalEntityID string, payload any) (int, []byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, fmt.Errorf("%s: no principal in context", what)
	}
	if c.signer == nil {
		return 0, nil, fmt.Errorf("%s: no service signing key configured", what)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", what, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
		return 0, nil, fmt.Errorf("%s: %w", what, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, body, nil
}

// LineCheck is what budgeting answered for one requisition line.
type LineCheck struct {
	LineID  string          `json:"lineId"`
	Result  string          `json:"result"`
	Net     decimal.Decimal `json:"net"`
	OverPct decimal.Decimal `json:"overPct"`
}

// Check is budgeting's answer to a requisition check: PASS, WARN, FAIL or NOT_CONTROLLED (no control rule covers the legal
// entity yet). FAIL reserved nothing.
type Check struct {
	Result string      `json:"result"`
	Lines  []LineCheck `json:"lines"`
}

// Failed reports a refusal by budget control.
func (c Check) Failed() bool { return strings.EqualFold(c.Result, checkResultFail) }

// CheckRequisition runs the budget check for the lines of an approved requisition that name a GL account and a cost center
// and reserves them as pre-encumbrances. A requisition with no such line is not budget controlled and is not sent. The
// Idempotency-Key is derived from the requisition and its version, so repeating the approval repeats the same check.
func (c *Client) CheckRequisition(ctx context.Context, r store.Requisition) (Check, error) {
	type line struct {
		LineID        string `json:"lineId"`
		GLAccount     string `json:"glAccount"`
		CostCenter    string `json:"costCenter"`
		Amount        string `json:"amount"`
		RequestedDate string `json:"requestedDate"`
	}
	var lines []line
	for _, l := range r.Lines {
		if l.Status == "OPEN" && l.GLAccount != "" && l.CostCenter != "" {
			lines = append(lines, line{LineID: l.ID, GLAccount: l.GLAccount, CostCenter: l.CostCenter, Amount: l.Amount.String(), RequestedDate: l.RequestedDate.String()})
		}
	}
	if len(lines) == 0 {
		return Check{Result: checkNotControlled}, nil
	}
	what := "budget check of requisition " + r.Number
	status, body, err := c.post(ctx, what, checkPath, fmt.Sprintf("requisition-check-%s-%d", r.ID, r.Version), dutyCheck, r.LegalEntityID,
		map[string]any{"legalEntityId": r.LegalEntityID, "requisitionId": r.ID, "currency": r.Currency, "lines": lines})
	if err != nil {
		return Check{}, err
	}
	if status != http.StatusOK {
		return Check{}, fmt.Errorf("%s: status %d: %s", what, status, strings.TrimSpace(string(body)))
	}
	var out Check
	if err := json.Unmarshal(body, &out); err != nil {
		return Check{}, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// ReleaseLine gives back the pre-encumbrance of a cancelled requisition line. A line that was never checked (404) or whose
// pre-encumbrance is already released (409) has nothing to give back and is not an error.
func (c *Client) ReleaseLine(ctx context.Context, legalEntityID, requisitionID, requisitionLineID string) error {
	what := "budget release of requisition line " + requisitionLineID
	status, body, err := c.post(ctx, what, releaseLinePath, "requisition-line-release-"+requisitionLineID, dutyCheck, legalEntityID,
		map[string]string{"requisitionId": requisitionID, "requisitionLineId": requisitionLineID})
	switch {
	case err != nil:
		return err
	case status == http.StatusOK, status == http.StatusNotFound:
		return nil
	case status == http.StatusConflict && bytes.Contains(body, []byte("BUD-0010")):
		return nil
	}
	return fmt.Errorf("%s: status %d: %s", what, status, strings.TrimSpace(string(body)))
}

// Port is what purchasing needs from budget control.
type Port interface {
	Converter
	CheckRequisition(ctx context.Context, r store.Requisition) (Check, error)
	ReleaseLine(ctx context.Context, legalEntityID, requisitionID, requisitionLineID string) error
}

// Converter is what the hook needs from the client.
type Converter interface {
	Convert(ctx context.Context, legalEntityID, requisitionLineID, orderID, orderLineID string) error
}

// Queue is the persistence of owed calls (the store).
type Queue interface {
	BudgetCallsFor(ctx context.Context, purchaseOrderID, requisitionID string) ([]store.BudgetCall, error)
	ListBudgetCalls(ctx context.Context, status string, limit int) ([]store.BudgetCall, error)
	RecordBudgetCall(ctx context.Context, id string, callErr error) error
}

// Run makes one owed call and records the outcome: done when budgeting accepted it, one more failed attempt otherwise.
func Run(ctx context.Context, p Port, q Queue, c store.BudgetCall) error {
	var err error
	switch c.Kind {
	case store.BudgetCallConvert:
		err = p.Convert(ctx, c.LegalEntityID, c.RequisitionLineID, c.PurchaseOrderID, c.PurchaseOrderLineID)
	case store.BudgetCallRelease:
		err = p.ReleaseLine(ctx, c.LegalEntityID, c.RequisitionID, c.RequisitionLineID)
	default:
		err = fmt.Errorf("unknown budget call kind %q", c.Kind)
	}
	if rec := q.RecordBudgetCall(ctx, c.ID, err); rec != nil {
		slog.ErrorContext(ctx, "budget call outcome not recorded", "err", rec, "callId", c.ID, "correlationId", middleware.GetReqID(ctx))
	}
	return err
}

// Hook returns the store hook that makes the conversion calls the created orders owe (written with the orders). A failure
// is logged and stays pending for Retry; it never fails the order, which already exists. The call is idempotent per PO line.
func Hook(p Port, q Queue) store.OrderHook {
	return func(ctx context.Context, orders []store.PurchaseOrder) {
		for _, o := range orders {
			calls, err := q.BudgetCallsFor(ctx, o.ID, "")
			if err != nil {
				slog.ErrorContext(ctx, "budget calls of a created order not read", "err", err, "purchaseOrderId", o.ID, "correlationId", middleware.GetReqID(ctx))
				continue
			}
			for _, c := range calls {
				if c.Status != store.BudgetCallPending {
					continue
				}
				if err := Run(ctx, p, q, c); err != nil {
					slog.ErrorContext(ctx, "budget pre-encumbrance conversion failed; it stays queued", "err", err, "purchaseOrderId", o.ID,
						"purchaseOrderLineId", c.PurchaseOrderLineID, "requisitionLineId", c.RequisitionLineID, "correlationId", middleware.GetReqID(ctx))
				}
			}
		}
	}
}

// Flush makes the pending release calls of one requisition, right after it was cancelled.
func Flush(ctx context.Context, p Port, q Queue, requisitionID string) {
	calls, err := q.BudgetCallsFor(ctx, "", requisitionID)
	if err != nil {
		slog.ErrorContext(ctx, "budget calls of a cancelled requisition not read", "err", err, "requisitionId", requisitionID, "correlationId", middleware.GetReqID(ctx))
		return
	}
	for _, c := range calls {
		if c.Status == store.BudgetCallPending && c.Kind == store.BudgetCallRelease {
			if err := Run(ctx, p, q, c); err != nil {
				slog.ErrorContext(ctx, "budget release failed; it stays queued", "err", err, "requisitionId", requisitionID, "requisitionLineId", c.RequisitionLineID, "correlationId", middleware.GetReqID(ctx))
			}
		}
	}
}

// RetryResult counts what one retry pass did.
type RetryResult struct {
	Tried  int `json:"tried"`
	Done   int `json:"done"`
	Failed int `json:"failed"`
}

// Retry repeats the pending calls of the caller's tenant, oldest first, at most 100 at a time. It is the body of the scheduled
// retry and of the buyer's "try again".
func Retry(ctx context.Context, p Port, q Queue) (RetryResult, error) {
	var res RetryResult
	pending, err := q.ListBudgetCalls(ctx, store.BudgetCallPending, maxRetryPerCall)
	if err != nil {
		return res, err
	}
	for _, c := range pending {
		res.Tried++
		if err := Run(ctx, p, q, c); err != nil {
			res.Failed++
			continue
		}
		res.Done++
	}
	return res, nil
}
