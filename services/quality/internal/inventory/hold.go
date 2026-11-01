package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 3 * time.Second
	audience        = "inventory"
	dutyHoldApply   = "inventory.hold.apply"
	holdsPath       = "/v1/inventory/holds"
	releasePath     = "/v1/inventory/holds:release-source"
	holdReason      = "QUALITY"
	maxResponseBody = 1 << 20
	idempotencyKey  = "Idempotency-Key"
)

var (
	ErrRejected = errors.New("inventory rejected the hold call")
	ErrNoHold   = fmt.Errorf("%w: inventory knows no hold for the source", ErrRejected)
)

type Call struct {
	TenantID      string
	LegalEntityID string
	UserID        string
	Key           string
	SourceType    string
	SourceRef     string
	ProductID     string
	Dims          map[string]string
	Qty           decimal.Decimal
	KeepQty       decimal.Decimal
	WholeLot      bool
	MoveToType    string
	MoveToRef     string
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}
}

func (c *Client) Place(ctx context.Context, call Call) error {
	body := map[string]any{"legalEntityId": call.LegalEntityID, "productId": call.ProductID, "dimensions": call.Dims,
		"allowPartial": true, "reason": holdReason, "sourceType": call.SourceType, "sourceRef": call.SourceRef}
	if call.WholeLot {
		body["wholeLot"] = true
	} else {
		body["qty"] = call.Qty.String()
	}
	_, err := c.post(ctx, call, holdsPath, body, true)
	return err
}

func (c *Client) Release(ctx context.Context, call Call) error {
	body := map[string]any{"sourceType": call.SourceType, "sourceRef": call.SourceRef, "keepQty": call.KeepQty.String()}
	if call.MoveToType != "" {
		body["moveKeptTo"] = map[string]string{"sourceType": call.MoveToType, "sourceRef": call.MoveToRef}
	}
	raw, err := c.post(ctx, call, releasePath, body, false)
	if err != nil {
		return err
	}
	var res struct {
		Known bool `json:"known"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || !res.Known {
		return fmt.Errorf("%w: %s %s", ErrNoHold, call.SourceType, call.SourceRef)
	}
	return nil
}

func (c *Client) post(ctx context.Context, call Call, path string, body map[string]any, keyed bool) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("inventory hold %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if keyed {
		req.Header.Set(idempotencyKey, call.Key)
	}
	if c.signer == nil {
		return nil, fmt.Errorf("inventory hold %s: no service signer", path)
	}
	principal := tenant.Principal{TenantID: call.TenantID, LegalEntityID: call.LegalEntityID, UserID: call.UserID}
	if _, err := c.signer.ApplyDuties(req, principal, audience, call.LegalEntityID, []string{dutyHoldApply}); err != nil {
		return nil, fmt.Errorf("inventory hold %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inventory hold %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return detail, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: %s %d %s", ErrRejected, path, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	return nil, fmt.Errorf("inventory hold %s: status %d (request %s)", path, resp.StatusCode, middleware.GetReqID(ctx))
}
