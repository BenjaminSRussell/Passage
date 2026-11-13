package sources

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

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	audPurchasing = "purchasing"
	// DutyRequisitionCreate is the requester's duty: the token carries the requester as act, so the requisition is theirs.
	DutyRequisitionCreate = "procurement.requisition.create"
)

// RequisitionLine is one line of purchasing's RequisitionInput: a line by description and category with its estimate.
type RequisitionLine struct {
	Description        string `json:"description"`
	Category           string `json:"category"`
	Qty                string `json:"qty"`
	Unit               string `json:"unit"`
	EstimatedUnitPrice string `json:"estimatedUnitPrice"`
	RequestedDate      string `json:"requestedDate"`
	// CatalogSource PUNCHOUT, SupplierPartID and NeedsApproval mark a punchout line in purchasing (BR-35.13): a flagged line keeps
	// the requisition from being approved automatically.
	CatalogSource  string `json:"catalogSource,omitempty"`
	SupplierPartID string `json:"supplierPartId,omitempty"`
	NeedsApproval  bool   `json:"needsApproval,omitempty"`
}

// RequisitionRequest is the body of POST /v1/procurement/requisitions.
type RequisitionRequest struct {
	LegalEntityID string            `json:"legalEntityId"`
	Currency      string            `json:"currency"`
	Lines         []RequisitionLine `json:"lines"`
}

// RequisitionRef is the requisition purchasing created.
type RequisitionRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// RequisitionRefusedError is purchasing refusing the requisition for a reason in the requisition itself (validation, a rule): sending
// it again fails the same way.
type RequisitionRefusedError struct {
	Status int
	Code   string
	Detail string
}

func (e *RequisitionRefusedError) Error() string {
	return fmt.Sprintf("purchasing refused the requisition (%d %s): %s", e.Status, e.Code, e.Detail)
}

// WithPurchasing sets the purchasing base URL the requisition calls go to.
func (c *Client) WithPurchasing(url string) *Client {
	c.purchasing = strings.TrimRight(url, "/")
	return c
}

// CreateRequisition creates a DRAFT requisition for the requester in the context (the principal's user is the token's act), under
// the Idempotency-Key key, so a retry returns the requisition already created.
func (c *Client) CreateRequisition(ctx context.Context, legalEntityID, key string, body RequisitionRequest) (RequisitionRef, error) {
	const path = "/v1/procurement/requisitions"
	var out RequisitionRef
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return out, errors.New("requisition: no principal in context")
	}
	if c.purchasing == "" || c.signer == nil {
		return out, fmt.Errorf("requisition %s: purchasing is not configured", path)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.purchasing+path, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if _, err := c.signer.ApplyDuties(req, principal, audPurchasing, legalEntityID, []string{DutyRequisitionCreate}); err != nil {
		return out, fmt.Errorf("requisition %s: %w: %v", path, ErrForbidden, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "requisition transport failed", "err", err, "path", path)
		return out, fmt.Errorf("requisition %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return out, fmt.Errorf("requisition %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		if err := json.Unmarshal(answer, &out); err != nil || out.ID == "" {
			return RequisitionRef{}, fmt.Errorf("requisition %s: response is not a requisition", path)
		}
		return out, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return out, fmt.Errorf("requisition %s: %w", path, ErrForbidden)
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests:
		var p struct {
			Code   string            `json:"code"`
			Detail string            `json:"detail"`
			Errors map[string]string `json:"errors"`
		}
		_ = json.Unmarshal(answer, &p)
		detail := p.Detail
		for field, msg := range p.Errors {
			detail += "; " + field + " " + msg
		}
		return out, &RequisitionRefusedError{Status: resp.StatusCode, Code: p.Code, Detail: detail}
	}
	return out, fmt.Errorf("requisition %s: unexpected status %d", path, resp.StatusCode)
}
