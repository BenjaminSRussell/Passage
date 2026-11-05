package api

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// Tender limits (§6.34.13 tender_timeout_min: default 120, allowed 5 to 1,440).
const (
	defaultTenderMin = 120
	minTenderMin     = 5
	maxTenderMin     = 1440
	maxTenderCarrier = 20
)

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

var tenderStatuses = []string{domain.TenderPending, domain.TenderAccepted, domain.TenderFailed}

// tenderCarrier is one route-guide entry, in rank order, with the rate quoted to it.
type tenderCarrier struct {
	CarrierID  string           `json:"carrierId"`
	RateQuoted *decimal.Decimal `json:"rateQuoted"`
}

type tenderRequest struct {
	Carriers   []tenderCarrier `json:"carriers"`
	Currency   string          `json:"currency"`
	TimeoutMin int             `json:"timeoutMin"`
}

type tenderRespondRequest struct {
	CarrierID string `json:"carrierId"`
	Accept    *bool  `json:"accept"`
}

// tenderAttempt is one carrier's turn, with the rate quoted to it.
type tenderAttempt struct {
	domain.Attempt
	RateQuoted *string `json:"rateQuoted,omitempty"`
}

// tenderResponse is the wire view of a tender. Overdue says the pending
// attempt's deadline has passed and the next sweep or response will cascade it.
type tenderResponse struct {
	ID                string          `json:"id"`
	LoadID            string          `json:"loadId"`
	Status            string          `json:"status"`
	Carriers          []string        `json:"carriers"`
	Currency          string          `json:"currency,omitempty"`
	TimeoutMin        int             `json:"timeoutMin"`
	Attempts          []tenderAttempt `json:"attempts"`
	AcceptedCarrierID string          `json:"acceptedCarrierId,omitempty"`
	PlannerTask       bool            `json:"plannerTask"`
	PendingCarrierID  string          `json:"pendingCarrierId,omitempty"`
	PendingExpiresAt  *time.Time      `json:"pendingExpiresAt,omitempty"`
	Overdue           bool            `json:"overdue"`
	Version           int             `json:"version"`
	CreatedAt         time.Time       `json:"createdAt"`
}

func (a *API) startTender(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	loadID := chi.URLParam(r, "id")
	if !uuidRe.MatchString(loadID) {
		validationFailed(w, r, map[string]string{"id": "must be a lower-case UUID"})
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req tenderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	carriers := make([]string, 0, len(req.Carriers))
	rates := map[string]decimal.Decimal{}
	for _, c := range req.Carriers {
		switch {
		case !uuidRe.MatchString(c.CarrierID):
			fields["carriers"] = "every carrier id must be a lower-case UUID"
		case slices.Contains(carriers, c.CarrierID):
			fields["carriers"] = "list each carrier once, in route-guide rank order"
		case c.RateQuoted != nil && (c.RateQuoted.IsNegative() || !c.RateQuoted.Equal(c.RateQuoted.Round(2))):
			fields["carriers"] = "a quoted rate must be zero or more with at most two decimals"
		}
		carriers = append(carriers, c.CarrierID)
		if c.RateQuoted != nil {
			rates[c.CarrierID] = *c.RateQuoted
		}
	}
	if len(carriers) == 0 || len(carriers) > maxTenderCarrier {
		fields["carriers"] = "between 1 and 20 carriers are required"
	}
	if len(rates) > 0 && !currencyRe.MatchString(req.Currency) {
		fields["currency"] = "is required with quoted rates: an ISO 4217 alpha-3 code"
	}
	if len(rates) == 0 && req.Currency != "" {
		fields["currency"] = "is only allowed with quoted rates"
	}
	timeout := req.TimeoutMin
	if timeout == 0 {
		timeout = defaultTenderMin
	}
	if timeout < minTenderMin || timeout > maxTenderMin {
		fields["timeoutMin"] = "must be between 5 and 1440 minutes"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	t, err := domain.NewTender(loadID, carriers, time.Duration(timeout)*time.Minute)
	if err != nil {
		validationFailed(w, r, map[string]string{"carriers": err.Error()})
		return
	}
	if len(rates) > 0 {
		t.Currency, t.Rates = req.Currency, rates
	}
	now := a.now()
	t.Send(now)
	created, err := a.store.CreateTender(r.Context(), t, idem, now)
	if err != nil {
		a.fail(w, r, "start tender failed", err)
		return
	}
	a.tendersStarted.Add(1)
	httpx.JSON(w, http.StatusCreated, a.tenderView(created))
}

func (a *API) listTenders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.TenderFilter{Status: q.Get("status"), LoadID: q.Get("loadId")}
	fields := map[string]string{}
	if f.Status != "" && !slices.Contains(tenderStatuses, f.Status) {
		fields["status"] = "must be one of " + strings.Join(tenderStatuses, ", ")
	}
	checkOptionalID(fields, "loadId", f.LoadID)
	// The workflow service lists only to adopt the open tender of one load
	// (TMS-0114 on start), so its duty reaches the list only with a loadId.
	if p, _ := tenant.FromContext(r.Context()); f.LoadID == "" && !p.HasDuty(DutyTender) {
		fields["loadId"] = "is required for the workflow service"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	tenders, err := a.store.ListTenders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list tenders failed", err)
		return
	}
	views := make([]tenderResponse, 0, len(tenders))
	for _, t := range tenders {
		views = append(views, a.tenderView(t))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": views})
}

func (a *API) getTender(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("tender"))
		return
	}
	t, err := a.store.GetTender(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get tender failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, a.tenderView(t))
}

