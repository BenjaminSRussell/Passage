package billingclient

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
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout     = 3 * time.Second
	audience    = "billing"
	dutyRead    = "billing.invoice.read"
	invoicePath = "/v1/billing/invoices/"

	dutyReserve     = "billing.credit-reservation.write"
	reservationPath = "/v1/billing/credit-reservations/"
)

var ErrUnsigned = errors.New("billing client needs the service signing key")

var ErrNotFound = errors.New("billing: invoice not found")

type Basis struct {
	Number        string
	LegalEntityID string
	Remaining     map[string]decimal.Decimal
}

type Reader interface {
	TaxBasis(ctx context.Context, legalEntityID, invoiceID string) (Basis, error)
}

var (
	ErrNoRoom    = errors.New("billing: the invoice has no room left for this credit")
	ErrReleased  = errors.New("billing: the reservation was released")
	ErrConsumed  = errors.New("billing: the reservation was already consumed")
	ErrBusy      = errors.New("billing: a concurrent request holds the same key")
	ErrKeyReuse  = errors.New("billing: the reservation key was used with other lines")
	ErrRejected  = errors.New("billing: the reservation request was rejected")
	codeKeyReuse = "idempotency_key_reuse"
	codeNoRoom   = "BIL-0005"
	codeState    = "reservation_state"
	codeInFlight = "idempotency_in_progress"
)

type Line struct {
	LineNo int
	Qty    decimal.Decimal
}

type Reservation struct {
	ID     string
	Status string
	Net    decimal.Decimal
	Known  bool
}

