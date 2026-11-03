package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

const (
	DutyGLPost        = "ProjGlPost"
	codeGLUnavailable = "PROJ-GL-004"
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	periodCode   = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
)

// WithGL books project costs and estimates in the general ledger through ledger and reconciles them to its balances.
func (a *API) WithGL(gl store.GL, ledger ledgerclient.Ledger) *API {
	a.gl, a.ledger = gl, ledger
	return a
}

func (a *API) registerGL(r chi.Router) {
	r.With(authz.RequireDuty(DutyGLPost)).Post("/gl-postings", a.postGL)
	r.With(authz.RequireDuty(DutyInquire)).Get("/gl-reconciliation", a.glReconciliation)
}

func (a *API) glReady(w http.ResponseWriter, r *http.Request) bool {
	if a.gl == nil || a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeGLUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

func glEntity(w http.ResponseWriter, r *http.Request, p tenant.Principal, entity string) (string, bool) {
	if entity == "" {
		entity = p.LegalEntityID
	}
	if !ids.Valid(entity) || !tenant.InLegalEntityScope(p, entity) {
		httpx.Fail(w, r, validation(map[string]string{"legalEntityId": "must be a legal entity of the caller"}))
		return "", false
	}
	return entity, true
}

func glFields(ledgerID, currency, periodField, period string) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(ledgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(currency) {
		fields["currency"] = "must be a 3-letter currency code"
	}
	if period != "" && !periodCode.MatchString(period) {
		fields[periodField] = "must be a period as YYYY-MM"
	}
	return fields
}

type glPostRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	Currency      string `json:"currency"`
	Period        string `json:"period"`
	ProjectID     string `json:"projectId"`
}

func (a *API) postGL(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !a.glReady(w, r) {
		return
	}
	var req glPostRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	entity, ok := glEntity(w, r, p, req.LegalEntityID)
	if !ok {
		return
	}
	fields := glFields(req.LedgerID, req.Currency, "period", req.Period)
	if req.ProjectID != "" && !domain.ValidProjectID(req.ProjectID) {
		fields["projectId"] = "is not a project id"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	res, err := a.gl.PostGL(r.Context(), a.ledger, store.GLPostRequest{LegalEntityID: entity, LedgerID: req.LedgerID, Currency: req.Currency, Period: req.Period, ProjectID: req.ProjectID})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type glAccountOut struct {
	Account    string `json:"account"`
	Subledger  string `json:"subledger"`
	GL         string `json:"gl"`
	Difference string `json:"difference"`
}

type glReconciliationOut struct {
	Currency   string         `json:"currency"`
	Reconciled bool           `json:"reconciled"`
	Unposted   store.TxnList  `json:"unpostedTransactions"`
	Accounts   []glAccountOut `json:"accounts"`
}

func (a *API) glReconciliation(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !a.glReady(w, r) {
		return
	}
	q := r.URL.Query()
	entity, ok := glEntity(w, r, p, q.Get("legalEntityId"))
	if !ok {
		return
	}
	if fields := glFields(q.Get("ledgerId"), q.Get("currency"), "periodTo", q.Get("periodTo")); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	rec, err := a.gl.ReconcileGL(r.Context(), a.ledger, store.GLReconcileRequest{LegalEntityID: entity, LedgerID: q.Get("ledgerId"), Currency: q.Get("currency"), PeriodTo: q.Get("periodTo")})
	var le *ledgerclient.Error
	if errors.As(err, &le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, codeGLUnavailable, "the general ledger refused the read ("+le.Code+")"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	mu := money.MinorUnits(rec.Currency)
	out := glReconciliationOut{Currency: rec.Currency, Reconciled: rec.Reconciled, Unposted: rec.Unposted, Accounts: make([]glAccountOut, 0, len(rec.Accounts))}
	for _, acc := range rec.Accounts {
		out.Accounts = append(out.Accounts, glAccountOut{Account: acc.Account, Subledger: acc.Subledger.StringFixed(mu), GL: acc.GL.StringFixed(mu), Difference: acc.Difference.StringFixed(mu)})
	}
	httpx.JSON(w, http.StatusOK, out)
}
