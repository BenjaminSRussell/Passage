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
	"net/url"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// The calls the cXML exchange makes on purchasing and receiving (#35, 35-4): read a purchase order to send it as an OrderRequest,
// confirm it when its confirmation comes back, and create the receiving ASN of a ship notice. Each is signed with the narrow duty of
// the grant edi -> service (policies/authz/data.json service_grants, platform/go/tenant).
const (
	audReceiving = "receiving"

	DutyPurchaseOrderRead    = "procurement.purchase-order.lookup"
	DutyPurchaseOrderConfirm = "procurement.purchase-order.vendor-confirm"
	DutyASNMaintain          = "receiving.asn.maintain"
)

// PurchaseOrderLine and PurchaseOrder are the parts of purchasing's PurchaseOrder the exchange reads.
type PurchaseOrderLine struct {
	LineNo       int    `json:"lineNo"`
	ProductID    string `json:"productId"`
	Description  string `json:"description"`
	Qty          string `json:"qty"`
	Unit         string `json:"unit"`
	UnitPrice    string `json:"unitPrice"`
	DeliveryDate string `json:"deliveryDate"`
	Status       string `json:"status"`
}

type PurchaseOrder struct {
	ID             string              `json:"id"`
	Number         string              `json:"number"`
	LegalEntityID  string              `json:"legalEntityId"`
	VendorID       string              `json:"vendorId"`
	Currency       string              `json:"currency"`
	ApprovalStatus string              `json:"approvalStatus"`
	RowVersion     int                 `json:"rowVersion"`
	Total          string              `json:"total"`
	Lines          []PurchaseOrderLine `json:"lines"`
	CreatedAt      string              `json:"createdAt"`
}

// ASNLine and ASNRequest are the body of receiving's POST /v1/receiving/asns.
type ASNLine struct {
	POLineNo int    `json:"poLineNo"`
	Qty      string `json:"qty"`
}

type ASNRequest struct {
	LegalEntityID   string    `json:"legalEntityId"`
	PurchaseOrderID string    `json:"purchaseOrderId"`
	Carrier         string    `json:"carrier,omitempty"`
	TrackingNumber  string    `json:"trackingNumber,omitempty"`
	ShipDate        string    `json:"shipDate,omitempty"`
	ExpectedArrival string    `json:"expectedArrival,omitempty"`
	Lines           []ASNLine `json:"lines"`
}

type ASNRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// DownstreamRefusedError is a service refusing a call for a reason in the document itself (validation, a rule, a conflict): sending
// it again fails the same way, so the message is parked with the code.
type DownstreamRefusedError struct {
	Service string
	Status  int
	Code    string
	Detail  string
}

func (e *DownstreamRefusedError) Error() string {
	return fmt.Sprintf("%s refused the call (%d %s): %s", e.Service, e.Status, e.Code, e.Detail)
}

// WithReceiving sets the receiving base URL the ASN calls go to.
func (c *Client) WithReceiving(url string) *Client {
	c.receiving = strings.TrimRight(url, "/")
	return c
}

// PurchaseOrder reads one purchase order.
func (c *Client) PurchaseOrder(ctx context.Context, legalEntityID, id string) (PurchaseOrder, error) {
	var out PurchaseOrder
	err := c.get(ctx, &out, audPurchasing, DutyPurchaseOrderRead, c.purchasing, legalEntityID, "/v1/procurement/purchase-orders/"+url.PathEscape(id))
	return out, err
}

// ConfirmPurchaseOrder moves an APPROVED purchase order to CONFIRMED (If-Match is the row version read with it). A repeat with the same
// key returns the first answer.
func (c *Client) ConfirmPurchaseOrder(ctx context.Context, legalEntityID, id string, rowVersion int, key string) error {
	return c.post(ctx, "purchasing", audPurchasing, DutyPurchaseOrderConfirm, c.purchasing, legalEntityID,
		"/v1/procurement/purchase-orders/"+url.PathEscape(id)+":confirm", key, map[string]string{"If-Match": strconv.Itoa(rowVersion)}, nil, nil)
}

// CreateASN records the advance ship notice of a vendor against a purchase order; key makes a retry return the notice already created.
func (c *Client) CreateASN(ctx context.Context, legalEntityID, key string, body ASNRequest) (ASNRef, error) {
	var out ASNRef
	err := c.post(ctx, "receiving", audReceiving, DutyASNMaintain, c.receiving, legalEntityID, "/v1/receiving/asns", key, nil, body, &out)
	if err == nil && out.ID == "" {
		return ASNRef{}, errors.New("receiving: response is not an advance ship notice")
	}
	return out, err
}

// post is one signed POST: a body (or none), an Idempotency-Key, extra headers and the answer decoded into out when not nil. 4xx
// answers other than 401, 403, 408 and 429 are *DownstreamRefusedError.
func (c *Client) post(ctx context.Context, service, aud, duty, base, legalEntityID, path, key string, headers map[string]string, body, out any) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New(service + " call: no principal in context")
	}
	if base == "" || c.signer == nil {
		return fmt.Errorf("%s %s: %s is not configured", service, path, aud)
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := c.signer.ApplyDuties(req, principal, aud, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("%s %s: %w: %v", service, path, ErrForbidden, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, service+" call transport failed", "err", err, "path", path)
		return fmt.Errorf("%s %s: %w", service, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("%s %s: %w", service, path, err)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil {
			if err := json.Unmarshal(answer, out); err != nil {
				return fmt.Errorf("%s %s: response is not the expected document", service, path)
			}
		}
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s %s: %w", service, path, ErrForbidden)
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
		return &DownstreamRefusedError{Service: service, Status: resp.StatusCode, Code: p.Code, Detail: detail}
	}
	return fmt.Errorf("%s %s: unexpected status %d", service, path, resp.StatusCode)
}
