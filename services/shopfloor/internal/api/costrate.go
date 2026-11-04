package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// registerCostRates mounts the dated cost rate routes of a worker (D2a'). Adding a rate uses the
// setup duty, like creating and editing a worker; reading uses the view duty.
func (a *API) registerCostRates(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}/cost-rates", a.addCostRate)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/workers/{id}/cost-rates", a.listCostRates)
}

type costRateRequest struct {
	EffectiveFrom string  `json:"effectiveFrom"`
	CostRate      string  `json:"costRate"`
	Note          *string `json:"note"`
}

type costRateList struct {
	Items []store.CostRate `json:"items"`
}

// Dates outside this window are almost certainly typos, and would price every day of the worker.
var (
	costRateEarliest = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	costRateLatest   = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

func (a *API) addCostRate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "setting a cost rate")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeWorkerNotFound, "worker")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req costRateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	from, err := time.Parse(dateLayout, req.EffectiveFrom)
	if err != nil || from.Before(costRateEarliest) || !from.Before(costRateLatest) {
		fields["effectiveFrom"] = "must be a date (YYYY-MM-DD) between 2000-01-01 and 2099-12-31"
	}
	rate, err := decimal.NewFromString(req.CostRate)
	if err != nil || rate.IsNegative() || rate.Exponent() < -6 || rate.Abs().GreaterThanOrEqual(decimal.New(1, 14)) {
		fields["costRate"] = "must be a non-negative decimal string with at most 6 decimals"
	}
	var note *string
	if req.Note != nil {
		n := strings.TrimSpace(*req.Note)
		if c := len([]rune(n)); c < 1 || c > 500 {
			fields["note"] = "must be 1 to 500 characters, or null"
		} else {
			note = &n
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.AddCostRate(r.Context(), id, store.CostRateInput{EffectiveFrom: req.EffectiveFrom, CostRate: rate, Note: note}, user, idem)
	if err != nil {
		a.fail(w, r, "add cost rate", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listCostRates(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeWorkerNotFound, "worker")
	if !ok {
		return
	}
	items, err := a.store.ListCostRates(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list cost rates", err)
		return
	}
	httpx.JSON(w, http.StatusOK, costRateList{Items: items})
}
