package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	dutyOpenItemRead      = "open-item.read"
	dutyProposalWrite     = "payment-proposal.write"
	dutyProposalRead      = "payment-proposal.read"
	dutyProposalApprove   = "payment-proposal.approve"
	pathOpenItems         = "/v1/payables/open-items"
	pathProposals         = "/v1/payables/payment-proposals"
	maxVendorMethods      = 500
	maxRunIDLength        = 64
	defaultPageSize       = 50
	maxPageSize           = 200
	codeNotFound          = "COM-0404"
	codeValidationFailed  = "COM-0001"
	codeRatesUnconfigured = "COM-0503"
	currencyMessage       = "must be a 3-letter upper-case ISO 4217 code"
)

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	openItemStatus  = []string{"OPEN", "PARTIALLY_SETTLED", "SETTLED", "REVERSED"}
)

type PayablesStore interface {
	List(ctx context.Context, f store.Filter, limit int) ([]store.OpenItem, error)
	Get(ctx context.Context, id string) (store.OpenItem, error)
	Candidates(ctx context.Context, legalEntityID, dueBy string) ([]domain.ProposalCandidate, error)
	FindProposal(ctx context.Context, legalEntityID, runDate, runID string) (store.Proposal, error)
	CreateProposal(ctx context.Context, in store.NewProposal) (store.Proposal, bool, error)
	GetProposal(ctx context.Context, id string) (store.Proposal, error)
	ApproveProposal(ctx context.Context, id, approver, today string) (store.Proposal, error)
	CancelProposal(ctx context.Context, id, user string) (store.Proposal, error)
}

type RateSource interface {
	Rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error)
}

type proposalDeps struct {
	store         PayablesStore
	rates         RateSource
	created       atomic.Int64
	replayed      atomic.Int64
	approved      atomic.Int64
	cancelled     atomic.Int64
	selfApprovals atomic.Int64
	rateFaults    atomic.Int64
}

func (a *API) WithProposals(s PayablesStore, rates RateSource) *API {
	a.proposals = &proposalDeps{store: s, rates: rates}
	return a
}

func (a *API) registerProposals(r chi.Router) {
	if a.proposals == nil {
		return
	}
	r.With(authz.RequireDuty(dutyOpenItemRead)).Get(pathOpenItems, a.listOpenItems)
	r.With(authz.RequireDuty(dutyOpenItemRead)).Get(pathOpenItems+"/{id}", a.getOpenItem)
	r.With(authz.RequireDuty(dutyProposalWrite)).Post(pathProposals, a.createProposal)
	r.With(authz.RequireDuty(dutyProposalRead)).Get(pathProposals+"/{id}", a.getProposal)
	r.With(authz.RequireDuty(dutyProposalApprove)).Post(pathProposals+"/{id}:approve", a.approveProposal)
	r.With(authz.RequireDuty(dutyProposalWrite)).Post(pathProposals+"/{id}:cancel", a.cancelProposal)
}

func (a *API) problem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	a.countClientError(r)
	fail(w, r, httpx.NewProblem(status, code, detail))
}

func (a *API) invalid(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	a.countClientError(r)
	p := httpx.NewProblem(http.StatusBadRequest, codeValidationFailed, "request failed validation")
	p.Errors = fields
	fail(w, r, p)
}

func (a *API) resolveEntity(w http.ResponseWriter, r *http.Request, requested string) (string, bool) {
	p, _ := tenant.FromContext(r.Context())
	if requested != "" && !tenant.InLegalEntityScope(p, requested) {
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's legal-entity scope")
		return "", false
	}
	if requested == "" {
		requested = p.LegalEntityID
	}
	if !ids.Valid(requested) {
		a.invalid(w, r, map[string]string{"legalEntityId": "is required and must be a UUID"})
		return "", false
	}
	return requested, true
}

func parseDay(fields map[string]string, name, raw string, required bool) time.Time {
	if raw == "" {
		if required {
			fields[name] = "is required"
		}
		return time.Time{}
	}
	d, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		fields[name] = "must be a date as YYYY-MM-DD"
	}
	return d
}

