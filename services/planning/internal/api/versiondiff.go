package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

// diffVersions compares two versions of a plan (FR-22.14): the planned orders
// added, removed and changed from version `from` to version `to`, and whether
// the two plan hashes are equal. `limit` caps each list; the counts do not.
func (a *API) diffVersions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "planCode")
	fields := map[string]string{}
	if !planCodePattern.MatchString(code) {
		fields["planCode"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	num := func(name string) int {
		n, err := strconv.Atoi(r.URL.Query().Get(name))
		if err != nil || n < 1 {
			fields[name] = "is required and must be a positive version number"
			return 0
		}
		return n
	}
	from, to := num("from"), num("to")
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	d, err := a.store.DiffVersions(r.Context(), code, from, to, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}
