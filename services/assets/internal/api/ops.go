package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

var (
	validAdjustment = map[string]bool{
		string(domain.AdjustmentAcquisition): true, string(domain.AdjustmentDepreciation): true,
		string(domain.AdjustmentRevaluation): true, string(domain.AdjustmentImpairment): true,
	}
	validAdditionType = map[string]bool{"Improvement": true, "Component": true, "Accessory": true}
)

// anyDuty allows a request when the principal holds at least one of the duties.
// Reads of insurance, capex and run data are open to AssetView (PRD 7.37.17:
// GET /assets/*) and to the duty that maintains the record.
type anyDuty []string

// Allow implements authz.Policy.
func (d anyDuty) Allow(_ context.Context, p tenant.Principal, _, _ string) (bool, error) {
	for _, duty := range d {
		if p.HasDuty(duty) {
			return true, nil
		}
	}
	return false, nil
}

func requireAny(duties ...string) func(http.Handler) http.Handler {
	return authz.Require(anyDuty(duties), strings.Join(duties, " or "), "")
}

type adjustRequest struct {
	BookID          string `json:"bookId"`
	Type            string `json:"type"`
	TransactionDate string `json:"transactionDate"`
	Amount          string `json:"amount"`
}

// adjustResponse is the stored adjustment plus warnings such as a capped
// impairment reversal (AST-0512).
type adjustResponse struct {
	store.AdjustResult
	Warnings []string `json:"warnings"`
}

type additionRequest struct {
	Type         string   `json:"type"`
	Description  string   `json:"description"`
	Quantity     *string  `json:"quantity"`
	SerialNumber *string  `json:"serialNumber"`
	AddedDate    string   `json:"addedDate"`
	Financial    bool     `json:"financial"`
	Cost         *string  `json:"cost"`
	Currency     string   `json:"currency"`
	BookIDs      []string `json:"bookIds"`
}

type runRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	BookCode      string `json:"bookCode"`
	Period        string `json:"period"`
}

// assetID reads a well-formed asset id from the path or answers 404.
func assetID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAssetNotFound, "no asset with id "+id))
		return "", false
	}
	return id, true
}

