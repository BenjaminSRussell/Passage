package api

import (
	"errors"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

const CodeGLUnavailable = "PRODUCTION_GL_UNAVAILABLE"

// WithGL books the WIP subledger in the general ledger through ledger and reconciles it against the ledger's balances.
func (a *API) WithGL(books store.GLBooks, ledger ledgerclient.Ledger) *API {
	a.gl, a.ledger = books, ledger
	return a
}

type glPostRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	OrderID       string `json:"orderId"`
}

type glPostView struct {
	Booked    []store.GLBooked `json:"booked"`
	Failed    []store.GLFailed `json:"failed"`
	Remaining int              `json:"remaining"`
}

func (a *API) glReady(w http.ResponseWriter, r *http.Request) bool {
	if a.gl == nil || a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

func (a *API) glEntity(w http.ResponseWriter, r *http.Request, p tenant.Principal, entity, ledgerID string, fields map[string]string) (string, bool) {
	if entity == "" {
		entity = p.LegalEntityID
	}
	if !ids.Valid(entity) || !tenant.InLegalEntityScope(p, entity) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if !ids.Valid(ledgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return "", false
	}
	return entity, true
}

func (a *API) glFail(w http.ResponseWriter, r *http.Request, op string, err error) {
	var le *ledgerclient.Error
	if errors.As(err, &le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeGLUnavailable, "the general ledger refused the read ("+le.Code+")"))
		return
	}
	a.fail(w, r, op, err)
}

func (a *API) postToGL(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !a.glReady(w, r) {
		return
	}
	var req glPostRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.OrderID != "" && !ids.Valid(req.OrderID) {
		fields["orderId"] = "must be a UUID"
	}
	entity, ok := a.glEntity(w, r, p, req.LegalEntityID, req.LedgerID, fields)
	if !ok {
		return
	}
	res, err := a.gl.PostToGL(r.Context(), a.ledger, store.GLPostRequest{LegalEntityID: entity, LedgerID: req.LedgerID, OrderID: req.OrderID})
	if err != nil {
		a.fail(w, r, "book production postings in the general ledger", err)
		return
	}
	a.glBooked.Add(int64(len(res.Booked)))
	httpx.JSON(w, http.StatusOK, glPostView(res))
}

type glAccountView struct {
	Account    string `json:"account"`
	Subledger  string `json:"subledger"`
	Unbooked   string `json:"unbooked"`
	GL         string `json:"gl"`
	Difference string `json:"difference"`
}

type glReconcileView struct {
	Currency   string          `json:"currency"`
	PeriodTo   string          `json:"periodTo,omitempty"`
	Reconciled bool            `json:"reconciled"`
	Accounts   []glAccountView `json:"accounts"`
}

func (a *API) wipReconciliation(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !a.glReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	if !currencyCode.MatchString(q.Get("currency")) {
		fields["currency"] = "must be a 3-letter currency code"
	}
	if period := q.Get("periodTo"); period != "" && !periodPattern.MatchString(period) {
		fields["periodTo"] = "must be a period as YYYY-MM"
	}
	entity, ok := a.glEntity(w, r, p, q.Get("legalEntityId"), q.Get("ledgerId"), fields)
	if !ok {
		return
	}
	rec, err := a.gl.ReconcileWIP(r.Context(), a.ledger, store.GLReconcileRequest{LegalEntityID: entity, LedgerID: q.Get("ledgerId"), Currency: q.Get("currency"), PeriodTo: q.Get("periodTo")})
	if err != nil {
		a.glFail(w, r, "reconcile WIP to the general ledger", err)
		return
	}
	v := glReconcileView{Currency: rec.Currency, PeriodTo: q.Get("periodTo"), Reconciled: rec.Reconciled, Accounts: make([]glAccountView, 0, len(rec.Accounts))}
	for _, x := range rec.Accounts {
		v.Accounts = append(v.Accounts, glAccountView{Account: x.Account, Subledger: fixed(x.Subledger, rec.Currency), Unbooked: fixed(x.Unbooked, rec.Currency),
			GL: fixed(x.GL, rec.Currency), Difference: fixed(x.Difference, rec.Currency)})
	}
	httpx.JSON(w, http.StatusOK, v)
}
