package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

const CodeGLUnavailable = "AST-0602"

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	accountPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)
)

// WithGL books asset transactions and depreciation runs in the general ledger through ledger and reconciles the register to its balances.
func (a *API) WithGL(books store.GLBooks, ledger ledgerclient.Ledger) *API {
	a.gl, a.ledger = books, ledger
	return a
}

func (a *API) glReady(w http.ResponseWriter, r *http.Request) bool {
	if a.gl == nil || a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

type glPostRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
	BookCode      string `json:"bookCode"`
	Currency      string `json:"currency"`
}

func glFields(legalEntityID, ledgerID, book, currency string) map[string]string {
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": legalEntityID, "ledgerId": ledgerID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !bookCode.MatchString(book) {
		fields["bookCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	if !currencyPattern.MatchString(currency) {
		fields["currency"] = "must be a 3-letter currency code"
	}
	return fields
}

func (a *API) postToGL(w http.ResponseWriter, r *http.Request) {
	if !a.glReady(w, r) {
		return
	}
	var req glPostRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := glFields(req.LegalEntityID, req.LedgerID, req.BookCode, req.Currency); len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	res, err := a.gl.PostToGL(r.Context(), a.ledger, store.GLPostRequest{LegalEntityID: req.LegalEntityID, LedgerID: req.LedgerID, BookCode: req.BookCode, Currency: req.Currency})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.glBooked.Add(int64(len(res.Booked)))
	httpx.JSON(w, http.StatusOK, res)
}

type glAccountView struct {
	Account    string `json:"account"`
	Subledger  string `json:"subledger"`
	GL         string `json:"gl"`
	Difference string `json:"difference"`
}

type glReconciliationView struct {
	Currency   string          `json:"currency"`
	BookCode   string          `json:"bookCode"`
	Reconciled bool            `json:"reconciled"`
	Unposted   int             `json:"unposted"`
	Accounts   []glAccountView `json:"accounts"`
}

func (a *API) glReconciliation(w http.ResponseWriter, r *http.Request) {
	if !a.glReady(w, r) {
		return
	}
	q := r.URL.Query()
	if fields := glFields(q.Get("legalEntityId"), q.Get("ledgerId"), q.Get("bookCode"), q.Get("currency")); len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, q.Get("legalEntityId")) {
		return
	}
	rec, err := a.gl.ReconcileGL(r.Context(), a.ledger, store.GLReconcileRequest{LegalEntityID: q.Get("legalEntityId"), LedgerID: q.Get("ledgerId"), BookCode: q.Get("bookCode"), Currency: q.Get("currency")})
	var le *ledgerclient.Error
	if errors.As(err, &le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeGLUnavailable, "the general ledger refused the read ("+le.Code+")"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	mu := money.MinorUnits(rec.Currency)
	v := glReconciliationView{Currency: rec.Currency, BookCode: rec.BookCode, Reconciled: rec.Reconciled, Unposted: rec.Unposted, Accounts: make([]glAccountView, 0, len(rec.Accounts))}
	for _, x := range rec.Accounts {
		v.Accounts = append(v.Accounts, glAccountView{Account: x.Account, Subledger: x.Subledger.StringFixed(mu), GL: x.GL.StringFixed(mu), Difference: x.Difference.StringFixed(mu)})
	}
	httpx.JSON(w, http.StatusOK, v)
}

type profileRequest struct {
	LegalEntityID       string `json:"legalEntityId"`
	ClassCode           string `json:"classCode"`
	Cost                string `json:"costAccount"`
	Accumulated         string `json:"accumulatedDepreciationAccount"`
	DepreciationExpense string `json:"depreciationExpenseAccount"`
	Impairment          string `json:"impairmentAccount"`
	Revaluation         string `json:"revaluationAccount"`
	AcquisitionClearing string `json:"acquisitionClearingAccount"`
	Proceeds            string `json:"proceedsAccount"`
	Gain                string `json:"gainAccount"`
	Loss                string `json:"lossAccount"`
	TransferClearing    string `json:"transferClearingAccount"`
}

func (a *API) listPostingProfiles(w http.ResponseWriter, r *http.Request) {
	if a.gl == nil {
		a.glReady(w, r)
		return
	}
	entity := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(entity) {
		writeValidation(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !inScope(w, r, entity) {
		return
	}
	out, err := a.gl.ListPostingProfiles(r.Context(), entity)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out, "placeholder": store.PlaceholderProfile})
}

func (a *API) savePostingProfile(w http.ResponseWriter, r *http.Request) {
	if a.gl == nil {
		a.glReady(w, r)
		return
	}
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.ClassCode == "" {
		req.ClassCode = "ALL"
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !bookCode.MatchString(req.ClassCode) {
		fields["classCode"] = "must be ALL or an asset class code"
	}
	for name, v := range map[string]string{"costAccount": req.Cost, "accumulatedDepreciationAccount": req.Accumulated, "depreciationExpenseAccount": req.DepreciationExpense,
		"impairmentAccount": req.Impairment, "revaluationAccount": req.Revaluation, "acquisitionClearingAccount": req.AcquisitionClearing, "proceedsAccount": req.Proceeds,
		"gainAccount": req.Gain, "lossAccount": req.Loss, "transferClearingAccount": req.TransferClearing} {
		if !accountPattern.MatchString(strings.TrimSpace(v)) {
			fields[name] = "must be an account number of at most 40 characters"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	version := 0
	if r.Header.Get("If-Match") != "" {
		var ok bool
		if version, ok = ifMatch(w, r); !ok {
			return
		}
	}
	p, _ := tenant.FromContext(r.Context())
	saved, created, err := a.gl.SavePostingProfile(r.Context(), store.PostingProfile{LegalEntityID: req.LegalEntityID, ClassCode: req.ClassCode, Cost: req.Cost, Accumulated: req.Accumulated,
		DepreciationExpense: req.DepreciationExpense, Impairment: req.Impairment, Revaluation: req.Revaluation, AcquisitionClearing: req.AcquisitionClearing, Proceeds: req.Proceeds,
		Gain: req.Gain, Loss: req.Loss, TransferClearing: req.TransferClearing}, version, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, saved.Version)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}
