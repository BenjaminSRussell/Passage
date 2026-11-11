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

const (
	dutyCashDesk        = "treasury.cashdesk.write"
	dutyCashDeskApprove = "treasury.cashdesk.approve"
)

// CashDesks is the persistence of cash desks and their journal (FR-36.16).
type CashDesks interface {
	CreateCashDesk(ctx context.Context, in store.NewCashDesk, key string) (store.CashDesk, error)
	ListCashDesks(ctx context.Context, legalEntityID string) ([]store.CashDesk, error)
	GetCashDesk(ctx context.Context, id string) (store.CashDesk, error)
	OpenCashDeskDay(ctx context.Context, deskID, date string) (store.CashDeskDay, error)
	AddCashDeskEntry(ctx context.Context, in store.NewCashDeskEntry, key string) (store.CashDeskEntry, error)
	ListCashDeskDays(ctx context.Context, deskID string) ([]store.CashDeskDay, error)
	ListCashDeskEntries(ctx context.Context, deskID, date string) ([]store.CashDeskEntry, error)
	CloseCashDeskDay(ctx context.Context, deskID, date string, in store.CloseDay) (store.CashDeskDay, error)
	ApproveCashDeskDay(ctx context.Context, deskID, date string, postDifference bool) (store.CashDeskDay, error)
	RejectCashDeskDay(ctx context.Context, deskID, date string) (store.CashDeskDay, error)
	GetCashDeskDay(ctx context.Context, deskID, date string) (store.CashDeskDay, error)
	UpdateCashDesk(ctx context.Context, id string, version int, in store.UpdateCashDesk) (store.CashDesk, error)
	SetCashDeskStatus(ctx context.Context, id string, version int, status string) (store.CashDesk, error)
	PendingDeskPostings(ctx context.Context, deskID, date string) (store.DeskPostings, error)
	MarkDeskEntryPosted(ctx context.Context, deskID, entryID, ledgerEntryID string) error
	MarkDeskDifferencePosted(ctx context.Context, deskID, date, ledgerEntryID string) error
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
	w.Post("/v1/treasury/cash-desks/{id}/days/{date}:post", a.postDeskDay)
	r.With(authz.RequireDuty(dutyCashDeskApprove)).Post("/v1/treasury/cash-desks/{id}/days/{date}:approve", a.approveDeskDay)
	r.With(authz.RequireDuty(dutyCashDeskApprove)).Post("/v1/treasury/cash-desks/{id}/days/{date}:reject", a.rejectDeskDay)
	w.Patch("/v1/treasury/cash-desks/{id}", a.updateDesk)
	w.Post("/v1/treasury/cash-desks/{id}:activate", a.setDeskStatus("active"))
	w.Post("/v1/treasury/cash-desks/{id}:deactivate", a.setDeskStatus("inactive"))
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
	DualControl   bool   `json:"dualControl"`
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
		Currency: req.Currency, MainAccountID: req.MainAccountID, MaxBalance: max.String(), DualControl: req.DualControl}, key)
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
	// Denominations is the count by note and coin value. Without countedBalance the total is the count; with it, the two
	// must agree (TRS-0556).
	Denominations []domain.Denomination `json:"denominations"`
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
	desk, err := a.desks.GetCashDesk(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	places := int32(money.MinorUnits(desk.Currency))
	var counted decimal.Decimal
	if len(req.Denominations) > 0 {
		total, err := domain.DenominationTotal(req.Denominations, places)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		counted = total
		if raw := strings.TrimSpace(req.CountedBalance); raw != "" {
			given, err := decimal.NewFromString(raw)
			if err != nil || !given.Equal(total) {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeDeskCountMismatch, "the denominations add up to "+total.String()+", which differs from the counted balance"))
				return
			}
		}
	} else {
		var err error
		counted, err = decimal.NewFromString(strings.TrimSpace(req.CountedBalance))
		if err != nil {
			validationFailed(w, r, map[string]string{"countedBalance": "must be a decimal string, or give denominations"})
			return
		}
	}
	if counted.IsNegative() || !counted.LessThan(maxAmount) {
		validationFailed(w, r, map[string]string{"countedBalance": "must be a decimal string, not negative"})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if len(reason) > 200 {
		validationFailed(w, r, map[string]string{"reason": "at most 200 characters"})
		return
	}
	// A count such as 10.005 is refused instead of silently rounded by the numeric column.
	if !counted.Equal(counted.Round(places)) {
		validationFailed(w, r, map[string]string{"countedBalance": "has more decimal places than the desk currency"})
		return
	}
	_, posts, mapped := a.deskLedger(desk)
	if posts && !mapped {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNoProfile, "no GL posting profile maps this cash desk's cash account"))
		return
	}
	day, err := a.desks.CloseCashDeskDay(r.Context(), id, date, store.CloseDay{Counted: counted, Reason: reason, Denominations: req.Denominations, PostDifference: posts})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.deskDays.Add(1)
	a.finishDeskDay(w, r, id, date, day)
}

