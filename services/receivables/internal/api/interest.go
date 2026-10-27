package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const dutyInterest = "InterestPost"

type interestRequest struct {
	LegalEntityID   string          `json:"legalEntityId"`
	CalculationDate string          `json:"calculationDate"`
	AnnualRate      decimal.Decimal `json:"annualRate"`
	Basis           string          `json:"basis"`
	GraceDays       *int            `json:"graceDays"`
}

// interestRoutes mounts the interest custom methods (static paths, so chi matches
// the literal ':') and the list of posted notes.
func (a *API) interestRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyInterest)).Post("/interest-notes:calculate", a.calculateInterest)
	r.With(authz.RequireDuty(dutyInterest)).Post("/interest-notes:post", a.postInterest)
	r.With(authz.RequireDuty(dutyView)).Get("/interest-notes", a.listInterestNotes)
}

// listInterestNotes handles GET /interest-notes?customer=.
func (a *API) listInterestNotes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	customer := r.URL.Query().Get("customer")
	if customer != "" && !ids.Valid(customer) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListInterestNotes(r.Context(), customer, limit)
	if err != nil {
		a.fail("list interest notes", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// calculateInterest handles POST /interest-notes:calculate (preview only).
func (a *API) calculateInterest(w http.ResponseWriter, r *http.Request) {
	a.interest(w, r, false)
}

// postInterest handles POST /interest-notes:post. Idempotency-Key is required and
// a retry replays the stored response; the natural key (open item, period) also
// keeps a second post with a new key from charging an item twice in a month.
func (a *API) postInterest(w http.ResponseWriter, r *http.Request) {
	a.interest(w, r, true)
}

func (a *API) interest(w http.ResponseWriter, r *http.Request, post bool) {
	if !validTenant(w, r) {
		return
	}
	key := ""
	if post {
		var ok bool
		if key, ok = requiredKey(w, r); !ok {
			return
		}
	}
	var req interestRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if _, err := store.ParseDay(req.CalculationDate); err != nil {
		fields["calculationDate"] = "must be a YYYY-MM-DD date"
	}
	if !req.AnnualRate.IsPositive() || req.AnnualRate.GreaterThan(decimal.NewFromInt(1)) {
		fields["annualRate"] = "must be a decimal fraction above 0 and at most 1 (0.0900 is 9%)"
	}
	if !domain.ValidBasis(req.Basis) {
		fields["basis"] = "must be ACT_365 or ACT_360"
	}
	grace := domain.DefaultGraceDays
	if req.GraceDays != nil {
		grace = *req.GraceDays
	}
	if grace < 0 || grace > maxGraceDays {
		fields["graceDays"] = fmt.Sprintf("must be 0 to %d", maxGraceDays)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	in := store.InterestRequest{LegalEntityID: req.LegalEntityID, CalculationDate: req.CalculationDate,
		AnnualRate: req.AnnualRate, Basis: req.Basis, GraceDays: grace}
	var out store.InterestResult
	var err error
	if post {
		out, err = a.store.PostInterest(r.Context(), in, key)
	} else {
		out, err = a.store.CalculateInterest(r.Context(), in)
	}
	if err != nil {
		a.fail("interest", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
