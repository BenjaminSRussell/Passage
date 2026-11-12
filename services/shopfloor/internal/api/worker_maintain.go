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

// registerWorkerMaintenance mounts the edit, deactivate and reactivate routes of a worker (D2a').
// They use the existing setup duty, like creating a worker.
func (a *API) registerWorkerMaintenance(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/workers/{id}", a.updateWorker)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}:deactivate", a.deactivateWorker)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}:reactivate", a.reactivateWorker)
}

// workerUpdateRequest is the editable part of a worker; the party link cannot change.
type workerUpdateRequest struct {
	Name     string  `json:"name"`
	BadgeID  *string `json:"badgeId"`
	UserID   *string `json:"userId"`
	CostRate string  `json:"costRate"`
	Currency string  `json:"currency"`
	// TimeZone replaces the worker's site zone when sent; omitted keeps it.
	TimeZone string `json:"timeZone"`
}

type deactivateRequest struct {
	Reason string `json:"reason"`
}

// workerChange is the common head of the three routes: tenant, authenticated user, worker id,
// Idempotency-Key and If-Match.
func workerChange(w http.ResponseWriter, r *http.Request, what string) (id, user string, idem store.Idem, version int, ok bool) {
	if !validTenant(w, r) {
		return
	}
	if user, ok = authenticatedUser(w, r, what); !ok {
		return
	}
	if id, ok = pathID(w, r, store.CodeWorkerNotFound, "worker"); !ok {
		return
	}
	if idem, ok = readIdem(w, r); !ok {
		return
	}
	version, ok = ifMatch(w, r)
	return
}

func (a *API) updateWorker(w http.ResponseWriter, r *http.Request) {
	id, user, idem, version, ok := workerChange(w, r, "editing a worker")
	if !ok {
		return
	}
	var req workerUpdateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	name := strings.TrimSpace(req.Name)
	if n := len([]rune(name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if req.BadgeID != nil && (len(*req.BadgeID) < 1 || len(*req.BadgeID) > 64) {
		fields["badgeId"] = "must be 1 to 64 characters, or null for no badge"
	}
	if req.UserID != nil && (len(*req.UserID) < 1 || len(*req.UserID) > 200) {
		fields["userId"] = "must be 1 to 200 characters, or null for none"
	}
	rate, err := decimal.NewFromString(req.CostRate)
	if err != nil || rate.IsNegative() {
		fields["costRate"] = "must be a non-negative decimal string"
	}
	if !currencyRe.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if req.TimeZone != "" && !validZone(req.TimeZone) {
		fields["timeZone"] = "must be an IANA time zone name such as America/Chicago"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.UpdateWorker(r.Context(), id, version, store.WorkerInput{Name: name, BadgeID: req.BadgeID, UserID: req.UserID, CostRate: rate, Currency: req.Currency, TimeZone: req.TimeZone}, user, idem)
	if err != nil {
		a.fail(w, r, "update worker", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) deactivateWorker(w http.ResponseWriter, r *http.Request) {
	id, user, idem, version, ok := workerChange(w, r, "deactivating a worker")
	if !ok {
		return
	}
	var req deactivateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, bad := cleanReason(req.Reason)
	if bad != "" {
		validationFailed(w, r, map[string]string{"reason": bad})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.DeactivateWorker(r.Context(), id, version, reason, user, idem)
	if err != nil {
		a.fail(w, r, "deactivate worker", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) reactivateWorker(w http.ResponseWriter, r *http.Request) {
	id, user, idem, version, ok := workerChange(w, r, "reactivating a worker")
	if !ok {
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.ReactivateWorker(r.Context(), id, version, user, idem)
	if err != nil {
		a.fail(w, r, "reactivate worker", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// validZone reports whether name is an IANA time zone the service can load.
func validZone(name string) bool {
	if len(name) > 64 || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}
