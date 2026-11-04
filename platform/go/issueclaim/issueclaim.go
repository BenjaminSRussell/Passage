// Package issueclaim is the client of inventory's issue claims: the atomic
// "may this quantity leave the warehouse" call that warehouse picks,
// packing-slip posts and production consumption make so held (quality) stock
// is never moved before the usage decision.
package issueclaim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 3 * time.Second
	audience        = "inventory"
	dutyClaim       = "inventory.issue.claim"
	claimsPath      = "/v1/inventory/issue-claims"
	releasePath     = claimsPath + ":release"
	codeHold        = "QUALITY_HOLD"
	maxResponseSize = 1 << 20
)

type Kind string

const (
	Pick    Kind = "PICK"
	Ship    Kind = "SHIP"
	Consume Kind = "CONSUME"
)

var (
	ErrHeld       = errors.New("the quantity is on quality hold")
	ErrNotProduct = errors.New("the item is not an inventory product id, so its holds cannot be checked")
)

// Refused reports an error that means the movement must not happen (as
// opposed to an outage, which is retryable).
func Refused(err error) bool { return errors.Is(err, ErrHeld) || errors.Is(err, ErrNotProduct) }

// Reason completes "item X ..." for a refusal, so an item that is not a
// product id is not reported as held.
func Reason(err error) string {
	if errors.Is(err, ErrNotProduct) {
		return "is not an inventory product id, so its quality holds cannot be checked"
	}
	return "is on quality hold"
}

// Claim is one movement of Qty of a product out of a warehouse (and optional
// batch). Key names the movement; inventory treats a repeat of the key it
// receives as a replay, never a second claim. ClaimAll sends Key with an
// attempt suffix, so each attempt of a movement owns its claim.
type Claim struct {
	LegalEntityID string
	ProductID     string
	Warehouse     string
	Batch         string
	Kind          Kind
	Qty           decimal.Decimal
	Key           string
}

// Claimer takes and withdraws claims. A nil Claimer means INVENTORY_URL is
// unset and movements are not checked.
type Claimer interface {
	Claim(ctx context.Context, c Claim) error
	Release(ctx context.Context, c Claim)
}

// ClaimAll merges the claims that share a key (two lines of one product are
// one movement), claims each, and withdraws the earlier ones when a later one
// is refused. The returned undo withdraws them all, for a movement whose own
// transaction fails after the claims were taken.
//
// Each call is one attempt and sends every key with a fresh attempt id. Two
// attempts of one movement (a double submit at the same version, or a retry
// that starts while the failed attempt is still withdrawing) therefore never
// share a claim: if they did, the attempt that fails would withdraw the claim
// the attempt that commits relies on, and its quantity would leave the tally.
// A claim whose withdrawal is lost stays and only makes later movements of its
// kind stricter.
func ClaimAll(ctx context.Context, c Claimer, claims []Claim) (undo func(), err error) {
	attempt := ids.New()
	merged := make([]Claim, 0, len(claims))
	for _, cl := range claims {
		if !cl.Qty.IsPositive() {
			continue
		}
		if i := slices.IndexFunc(merged, func(m Claim) bool { return m.Key == cl.Key }); i >= 0 {
			merged[i].Qty = merged[i].Qty.Add(cl.Qty)
			continue
		}
		merged = append(merged, cl)
	}
	var taken []Claim
	undo = func() {
		for _, t := range taken {
			c.Release(context.WithoutCancel(ctx), t)
		}
	}
	for _, cl := range merged {
		cl.Key += ":" + attempt
		if err := c.Claim(ctx, cl); err != nil {
			undo()
			return func() {}, err
		}
		taken = append(taken, cl)
	}
	return undo, nil
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("inventory client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("inventory client needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}, nil
}

// FromEnv builds the client from INVENTORY_URL and the service signing key.
// It returns a nil Claimer (an untyped nil, safe to compare) when INVENTORY_URL is unset.
func FromEnv(getenv func(string) string) (Claimer, error) {
	baseURL := getenv("INVENTORY_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("INVENTORY_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	c, err := New(baseURL, signer, nil)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Claim(ctx context.Context, cl Claim) error {
	if !ids.Valid(cl.ProductID) {
		return ErrNotProduct
	}
	status, body, err := c.post(ctx, claimsPath, cl, map[string]string{"legalEntityId": cl.LegalEntityID, "productId": cl.ProductID,
		"warehouse": cl.Warehouse, "batch": cl.Batch, "kind": string(cl.Kind), "qty": cl.Qty.String()})
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return nil
	}
	var problem struct {
		Code string `json:"code"`
	}
	if status == http.StatusUnprocessableEntity && json.Unmarshal(body, &problem) == nil && problem.Code == codeHold {
		return ErrHeld
	}
	slog.ErrorContext(ctx, "inventory refused the issue claim", "status", status, "correlationId", middleware.GetReqID(ctx))
	return fmt.Errorf("inventory issue claim: status %d", status)
}

// Release withdraws a claim whose movement did not commit. It is best effort:
// a claim that stays only makes later movements of the same kind stricter.
func (c *Client) Release(ctx context.Context, cl Claim) {
	status, _, err := c.post(ctx, releasePath, cl, struct{}{})
	if err != nil || status != http.StatusNoContent {
		slog.ErrorContext(ctx, "inventory issue claim was not released", "status", status, "err", err, "correlationId", middleware.GetReqID(ctx))
	}
}

func (c *Client) post(ctx context.Context, path string, cl Claim, body any) (int, []byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, errors.New("inventory issue claim: no principal in context")
	}
	if !ids.Valid(cl.LegalEntityID) {
		return 0, nil, errors.New("inventory issue claim needs a legal entity")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, fmt.Errorf("inventory issue claim: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", cl.Key)
	if _, err := c.signer.ApplyDuties(req, principal, audience, cl.LegalEntityID, []string{dutyClaim}); err != nil {
		return 0, nil, fmt.Errorf("inventory issue claim: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "inventory issue claim failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("inventory issue claim: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return 0, nil, fmt.Errorf("inventory issue claim: %w", err)
	}
	return resp.StatusCode, out, nil
}