func (a *API) respondTender(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("tender"))
		return
	}
	var req tenderRespondRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !uuidRe.MatchString(req.CarrierID) {
		fields["carrierId"] = "must be a lower-case UUID"
	}
	if req.Accept == nil {
		fields["accept"] = "is required: true to accept, false to decline"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	t, err := a.store.RespondTender(r.Context(), id, req.CarrierID, *req.Accept, a.now())
	if err != nil {
		a.fail(w, r, "respond to tender failed", err)
		return
	}
	a.tenderResponses.Add(1)
	httpx.JSON(w, http.StatusOK, a.tenderView(t))
}

// expireTenders runs the expiry sweep for the caller's tenant (BR-34.3) and
// returns the tenders it cascaded.
func (a *API) expireTenders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	tenders, err := a.store.ExpireTenders(r.Context(), a.now())
	if err != nil {
		a.fail(w, r, "expire tenders failed", err)
		return
	}
	a.tendersExpired.Add(int64(len(tenders)))
	views := make([]tenderResponse, 0, len(tenders))
	for _, t := range tenders {
		views = append(views, a.tenderView(t))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": views})
}

func (a *API) tenderView(t domain.Tender) tenderResponse {
	v := tenderResponse{
		ID: t.ID, LoadID: t.LoadID, Status: t.Status, Carriers: t.Carriers, Currency: t.Currency,
		TimeoutMin: int(t.Timeout / time.Minute), Attempts: make([]tenderAttempt, 0, len(t.Attempts)),
		AcceptedCarrierID: t.AcceptedCarrier, PlannerTask: t.PlannerTask, Overdue: t.Overdue(a.now()),
		Version: t.Version, CreatedAt: t.CreatedAt,
	}
	if v.Carriers == nil {
		v.Carriers = []string{}
	}
	for _, at := range t.Attempts {
		view := tenderAttempt{Attempt: at}
		if rate, ok := t.Rates[at.CarrierID]; ok {
			s := rate.StringFixed(2)
			view.RateQuoted = &s
		}
		v.Attempts = append(v.Attempts, view)
	}
	if p, ok := t.Pending(); ok && t.Status == domain.TenderPending {
		expires := p.ExpiresAt
		v.PendingCarrierID, v.PendingExpiresAt = p.CarrierID, &expires
	}
	return v
}
