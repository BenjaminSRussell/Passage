package api

import (
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// worklist handles GET /worklist?legalEntityId=&asOf=: customers ranked by risk
// score as of the date (FR-30.6 worklist, FR-30.10 score).
func (a *API) worklist(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	le := q.Get("legalEntityId")
	if !ids.Valid(le) {
		badQuery(w, r, "legalEntityId", "must be a UUID")
		return
	}
	if !scopeAllows(w, r, le) {
		return
	}
	asOf := time.Now().UTC()
	if raw := q.Get("asOf"); raw != "" {
		t, err := store.ParseDay(raw)
		if err != nil {
			badQuery(w, r, "asOf", "must be a YYYY-MM-DD date")
			return
		}
		asOf = t
	}
	rows, err := a.store.Worklist(r.Context(), le, asOf)
	if err != nil {
		a.fail("worklist", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"asOf": asOf.Format("2006-01-02"), "items": rows})
}
