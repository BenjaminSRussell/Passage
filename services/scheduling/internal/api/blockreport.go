package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// blockReport totals the capacity blocks of all resources (or one) over a
// range: the Block utilization report (PRD 4.23.14).
func (a *API) blockReport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	resourceID := ""
	fields := map[string]string{}
	if raw := q.Get("resourceId"); raw != "" {
		if id := strings.ToLower(raw); ids.Valid(id) {
			resourceID = id
		} else {
			fields["resourceId"] = "must be a UUID"
		}
	}
	from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
	to, errTo := time.Parse(time.RFC3339, q.Get("to"))
	if errFrom != nil || errTo != nil || !to.After(from) || to.Sub(from) > store.MaxCapacityDays*24*time.Hour {
		fields["from"] = fmt.Sprintf("from and to are RFC 3339 times, to after from, at most %d days apart", store.MaxCapacityDays)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	out, err := a.store.BlockReport(r.Context(), resourceID, from.UTC(), to.UTC())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