// finishDeskDay sends the day after a close or approval, posting a pending count difference to the ledger first.
func (a *API) finishDeskDay(w http.ResponseWriter, r *http.Request, id, date string, day store.CashDeskDay) {
	if day.DifferencePosting == "pending" {
		if err := a.flushDeskPostings(r.Context(), id, date); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		var err error
		if day, err = a.desks.GetCashDeskDay(r.Context(), id, date); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	httpx.JSON(w, http.StatusOK, day)
}

func (a *API) approveDeskDay(w http.ResponseWriter, r *http.Request) {
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
	desk, err := a.desks.GetCashDesk(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	_, posts, _ := a.deskLedger(desk)
	day, err := a.desks.ApproveCashDeskDay(r.Context(), id, date, posts)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.deskDays.Add(1)
	a.finishDeskDay(w, r, id, date, day)
}

func (a *API) rejectDeskDay(w http.ResponseWriter, r *http.Request) {
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
	day, err := a.desks.RejectCashDeskDay(r.Context(), id, date)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, day)
}

// postDeskDay posts what a day still owes the ledger: lines saved while the ledger was down or refused, and the count
// difference. It is safe to repeat.
func (a *API) postDeskDay(w http.ResponseWriter, r *http.Request) {
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
	if err := a.flushDeskPostings(r.Context(), id, date); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	day, err := a.desks.GetCashDeskDay(r.Context(), id, date)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, day)
}

type updateDeskRequest struct {
	Name          *string `json:"name"`
	MaxBalance    *string `json:"maxBalance"`
	MainAccountID *string `json:"mainAccountId"`
	DualControl   *bool   `json:"dualControl"`
}

// updateDesk changes a desk's name, maximum balance, cash account or dual control. It needs If-Match.
func (a *API) updateDesk(w http.ResponseWriter, r *http.Request) {
	if !a.desksReady(w, r) {
		return
	}
	id, ok := deskID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req updateDeskRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	desk, err := a.desks.GetCashDesk(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.UpdateCashDesk{DualControl: req.DualControl}
	fields := map[string]string{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if n := len(name); n < 1 || n > 120 {
			fields["name"] = "1 to 120 characters"
		}
		in.Name = &name
	}
	if req.MaxBalance != nil {
		max, ok := parseAmount(*req.MaxBalance, desk.Currency)
		if !ok {
			fields["maxBalance"] = "must be a positive decimal string with at most the currency's minor-unit places"
		}
		m := max.String()
		in.MaxBalance = &m
	}
	if req.MainAccountID != nil {
		if !ids.Valid(*req.MainAccountID) {
			fields["mainAccountId"] = "must be a UUID (the cash account in the general ledger)"
		}
		in.MainAccountID = req.MainAccountID
	}
	if in.Name == nil && in.MaxBalance == nil && in.MainAccountID == nil && in.DualControl == nil {
		fields["body"] = "name at least one of name, maxBalance, mainAccountId, dualControl"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	d, err := a.desks.UpdateCashDesk(r.Context(), id, version, in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, d.Version)
	httpx.JSON(w, http.StatusOK, d)
}

func (a *API) setDeskStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.desksReady(w, r) {
			return
		}
		id, ok := deskID(w, r)
		if !ok {
			return
		}
		version, ok := ifMatchVersion(w, r)
		if !ok {
			return
		}
		d, err := a.desks.SetCashDeskStatus(r.Context(), id, version, status)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		setETag(w, d.Version)
		httpx.JSON(w, http.StatusOK, d)
	}
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
	// CounterAccount is the ledger account on the other side of the cash. A bank transfer names BankAccountID instead:
	// a payment is cash paid into the bank, a receipt is cash drawn from it.
	CounterAccount string `json:"counterAccount"`
	BankAccountID  string `json:"bankAccountId"`
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
	dp, posts, mapped := a.deskLedger(desk)
	counter := strings.TrimSpace(req.CounterAccount)
	switch {
	case counter != "" && req.BankAccountID != "":
		fields["bankAccountId"] = "give a counter account or a bank account, not both"
	case counter != "" && !accountText.MatchString(counter):
		fields["counterAccount"] = "1 to 20 characters of letters, digits, . _ or -"
	case req.BankAccountID != "" && !ids.Valid(req.BankAccountID):
		fields["bankAccountId"] = "must be a UUID"
	case posts && counter == "" && req.BankAccountID == "":
		fields["counterAccount"] = "required: the ledger account on the other side of the cash (or name a bank account for a transfer)"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if posts && !mapped {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNoProfile, "no GL posting profile maps this cash desk's cash account"))
		return
	}
	if req.BankAccountID != "" {
		bank, err := a.store.GetBankAccount(r.Context(), req.BankAccountID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if bank.LegalEntityID != desk.LegalEntityID || bank.Currency != desk.Currency || bank.Status != "active" {
			validationFailed(w, r, map[string]string{"bankAccountId": "must be an active bank account of the same legal entity and currency as the desk"})
			return
		}
		if posts && dp.profile.BankAccounts[bank.ID] == "" {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeLedgerNoProfile, "the posting profile has no ledger account for this bank account"))
			return
		}
	}
	e, err := a.desks.AddCashDeskEntry(r.Context(), store.NewCashDeskEntry{DeskID: id, Kind: req.Kind, Amount: amount.String(), Description: desc,
		Reference: strings.TrimSpace(req.Reference), CounterAccount: counter, BankAccountID: req.BankAccountID, PostPending: posts}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.deskEntries.Add(1)
	if e.PostingStatus == "pending" {
		if err := a.flushDeskPostings(r.Context(), id, e.BusinessDate); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		entries, err := a.desks.ListCashDeskEntries(r.Context(), id, e.BusinessDate)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		for _, posted := range entries {
			if posted.ID == e.ID {
				e = posted
			}
		}
	}
	httpx.JSON(w, http.StatusCreated, e)
}
