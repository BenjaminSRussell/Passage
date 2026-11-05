package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// dutyAgingRun runs the aging snapshot job. The PRD names no duty for scheduled
// jobs (§5.30.16); the scheduler's service principal carries this one, and an AR
// manager may re-run a missed night.
const dutyAgingRun = "receivables.aging.run"

// statementRoutes mounts customer statements (FR-30.13, receivables.view) and the aging
// snapshot job with its stored balances (§5.30.16).
func (a *API) statementRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get("/statements", a.statement)
	r.With(authz.RequireDuty(dutyAgingRun)).Post("/aging-snapshots", a.runAgingSnapshot)
	r.With(authz.RequireDuty(dutyView)).Get("/aging-snapshots", a.listAgingSnapshots)
}

// today is the current UTC calendar day at midnight.
func today() time.Time {
	t, _ := store.ParseDay(time.Now().UTC().Format("2006-01-02"))
	return t
}

// dayParam reads an optional YYYY-MM-DD query or body value; "" is today.
func dayParam(raw string) (time.Time, bool) {
	if raw == "" {
		return today(), true
	}
	t, err := store.ParseDay(raw)
	return t, err == nil
}

// statement handles GET /statements?legalEntityId=&customer=&asOf=: the customer's
// documents open at the end of asOf (default today) by currency, with running
// balance, closing balance and aging footer (FR-30.13). It reads only (BR-30.10).
func (a *API) statement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le, customer := q.Get("legalEntityId"), q.Get("customer")
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(customer) {
		fields["customer"] = "must be a UUID"
	}
	asOf, ok := dayParam(q.Get("asOf"))
	if !ok {
		fields["asOf"] = "must be a YYYY-MM-DD date"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, le) {
		return
	}
	sections, err := a.store.Statement(r.Context(), le, customer, asOf)
	if err != nil {
		a.fail("statement", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"legalEntityId": le,
		"customerId":    customer,
		"asOf":          asOf.Format("2006-01-02"),
		"sections":      sections,
	})
}

// agingSnapshotRequest names the legal entity and key date of one snapshot run.
type agingSnapshotRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	AsOf          string `json:"asOf"`
}

// runAgingSnapshot handles POST /aging-snapshots: the dated balance snapshot for a
// legal entity (§5.30.16, key = (customer, date)). A re-run for the same date
// rewrites the rows and emits customerbalance.changed.v1 only for balances that
// moved. A future date is refused: the job stores today's balances, aged at asOf.
func (a *API) runAgingSnapshot(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req agingSnapshotRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	asOf, ok := dayParam(req.AsOf)
	switch {
	case !ok:
		fields["asOf"] = "must be a YYYY-MM-DD date"
	case asOf.After(today()):
		fields["asOf"] = "must not be after today"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	res, err := a.store.RunAgingSnapshot(r.Context(), req.LegalEntityID, asOf)
	if err != nil {
		a.fail("aging snapshot", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// listAgingSnapshots handles GET /aging-snapshots?legalEntityId=&asOf=&customer=&limit=:
// the stored balances for a key date (default the latest), riskiest first.
func (a *API) listAgingSnapshots(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.SnapshotFilter{LegalEntityID: q.Get("legalEntityId"), AsOf: q.Get("asOf"), CustomerID: q.Get("customer")}
	fields := map[string]string{}
	if !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.AsOf != "" {
		if _, err := store.ParseDay(f.AsOf); err != nil {
			fields["asOf"] = "must be a YYYY-MM-DD date"
		}
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customer"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, f.LegalEntityID) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListBalanceSnapshots(r.Context(), f, limit)
	if err != nil {
		a.fail("list aging snapshots", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
