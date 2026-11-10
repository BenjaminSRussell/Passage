package api

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const dutyCashDesk = "treasury.cashdesk.write"

// CashDesks is the persistence of cash desks and their journal (FR-36.16).
type CashDesks interface {
	CreateCashDesk(ctx context.Context, in store.NewCashDesk, key string) (store.CashDesk, error)
	ListCashDesks(ctx context.Context, legalEntityID string) ([]store.CashDesk, error)
	GetCashDesk(ctx context.Context, id string) (store.CashDesk, error)
	OpenCashDeskDay(ctx context.Context, deskID, date string) (store.CashDeskDay, error)
	AddCashDeskEntry(ctx context.Context, in store.NewCashDeskEntry, key string) (store.CashDeskEntry, error)
	ListCashDeskDays(ctx context.Context, deskID string) ([]store.CashDeskDay, error)
	ListCashDeskEntries(ctx context.Context, deskID, date string) ([]store.CashDeskEntry, error)
	CloseCashDeskDay(ctx context.Context, deskID, date string, counted decimal.Decimal, reason string) (store.CashDeskDay, error)
}

type deskCounters struct {
	desksCreated atomic.Int64
	deskEntries  atomic.Int64
	deskDays     atomic.Int64
}

// WithCashDesks mounts the cash desk routes.
func (a *API) WithCashDesks(c CashDesks) *API {
	a.desks = c
	return a
}

func (a *API) registerCashDesks(r chi.Router) {
	w := r.With(authz.RequireDuty(dutyCashDesk))
	v := r.With(authz.RequireDuty(dutyView))
	w.Post("/v1/treasury/cash-desks", a.createDesk)
	v.Get("/v1/treasury/cash-desks", a.listDesks)
	v.Get("/v1/treasury/cash-desks/{id}", a.getDesk)
	w.Post("/v1/treasury/cash-desks/{id}/days", a.openDeskDay)
	v.Get("/v1/treasury/cash-desks/{id}/days", a.listDeskDays)
	w.Post("/v1/treasury/cash-desks/{id}/days/{date}:close", a.closeDeskDay)
	v.Get("/v1/treasury/cash-desks/{id}/days/{date}/entries", a.listDeskEntries)
	w.Post("/v1/treasury/cash-desks/{id}/entries", a.addDeskEntry)
}

func (a *API) desksReady(w http.ResponseWriter, r *http.Request) bool {
	if !validTenant(w, r) {
		return false
	}
	if a.desks == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "TRS-0551", "cash desks are not enabled in this deployment"))
		return false
	}
	return true
}

func deskID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return pathID(w, r, domain.CodeDeskNotFound, "cash desk not found")
}

func deskDate(w http.ResponseWriter, r *http.Request) (string, bool) {
	date := chi.URLParam(r, "date")
	if _, err := domain.Day(date); err != nil {
		validationFailed(w, r, map[string]string{"date": "must be a YYYY-MM-DD date"})
		return "", false
	}
	return date, true
}

type createDeskRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	MainAccountID string `json:"mainAccountId"`
	MaxBalance    string `json:"maxBalance"`
}

func (a *API) createDesk(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createDeskRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !domain.ValidAccountCode(req.Code) {
		fields["code"] = "1 to 20 characters of A-Z, 0-9 or -"
	}
	if n := len(strings.TrimSpace(req.Name)); n < 1 || n > 120 {
		fields["name"] = "1 to 120 characters"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if !ids.Valid(req.MainAccountID) {
		fields["mainAccountId"] = "must be a UUID (the cash account in the general ledger)"
	}
	max, ok := parseAmount(req.MaxBalance, req.Currency)
	if !ok {
		fields["maxBalance"] = "must be a positive decimal string with at most the currency's minor-unit places"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	d, err := a.desks.CreateCashDesk(r.Context(), store.NewCashDesk{LegalEntityID: req.LegalEntityID, Code: req.Code, Name: strings.TrimSpace(req.Name),
		Currency: req.Currency, MainAccountID: req.MainAccountID, MaxBalance: max.String()}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.desksCreated.Add(1)
	setETag(w, d.Version)
	httpx.JSON(w, http.StatusCreated, d)
}

func (a *API) listDesks(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "required UUID query parameter"})
		return
	}
	items, err := a.desks.ListCashDesks(r.Context(), le)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getDesk(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	d, err := a.desks.GetCashDesk(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, d.Version)
	httpx.JSON(w, http.StatusOK, d)
}

type openDayRequest struct {
	BusinessDate string `json:"businessDate"`
}

func (a *API) openDeskDay(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	var req openDayRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if _, err := domain.Day(req.BusinessDate); err != nil {
		validationFailed(w, r, map[string]string{"businessDate": "must be a YYYY-MM-DD date"})
		return
	}
	day, err := a.desks.OpenCashDeskDay(r.Context(), id, req.BusinessDate)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.deskDays.Add(1)
	httpx.JSON(w, http.StatusCreated, day)
}

func (a *API) listDeskDays(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	items, err := a.desks.ListCashDeskDays(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type closeDayRequest struct {
	CountedBalance string `json:"countedBalance"`
	Reason         string `json:"reason"`
}

func (a *API) closeDeskDay(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	date, ok := deskDate(w, r)
	if !ok {
		return
	}
	var req closeDayRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	counted, err := decimal.NewFromString(strings.TrimSpace(req.CountedBalance))
	if err != nil || counted.IsNegative() || !counted.LessThan(maxAmount) {
		validationFailed(w, r, map[string]string{"countedBalance": "must be a decimal string, not negative"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) > 200 {
		validationFailed(w, r, map[string]string{"reason": "at most 200 characters"})
		return
	}
	// The count is checked against the desk currency's minor units by the store's numeric type; round-trip it here so
	// a count such as 10.005 is refused instead of silently rounded.
	desk, err := a.desks.GetCashDesk(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !counted.Equal(counted.Round(money.MinorUnits(desk.Currency))) {
		validationFailed(w, r, map[string]string{"countedBalance": "has more decimal places than the desk currency"})
		return
	}
	day, err := a.desks.CloseCashDeskDay(r.Context(), id, date, counted, reason)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.deskDays.Add(1)
	httpx.JSON(w, http.StatusOK, day)
}

func (a *API) listDeskEntries(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	date, ok := deskDate(w, r)
	if !ok {
		return
	}
	items, err := a.desks.ListCashDeskEntries(r.Context(), id, date)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type deskEntryRequest struct {
	Kind        string `json:"kind"`
	Amount      string `json:"amount"`
	Description string `json:"description"`
	Reference   string `json:"reference"`
}

func (a *API) addDeskEntry(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req deskEntryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	desk, err := a.desks.GetCashDesk(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Kind != domain.DeskReceipt && req.Kind != domain.DeskPayment {
		fields["kind"] = "must be receipt or payment"
	}
	amount, ok := parseAmount(req.Amount, desk.Currency)
	if !ok {
		fields["amount"] = "must be a positive decimal string with at most the currency's minor-unit places"
	}
	desc := strings.TrimSpace(req.Description)
	if n := len(desc); n < 1 || n > 200 {
		fields["description"] = "1 to 200 characters"
	}
	if len(req.Reference) > 100 {
		fields["reference"] = "at most 100 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	e, err := a.desks.AddCashDeskEntry(r.Context(), store.NewCashDeskEntry{DeskID: id, Kind: req.Kind, Amount: amount.String(), Description: desc, Reference: strings.TrimSpace(req.Reference)}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.deskEntries.Add(1)
	httpx.JSON(w, http.StatusCreated, e)
}