type Reserver interface {
	Reserve(ctx context.Context, legalEntityID, invoiceID, key, reference string, lines []Line) (Reservation, error)
	Commit(ctx context.Context, legalEntityID, reservationID string) error
	Release(ctx context.Context, legalEntityID, reservationID string) error
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("BILLING_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, ErrUnsigned
	}
	d := timeout
	if v, err := time.ParseDuration(getenv("BILLING_TIMEOUT")); err == nil && v > 0 {
		d = v
	}
	noRedirect := http.Client{Timeout: d, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

type invoiceBody struct {
	Number        string `json:"number"`
	LegalEntityID string `json:"legalEntityId"`
	Status        string `json:"status"`
	Type          string `json:"type"`
	Lines         []struct {
		TaxCode     string `json:"taxCode"`
		Tax         string `json:"tax"`
		CreditedTax string `json:"creditedTax"`
	} `json:"lines"`
}

func (c *Client) TaxBasis(ctx context.Context, legalEntityID, invoiceID string) (Basis, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return Basis{}, errors.New("billing: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+invoicePath+url.PathEscape(invoiceID), nil)
	if err != nil {
		return Basis{}, fmt.Errorf("billing: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRead}); err != nil {
		return Basis{}, fmt.Errorf("billing: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "billing request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return Basis{}, fmt.Errorf("billing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return Basis{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "billing refused the invoice read", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return Basis{}, fmt.Errorf("billing: unexpected status %d", resp.StatusCode)
	}
	var body invoiceBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return Basis{}, fmt.Errorf("billing: decode invoice: %w", err)
	}
	if body.Type == "CREDIT_NOTE" || body.Status == "REVERSED" || body.Status == "PROPOSAL" {
		return Basis{}, ErrNotFound
	}
	out := Basis{Number: body.Number, LegalEntityID: body.LegalEntityID, Remaining: map[string]decimal.Decimal{}}
	for _, l := range body.Lines {
		if l.TaxCode == "" {
			continue
		}
		tax, terr := optional(l.Tax)
		credited, cerr := optional(l.CreditedTax)
		if err := errors.Join(terr, cerr); err != nil {
			return Basis{}, fmt.Errorf("billing: invoice line tax: %w", err)
		}
		out.Remaining[l.TaxCode] = out.Remaining[l.TaxCode].Add(tax).Sub(credited)
	}
	return out, nil
}

type reservationBody struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Code   string `json:"code"`
	Totals struct {
		Net     string `json:"net"`
		Charges string `json:"charges"`
	} `json:"totals"`
}

func (c *Client) send(ctx context.Context, legalEntityID, path, key string, body any) (reservationBody, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return reservationBody{}, errors.New("billing: no principal in context")
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return reservationBody{}, fmt.Errorf("billing: %w", err)
		}
		payload = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, payload)
	if err != nil {
		return reservationBody{}, fmt.Errorf("billing: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyReserve}); err != nil {
		return reservationBody{}, fmt.Errorf("billing: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "billing request failed", "path", path, "err", err, "correlationId", middleware.GetReqID(ctx))
		return reservationBody{}, fmt.Errorf("billing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out reservationBody
	_ = json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		return out, nil
	case resp.StatusCode == http.StatusNotFound:
		return reservationBody{}, ErrNotFound
	case resp.StatusCode == http.StatusUnprocessableEntity && out.Code == codeNoRoom:
		return reservationBody{}, ErrNoRoom
	case resp.StatusCode == http.StatusConflict && out.Code == codeInFlight:
		return reservationBody{}, ErrBusy
	case resp.StatusCode == http.StatusConflict && out.Code == codeState:
		return out, errStateConflict
	case resp.StatusCode == http.StatusUnprocessableEntity && out.Code == codeKeyReuse:
		return reservationBody{}, ErrKeyReuse
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity:
		slog.ErrorContext(ctx, "billing rejected the reservation request", "path", path, "status", resp.StatusCode, "code", out.Code, "correlationId", middleware.GetReqID(ctx))
		return reservationBody{}, fmt.Errorf("%w (status %d code %q)", ErrRejected, resp.StatusCode, out.Code)
	}
	slog.ErrorContext(ctx, "billing refused the reservation call", "path", path, "status", resp.StatusCode, "code", out.Code, "correlationId", middleware.GetReqID(ctx))
	return reservationBody{}, fmt.Errorf("billing: unexpected status %d", resp.StatusCode)
}

var errStateConflict = errors.New("billing: reservation state conflict")

func (c *Client) Reserve(ctx context.Context, legalEntityID, invoiceID, key, reference string, lines []Line) (Reservation, error) {
	type wireLine struct {
		LineNo int    `json:"lineNo"`
		Qty    string `json:"qty"`
	}
	wire := make([]wireLine, 0, len(lines))
	for _, l := range lines {
		wire = append(wire, wireLine{LineNo: l.LineNo, Qty: l.Qty.String()})
	}
	out, err := c.send(ctx, legalEntityID, invoicePath+url.PathEscape(invoiceID)+"/credit-reservations", key,
		map[string]any{"reference": reference, "lines": wire})
	if err != nil {
		return Reservation{}, err
	}
	res := Reservation{ID: out.ID, Status: out.Status}
	net, nerr := optional(out.Totals.Net)
	charges, cerr := optional(out.Totals.Charges)
	if out.Totals.Net != "" && nerr == nil && cerr == nil {
		res.Net, res.Known = net.Add(charges), true
	}
	return res, nil
}

func (c *Client) Commit(ctx context.Context, legalEntityID, reservationID string) error {
	_, err := c.send(ctx, legalEntityID, reservationPath+url.PathEscape(reservationID)+":commit", "", nil)
	if errors.Is(err, errStateConflict) {
		return ErrReleased
	}
	return err
}

func (c *Client) Release(ctx context.Context, legalEntityID, reservationID string) error {
	_, err := c.send(ctx, legalEntityID, reservationPath+url.PathEscape(reservationID)+":release", "", nil)
	if errors.Is(err, errStateConflict) {
		return ErrConsumed
	}
	return err
}

func optional(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}

type Fake struct {
	mu          sync.Mutex
	Err         error
	ReserveErr  error
	CommitErr   error
	ReleaseErr  error
	NoRoom      bool
	Net         *decimal.Decimal
	invoices    map[string]Basis
	byKey       map[string]*FakeReservation
	byID        map[string]*FakeReservation
	reserveCall int
}

type FakeReservation struct {
	ID, Key, InvoiceID, Status string
	Lines                      []Line
}

func NewFake() *Fake {
	return &Fake{invoices: map[string]Basis{}, byKey: map[string]*FakeReservation{}, byID: map[string]*FakeReservation{}}
}

func (f *Fake) Reservation(key string) (FakeReservation, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.byKey[key]
	if !ok {
		return FakeReservation{}, false
	}
	return *r, true
}

func (f *Fake) ReserveCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reserveCall
}

func (f *Fake) Reserve(_ context.Context, _, invoiceID, key, _ string, lines []Line) (Reservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserveCall++
	if f.ReserveErr != nil {
		return Reservation{}, f.ReserveErr
	}
	if r, ok := f.byKey[key]; ok {
		if r.Status == "RELEASED" {
			if f.NoRoom {
				return Reservation{}, ErrNoRoom
			}
			r.Status = "RESERVED"
		}
		return f.answer(r), nil
	}
	if f.NoRoom {
		return Reservation{}, ErrNoRoom
	}
	r := &FakeReservation{ID: ids.New(), Key: key, InvoiceID: invoiceID, Status: "RESERVED", Lines: lines}
	f.byKey[key], f.byID[r.ID] = r, r
	return f.answer(r), nil
}

func (f *Fake) answer(r *FakeReservation) Reservation {
	out := Reservation{ID: r.ID, Status: r.Status}
	if f.Net != nil {
		out.Net, out.Known = *f.Net, true
	}
	return out
}

func (f *Fake) Commit(_ context.Context, _, reservationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CommitErr != nil {
		return f.CommitErr
	}
	r, ok := f.byID[reservationID]
	switch {
	case !ok:
		return ErrNotFound
	case r.Status == "RELEASED":
		return ErrReleased
	}
	r.Status = "CONSUMED"
	return nil
}

func (f *Fake) Release(_ context.Context, _, reservationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ReleaseErr != nil {
		return f.ReleaseErr
	}
	r, ok := f.byID[reservationID]
	switch {
	case !ok:
		return ErrNotFound
	case r.Status == "CONSUMED":
		return ErrConsumed
	}
	r.Status = "RELEASED"
	return nil
}

func (f *Fake) Set(invoiceID string, b Basis) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invoices[invoiceID] = b
}

func (f *Fake) TaxBasis(_ context.Context, _, invoiceID string) (Basis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Basis{}, f.Err
	}
	b, ok := f.invoices[invoiceID]
	if !ok {
		return Basis{}, ErrNotFound
	}
	return b, nil
}