// adjust posts an acquisition or depreciation adjustment, a revaluation or an
// impairment (positive write-down, negative reversal) on one book (FR-37.9).
func (a *API) adjust(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req adjustRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.BookID) {
		fields["bookId"] = "must be a book UUID"
	}
	if !validAdjustment[req.Type] {
		fields["type"] = "must be AcquisitionAdj, DepreciationAdj, Revaluation or Impairment"
	}
	date, err := store.ParseDate(req.TransactionDate)
	if err != nil {
		fields["transactionDate"] = "must be a YYYY-MM-DD date"
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || amount.IsZero() {
		fields["amount"] = "must be a non-zero signed decimal"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	id, ok := assetID(w, r)
	if !ok {
		return
	}
	res, err := a.store.Adjust(r.Context(), id, store.AdjustmentInput{BookID: req.BookID, Type: domain.AdjustmentType(req.Type), Date: date, Amount: amount}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	out := adjustResponse{AdjustResult: res, Warnings: []string{}}
	if res.Limited {
		minor := money.MinorUnits(res.Book.Currency)
		out.Warnings = append(out.Warnings, domain.CodeImpairmentLimited+": impairment reversal limited to "+
			res.Transaction.AccumulatedAmount.Abs().StringFixed(minor))
	}
	a.adjusted.Add(1)
	httpx.JSON(w, http.StatusCreated, out)
}

// addAddition records an improvement, component or accessory (FR-37.14). A
// financial addition posts an AcquisitionAdj on the selected books, or every
// Open book, in the same request (AC-37.12).
func (a *API) addAddition(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req additionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !validAdditionType[req.Type] {
		fields["type"] = "must be Improvement, Component or Accessory"
	}
	if n := strings.TrimSpace(req.Description); n == "" || len(n) > 200 {
		fields["description"] = "must be 1 to 200 characters"
	}
	added, err := store.ParseDate(req.AddedDate)
	if err != nil {
		fields["addedDate"] = "must be a YYYY-MM-DD date"
	}
	quantity := decimal.NewFromInt(1)
	if req.Quantity != nil {
		q, err := decimal.NewFromString(*req.Quantity)
		if err != nil || !q.IsPositive() || q.Exponent() < -4 {
			fields["quantity"] = "must be a positive decimal with at most 4 places"
		} else {
			quantity = q
		}
	}
	var serial *string
	if req.SerialNumber != nil {
		if s := strings.TrimSpace(*req.SerialNumber); s == "" || len(s) > 64 {
			fields["serialNumber"] = "must be 1 to 64 characters, or omitted"
		} else {
			serial = &s
		}
	}
	cost := decimal.Zero
	if req.Financial {
		if !currency.MatchString(req.Currency) {
			fields["currency"] = "must be an ISO 4217 code for a financial addition"
		}
		c, err := decimal.NewFromString(deref(req.Cost))
		switch {
		case err != nil || !c.IsPositive():
			fields["cost"] = "must be a positive decimal for a financial addition"
		case currency.MatchString(req.Currency) && !c.Equal(c.Round(money.MinorUnits(req.Currency))):
			fields["cost"] = "has more decimals than the currency minor unit"
		default:
			cost = c
		}
	} else {
		if req.Cost != nil || req.Currency != "" || len(req.BookIDs) > 0 {
			fields["financial"] = "cost, currency and bookIds apply only to a financial addition"
		}
	}
	for _, b := range req.BookIDs {
		if !ids.Valid(b) {
			fields["bookIds"] = "every entry must be a book UUID"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	id, ok := assetID(w, r)
	if !ok {
		return
	}
	res, err := a.store.AddAddition(r.Context(), id, store.AdditionInput{Type: req.Type, Description: strings.TrimSpace(req.Description),
		Quantity: quantity, SerialNumber: serial, AddedDate: added, Financial: req.Financial, Cost: cost, Currency: req.Currency, BookIDs: req.BookIDs}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.added.Add(1)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) listAdditions(w http.ResponseWriter, r *http.Request) {
	id, ok := assetID(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListAdditions(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// proposeRun proposes depreciation for one book code of a legal entity and one
// period (FR-37.5). Proposing again refreshes a run that is not yet posted.
func (a *API) proposeRun(w http.ResponseWriter, r *http.Request) {
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !bookCode.MatchString(req.BookCode) {
		fields["bookCode"] = "must be upper-case letters, digits, hyphen or underscore"
	}
	if _, err := domain.PeriodStart(req.Period); err != nil {
		fields["period"] = "must be YYYY-MM for monthly books or YYYY for yearly books"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	run, err := a.store.ProposeRun(r.Context(), store.RunInput{LegalEntityID: req.LegalEntityID, BookCode: req.BookCode, Period: req.Period}, key)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	setETag(w, run.Version)
	httpx.JSON(w, http.StatusCreated, run)
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	fields := map[string]string{}
	limit, after := pageLimit(r, fields)
	legalEntity := r.URL.Query().Get("legalEntityId")
	if legalEntity != "" && !ids.Valid(legalEntity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := a.store.ListRuns(r.Context(), store.RunFilter{LegalEntityID: legalEntity, After: after, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func runID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "no depreciation run with id "+id))
		return "", false
	}
	return id, true
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	run, err := a.store.GetRun(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, run.Version)
	httpx.JSON(w, http.StatusOK, run)
}

// postRun posts the proposal the caller reviewed (If-Match with the run's
// ETag). Books that changed since the proposal become exceptions; the others post.
func (a *API) postRun(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	run, err := a.store.PostRun(r.Context(), id, version)
	if err != nil {
		writeDomain(w, r, err)
		return
	}
	a.runs.Add(1)
	setETag(w, run.Version)
	httpx.JSON(w, http.StatusOK, run)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
