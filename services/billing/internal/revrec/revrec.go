// Package revrec reads the allocation of the revenue contract of a sales order (BR-29.1) from the revrec
// service. It signs a service token restricted to the revrec.allocation.read duty; the end user's headers
// are never forwarded.
package revrec

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
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 5 * time.Second
	MethodPoint     = "POINT_IN_TIME"
	audience        = "revrec"
	dutyRead        = "revrec.allocation.read"
	contractsPath   = "/v1/revrec/contracts"
	statusCancelled = "CANCELLED"
	maxResponseSize = 4 << 20
	listLimit       = 200
)

var (
	ErrNoContract = errors.New("revrec: the order has no revenue contract")
	ErrAmbiguous  = errors.New("revrec: the order has more than one revenue contract")
)

type Obligation struct {
	ProductID string
	Allocated decimal.Decimal
	Method    string
}

type Allocation struct {
	Currency    string
	Obligations []Obligation
}

// Reader returns the allocation of the one live revenue contract of an order of a legal entity: ErrNoContract when there is
// none, ErrAmbiguous when there are several, any other error when revrec did not answer.
type Reader interface {
	Allocation(ctx context.Context, legalEntityID, orderID string) (Allocation, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("revrec client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("revrec client needs a service signing key: the end user's headers are never forwarded")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from REVREC_URL and the service signing key variables; it returns nil, nil when REVREC_URL is unset
// and an error when it is set without a signing key.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("REVREC_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return New(baseURL, signer, nil)
}

type contractHeader struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	OrderID       string `json:"orderId"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
}

type listBody struct {
	Items      []contractHeader `json:"items"`
	NextCursor string           `json:"nextCursor"`
}

type contractBody struct {
	contractHeader
	Obligations []struct {
		ProductID string `json:"productId"`
		Allocated string `json:"allocated"`
		Method    string `json:"method"`
	} `json:"obligations"`
}

func (c *Client) Allocation(ctx context.Context, legalEntityID, orderID string) (Allocation, error) {
	le, leOK := ids.Canonical(legalEntityID)
	order, orderOK := ids.Canonical(orderID)
	if !leOK || !orderOK {
		return Allocation{}, fmt.Errorf("revrec allocation: legal entity %q and order %q must be UUIDs", legalEntityID, orderID)
	}
	var list listBody
	query := url.Values{"legalEntityId": {le}, "orderId": {order}, "limit": {fmt.Sprint(listLimit)}}
	if err := c.get(ctx, le, contractsPath+"?"+query.Encode(), &list); err != nil {
		return Allocation{}, err
	}
	if list.NextCursor != "" {
		return Allocation{}, ErrAmbiguous
	}
	var live []contractHeader
	for _, h := range list.Items {
		if h.Status != statusCancelled {
			live = append(live, h)
		}
	}
	switch len(live) {
	case 0:
		return Allocation{}, ErrNoContract
	case 1:
	default:
		return Allocation{}, ErrAmbiguous
	}
	id, idOK := ids.Canonical(live[0].ID)
	if !idOK {
		return Allocation{}, fmt.Errorf("revrec allocation: contract id %q is not a UUID", live[0].ID)
	}
	var contract contractBody
	if err := c.get(ctx, le, contractsPath+"/"+id, &contract); err != nil {
		return Allocation{}, err
	}
	if gotLE, _ := ids.Canonical(contract.LegalEntityID); gotLE != le {
		return Allocation{}, errors.New("revrec allocation: the contract belongs to another legal entity")
	}
	if gotOrder, _ := ids.Canonical(contract.OrderID); gotOrder != order {
		return Allocation{}, errors.New("revrec allocation: the contract belongs to another order")
	}
	out := Allocation{Currency: contract.Currency}
	for _, o := range contract.Obligations {
		allocated, err := decimal.NewFromString(o.Allocated)
		product, productOK := ids.Canonical(o.ProductID)
		if err != nil || !productOK {
			return Allocation{}, fmt.Errorf("revrec allocation: obligation of product %q has allocated %q", o.ProductID, o.Allocated)
		}
		out.Obligations = append(out.Obligations, Obligation{ProductID: product, Allocated: allocated, Method: o.Method})
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, legalEntityID, path string, into any) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("revrec allocation: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("revrec allocation: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRead}); err != nil {
		return fmt.Errorf("revrec allocation: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "revrec request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("revrec allocation: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "revrec response unreadable", "err", err, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("revrec allocation: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "revrec refused the allocation read", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("revrec allocation: status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		slog.ErrorContext(ctx, "revrec response unusable", "err", err, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("revrec allocation: %w", err)
	}
	return nil
}