type valuation struct {
	Currency string          `json:"currency"`
	Amount   decimal.Decimal `json:"amount"`
	Rate     decimal.Decimal `json:"rate"`
	RateDate string          `json:"rateDate"`
}

type openItemBody struct {
	store.OpenItem
	Valuation *valuation `json:"valuation,omitempty"`
}

type openItemPage struct {
	Items      []openItemBody `json:"items"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

func encodeCursor(o store.OpenItem) string {
	return hex.EncodeToString([]byte(o.DueDate + "|" + o.ID))
}

func decodeCursor(raw string) (due, id string, ok bool) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != len(time.DateOnly)+1+36 || b[len(time.DateOnly)] != '|' {
		return "", "", false
	}
	due, id = string(b[:len(time.DateOnly)]), string(b[len(time.DateOnly)+1:])
	_, err = time.Parse(time.DateOnly, due)
	return due, id, err == nil && ids.Valid(id)
}

func (a *API) listOpenItems(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le, ok := a.resolveEntity(w, r, q.Get("legal_entity"))
	if !ok {
		return
	}
	fields := map[string]string{}
	f := store.Filter{LegalEntityID: le, VendorID: q.Get("vendor"), DueFrom: q.Get("due_from"), DueOnOrBy: q.Get("due_to"), Currency: q.Get("currency"), Status: q.Get("status")}
	if f.VendorID != "" && !ids.Valid(f.VendorID) {
		fields["vendor"] = "must be a UUID"
	}
	from, to := parseDay(fields, "due_from", f.DueFrom, false), parseDay(fields, "due_to", f.DueOnOrBy, false)
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		fields["due_from"] = "must not be after due_to"
	}
	if f.Currency != "" && !currencyPattern.MatchString(f.Currency) {
		fields["currency"] = currencyMessage
	}
	if f.Status != "" && !slices.Contains(openItemStatus, f.Status) {
		fields["status"] = "must be one of OPEN, PARTIALLY_SETTLED, SETTLED, REVERSED"
	}
	valueIn := q.Get("value_in")
	if valueIn != "" && !currencyPattern.MatchString(valueIn) {
		fields["value_in"] = currencyMessage
	}
	limit := defaultPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = fmt.Sprintf("must be an integer from 1 to %d", maxPageSize)
		}
		limit = n
	}
	if raw := q.Get("cursor"); raw != "" {
		if f.AfterDue, f.AfterID, ok = decodeCursor(raw); !ok {
			fields["cursor"] = "must be the nextCursor of the previous page"
		}
	}
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	items, err := a.proposals.store.List(r.Context(), f, limit+1)
	if err != nil {
		a.fault(w, r, "open item list failed", err)
		return
	}
	page := openItemPage{Items: make([]openItemBody, 0, min(len(items), limit))}
	if len(items) > limit {
		items = items[:limit]
		page.NextCursor = encodeCursor(items[limit-1])
	}
	valued, ok := a.valueItems(w, r, le, items, valueIn)
	if !ok {
		return
	}
	for i, it := range items {
		page.Items = append(page.Items, openItemBody{OpenItem: it, Valuation: valued[i]})
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getOpenItem(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	valueIn := r.URL.Query().Get("value_in")
	switch {
	case !ids.Valid(id):
		a.invalid(w, r, map[string]string{"id": "must be a UUID"})
		return
	case valueIn != "" && !currencyPattern.MatchString(valueIn):
		a.invalid(w, r, map[string]string{"value_in": currencyMessage})
		return
	}
	item, err := a.proposals.store.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "open item not found")
		return
	}
	if err != nil {
		a.fault(w, r, "open item get failed", err)
		return
	}
	valued, ok := a.valueItems(w, r, item.LegalEntityID, []store.OpenItem{item}, valueIn)
	if !ok {
		return
	}
	httpx.JSON(w, http.StatusOK, openItemBody{OpenItem: item, Valuation: valued[0]})
}

func (a *API) today() time.Time { return a.now().UTC().Truncate(24 * time.Hour) }

func (a *API) valueItems(w http.ResponseWriter, r *http.Request, legalEntityID string, items []store.OpenItem, valueIn string) ([]*valuation, bool) {
	out := make([]*valuation, len(items))
	if valueIn == "" || len(items) == 0 {
		return out, true
	}
	date := a.today()
	currencies := make([]string, len(items))
	for i, it := range items {
		currencies[i] = it.Currency
	}
	rates, ok := a.ratesFor(w, r, legalEntityID, currencies, valueIn, date)
	if !ok {
		return nil, false
	}
	for i, it := range items {
		out[i] = &valuation{Currency: valueIn, Amount: it.OpenAmount.Mul(rates[it.Currency]).Round(money.MinorUnits(valueIn)), Rate: rates[it.Currency], RateDate: date.Format(time.DateOnly)}
	}
	return out, true
}

func (a *API) ratesFor(w http.ResponseWriter, r *http.Request, legalEntityID string, currencies []string, to string, date time.Time) (map[string]decimal.Decimal, bool) {
	rates := map[string]decimal.Decimal{to: decimal.NewFromInt(1)}
	for _, c := range currencies {
		if _, done := rates[c]; done {
			continue
		}
		if a.proposals.rates == nil {
			a.problem(w, r, http.StatusServiceUnavailable, codeRatesUnconfigured, "exchange rate source is not configured")
			return nil, false
		}
		rate, err := a.proposals.rates.Rate(r.Context(), legalEntityID, c, to, date)
		if err != nil {
			a.rateFailure(w, r, c, to, err)
			return nil, false
		}
		rates[c] = rate
	}
	return rates, true
}

func (a *API) rateFailure(w http.ResponseWriter, r *http.Request, from, to string, err error) {
	var se *fx.StatusError
	switch {
	case errors.Is(err, fx.ErrRateUnavailable):
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeCurrency, "no exchange rate from "+from+" to "+to)
	case errors.As(err, &se) && se.Status == http.StatusForbidden:
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "caller lacks the duty to read exchange rates")
	default:
		a.proposals.rateFaults.Add(1)
		slog.Error("exchange rate unavailable", "err", err, "from", from, "to", to, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, httpx.NewProblem(http.StatusBadGateway, "COM-0502", "exchange rate unavailable"))
	}
}

type proposalRequest struct {
	LegalEntityID        string                        `json:"legalEntityId"`
	LegalEntityCurrency  string                        `json:"legalEntityCurrency"`
	RunDate              string                        `json:"runDate"`
	RunID                string                        `json:"runId"`
	DueBy                string                        `json:"dueBy"`
	PaymentMethod        string                        `json:"paymentMethod"`
	VendorPaymentMethods map[string]string             `json:"vendorPaymentMethods"`
	Creditors            map[string]domain.CreditorDTO `json:"creditors,omitempty"`
}

func (p proposalRequest) validate() (runDate time.Time, fields map[string]string) {
	fields = map[string]string{}
	runDate = parseDay(fields, "runDate", p.RunDate, true)
	if dueBy := parseDay(fields, "dueBy", p.DueBy, false); !dueBy.IsZero() && !runDate.IsZero() && dueBy.Before(runDate) {
		fields["dueBy"] = "must not be before runDate"
	}
	if !currencyPattern.MatchString(p.LegalEntityCurrency) {
		fields["legalEntityCurrency"] = "is required: " + currencyMessage
	}
	if p.RunID == "" || len(p.RunID) > maxRunIDLength {
		fields["runId"] = fmt.Sprintf("is required, at most %d characters", maxRunIDLength)
	}
	if !domain.ValidPaymentMethod(p.PaymentMethod) {
		fields["paymentMethod"] = "must be TRANSFER, ACH, CHECK, CARD or WIRE"
	}
	if len(p.VendorPaymentMethods) > maxVendorMethods {
		fields["vendorPaymentMethods"] = fmt.Sprintf("at most %d entries", maxVendorMethods)
	}
	for vendor, method := range p.VendorPaymentMethods {
		if !ids.Valid(vendor) || !domain.ValidPaymentMethod(method) {
			fields["vendorPaymentMethods"] = "keys must be vendor UUIDs and values payment methods"
		}
	}
	maps.Copy(fields, domain.ValidateCreditors(p.Creditors))
	return runDate, fields
}

func (p proposalRequest) hash() string {
	if p.VendorPaymentMethods == nil {
		p.VendorPaymentMethods = map[string]string{}
	}
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (a *API) createProposal(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	if caller.UserID == "" {
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "a proposal needs an identified creator")
		return
	}
	var req proposalRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	runDate, fields := req.validate()
	le, ok := a.resolveEntity(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	if len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	req.LegalEntityID = le
	if req.DueBy == "" {
		req.DueBy = req.RunDate
	}
	ctx := r.Context()
	existing, err := a.proposals.store.FindProposal(ctx, le, req.RunDate, req.RunID)
	if err == nil {
		a.replay(w, r, existing, req)
		return
	}
	if !errors.Is(err, store.ErrProposalNotFound) {
		a.fault(w, r, "payment proposal lookup failed", err)
		return
	}
	if runDate.Before(a.today()) {
		a.invalid(w, r, map[string]string{"runDate": "must not be earlier than today (UTC)"})
		return
	}
	candidates, err := a.proposals.store.Candidates(ctx, le, req.DueBy)
	if errors.Is(err, store.ErrTooManyItems) {
		a.invalid(w, r, map[string]string{"dueBy": fmt.Sprintf("more than %d open items are due; lower dueBy", store.MaxProposalItems)})
		return
	}
	if err != nil {
		a.fault(w, r, "payment proposal candidates failed", err)
		return
	}
	rateDate := runDate
	if today := a.today(); runDate.After(today) {
		rateDate = today
	}
	currencies := make([]string, len(candidates))
	for i, c := range candidates {
		currencies[i] = c.Currency
	}
	rates, ok := a.ratesFor(w, r, le, currencies, req.LegalEntityCurrency, rateDate)
	if !ok {
		return
	}
	groups, err := domain.BuildProposal(domain.ProposalInput{
		RunDate: runDate, LegalEntityCurrency: req.LegalEntityCurrency, DefaultMethod: req.PaymentMethod,
		VendorMethods: req.VendorPaymentMethods, Rates: rates, Candidates: candidates,
	})
	if err != nil {
		a.reject(w, r, err)
		return
	}
	if len(groups) == 0 {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeNothingDue, "no open items are due for payment on or before "+req.DueBy)
		return
	}
	proposal, created, err := a.proposals.store.CreateProposal(ctx, store.NewProposal{
		LegalEntityID: le, RunDate: req.RunDate, RunID: req.RunID, DueBy: req.DueBy, RateDate: rateDate.Format(time.DateOnly),
		LegalEntityCurrency: req.LegalEntityCurrency, PaymentMethod: req.PaymentMethod, RequestHash: req.hash(), CreatedBy: caller.UserID, Groups: groups, Payees: req.Creditors,
	})
	if errors.Is(err, store.ErrItemsChanged) {
		a.problem(w, r, http.StatusConflict, domain.CodeProposalConflict, err.Error()+"; retry the request")
		return
	}
	if errors.Is(err, store.ErrNoFieldCrypt) {
		a.problem(w, r, http.StatusServiceUnavailable, codeRatesUnconfigured, err.Error())
		return
	}
	if err != nil {
		a.fault(w, r, "payment proposal create failed", err)
		return
	}
	if !created {
		a.replay(w, r, proposal, req)
		return
	}
	a.proposals.created.Add(1)
	w.Header().Set("Location", pathProposals+"/"+proposal.ID)
	httpx.JSON(w, http.StatusCreated, proposal)
}

func (a *API) replay(w http.ResponseWriter, r *http.Request, existing store.Proposal, req proposalRequest) {
	if existing.RequestHash != req.hash() {
		a.problem(w, r, http.StatusConflict, domain.CodeProposalConflict, "run id "+req.RunID+" was already used for this legal entity and run date with a different request")
		return
	}
	a.proposals.replayed.Add(1)
	w.Header().Set("Idempotent-Replayed", "true")
	httpx.JSON(w, http.StatusOK, existing)
}

func (a *API) getProposal(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		a.invalid(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	proposal, err := a.proposals.store.GetProposal(r.Context(), id)
	if errors.Is(err, store.ErrProposalNotFound) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment proposal not found")
		return
	}
	if err != nil {
		a.fault(w, r, "payment proposal get failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, proposal)
}

func (a *API) approveProposal(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	switch {
	case caller.UserID == "":
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "an approval needs an identified approver")
		return
	case !ids.Valid(id):
		a.invalid(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	proposal, err := a.proposals.store.ApproveProposal(r.Context(), id, caller.UserID, a.today().Format(time.DateOnly))
	var changed *store.LinesChangedError
	switch {
	case errors.As(err, &changed):
		a.problem(w, r, http.StatusConflict, domain.CodeProposalConflict, changed.Error())
	case errors.Is(err, store.ErrProposalNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment proposal not found")
	case errors.Is(err, store.ErrNotDraft):
		a.problem(w, r, http.StatusConflict, domain.CodeProposalState, "payment proposal is not a draft")
	case errors.Is(err, store.ErrRunDateElapsed):
		a.problem(w, r, http.StatusConflict, domain.CodeRunDatePassed, "run date has passed; create a new proposal")
	case errors.Is(err, store.ErrSelfApproval):
		a.proposals.selfApprovals.Add(1)
		slog.Warn("payment proposal approval refused: creator cannot approve", "proposalId", id, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		a.problem(w, r, http.StatusForbidden, domain.CodeSegregation, "a proposal must be approved by a different user than its creator")
	case err != nil:
		a.fault(w, r, "payment proposal approval failed", err)
	default:
		a.proposals.approved.Add(1)
		httpx.JSON(w, http.StatusOK, proposal)
	}
}

func (a *API) cancelProposal(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	id := chi.URLParam(r, "id")
	switch {
	case caller.UserID == "":
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "a cancellation needs an identified user")
		return
	case !ids.Valid(id):
		a.invalid(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	proposal, err := a.proposals.store.CancelProposal(r.Context(), id, caller.UserID)
	switch {
	case errors.Is(err, store.ErrProposalNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment proposal not found")
	case errors.Is(err, store.ErrNotDraft):
		a.problem(w, r, http.StatusConflict, domain.CodeProposalState, "payment proposal is not a draft")
	case err != nil:
		a.fault(w, r, "payment proposal cancel failed", err)
	default:
		a.proposals.cancelled.Add(1)
		httpx.JSON(w, http.StatusOK, proposal)
	}
}

func (a *API) writeProposalMetrics(w http.ResponseWriter) {
	if a.proposals == nil {
		return
	}
	fmt.Fprintf(w, "# HELP payables_payment_proposals_total Payment proposal commands by outcome.\n# TYPE payables_payment_proposals_total counter\npayables_payment_proposals_total{outcome=\"created\"} %d\npayables_payment_proposals_total{outcome=\"replayed\"} %d\npayables_payment_proposals_total{outcome=\"approved\"} %d\npayables_payment_proposals_total{outcome=\"self_approval_refused\"} %d\npayables_payment_proposals_total{outcome=\"cancelled\"} %d\n",
		a.proposals.created.Load(), a.proposals.replayed.Load(), a.proposals.approved.Load(), a.proposals.selfApprovals.Load(), a.proposals.cancelled.Load())
	fmt.Fprintf(w, "# HELP payables_fx_rate_faults_total Exchange rate lookups that failed on a server or transport fault.\n# TYPE payables_fx_rate_faults_total counter\npayables_fx_rate_faults_total %d\n", a.proposals.rateFaults.Load())
}
