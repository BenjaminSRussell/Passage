package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

type andonClearRequest struct {
	Reason string `json:"reason"`
}

// sweepAndon is the scheduled andon job (FR-25.11, BR-25.10): it raises the downtime alerts a
// silent machine could not raise, clears alerts whose condition ended, and applies the scrap
// rate rule to the latest stored OEE buckets. Repeating it changes nothing, so it takes no
// Idempotency-Key and no body. It runs under DutyOEERollup: the same scheduler identity runs
// the OEE rollup whose buckets the scrap rate rule reads, and no person role holds it.
func (a *API) sweepAndon(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	out, err := a.store.SweepAndon(r.Context(), a.now().UTC())
	if err != nil {
		a.fail(w, r, "sweep andon", err)
		return
	}
	a.andonsRaised.Add(int64(out.DowntimeRaised + out.ScrapRateRaised))
	httpx.JSON(w, http.StatusOK, out)
}

// clearAndon clears a quality fail alert with a reason; the clearing user is recorded. It runs
// under DutyCheckOverride: dispositioning a failed quality check with a reason is the shop
// floor supervisor's override authority (FR-25.5), and the alert is that failed check.
func (a *API) clearAndon(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "clearing an andon alert")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeAndonNotFound, "andon alert")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req andonClearRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, problem := cleanReason(req.Reason)
	if problem != "" {
		validationFailed(w, r, map[string]string{"reason": problem})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.ClearAndon(r.Context(), id, reason, user, a.now().UTC(), idem)
	if err != nil {
		a.fail(w, r, "clear andon", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
