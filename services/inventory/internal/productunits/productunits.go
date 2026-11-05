// Package productunits converts a document quantity into a product's
// inventory unit through the product service (#480): GET
// /v1/product/uom/convert without a target unit converts into the product's
// base unit, which the answer names. Calls are signed as inventory with the
// service-only duty product.uom.convert, which reaches that operation only.
package productunits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	audience        = "product"
	dutyConvert     = "product.uom.convert"
	defaultTimeout  = 10 * time.Second
	maxResponseSize = 64 << 10
	maxErrorDetail  = 256
)

// ErrNoConversion is returned when the product service answers that the
// quantity cannot be converted: the product is unknown or no conversion path
// leads from the unit into its base unit. Retrying does not change the answer.
var ErrNoConversion = errors.New("no unit conversion into the product's inventory unit")

// Client calls the product service's unit conversion.
type Client struct {
	baseURL string
	signer  *tenant.ServiceSigner
	http    *http.Client
}

// New returns a client for the product service at baseURL that signs its
// calls with signer.
func New(baseURL string, signer *tenant.ServiceSigner, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: hc}
}

// FromEnv builds the client from PRODUCT_URL and the service signing key
// (tenant.ServiceSignerFromEnv). It returns nil, nil without PRODUCT_URL; with
// it a signing key is required.
func FromEnv(getenv func(string) string) (*Client, error) {
	base := getenv("PRODUCT_URL")
	if base == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("PRODUCT_URL is set but no service signing key is configured")
	}
	timeout := defaultTimeout
	if v, err := time.ParseDuration(getenv("PRODUCT_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(base, signer, &http.Client{Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}), nil
}

// ToInventoryUnit converts qty in unit into the inventory (base) unit of the
// product and returns the converted quantity and that unit. The call is
// signed for legalEntityID on behalf of the principal in ctx. An answer that
// refuses the conversion wraps ErrNoConversion; any other failure (network,
// 5xx, a refused token, throttling) may pass on a retry.
func (c *Client) ToInventoryUnit(ctx context.Context, legalEntityID, productID, unit string, qty decimal.Decimal) (decimal.Decimal, string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Zero, "", errors.New("no principal in context")
	}
	q := url.Values{"product": {productID}, "from": {unit}, "qty": {qty.String()}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/product/uom/convert?"+q.Encode(), nil)
	if err != nil {
		return decimal.Zero, "", err
	}
	signed, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{dutyConvert})
	if err == nil && !signed {
		err = errors.New("no service signer configured")
	}
	if err != nil {
		return decimal.Zero, "", fmt.Errorf("sign product request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return decimal.Zero, "", fmt.Errorf("product request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return decimal.Zero, "", fmt.Errorf("read product response: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case refused(resp.StatusCode):
		return decimal.Zero, "", fmt.Errorf("%w: %s %s: product answered %d %s", ErrNoConversion, qty, unit, resp.StatusCode, detail(raw))
	default:
		return decimal.Zero, "", fmt.Errorf("product answered %d %s", resp.StatusCode, detail(raw))
	}
	var body struct {
		To       string `json:"to"`
		Quantity string `json:"quantity"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return decimal.Zero, "", fmt.Errorf("decode product response: %w", err)
	}
	out, err := decimal.NewFromString(body.Quantity)
	if err != nil || strings.TrimSpace(body.To) == "" {
		return decimal.Zero, "", fmt.Errorf("product response without a quantity and unit: %s", detail(raw))
	}
	return out, strings.ToUpper(strings.TrimSpace(body.To)), nil
}

// refused reports the answers that will not change on a retry: the request
// was understood and the conversion refused (bad input, unknown product, no
// conversion path). A refused token (401, 403), a timeout and throttling may
// pass once the deployment is fixed or the load falls.
func refused(status int) bool {
	switch status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func detail(raw []byte) string {
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &p) == nil && (p.Code != "" || p.Detail != "") {
		return strings.TrimSpace(p.Code + " " + p.Detail)
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > maxErrorDetail {
		s = s[:maxErrorDetail]
	}
	return s
}
