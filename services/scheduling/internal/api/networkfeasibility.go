package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// networkFeasibility lists the resource network findings of schedule runs:
// operations left unplaced as NETWORK_INFEASIBLE (SCHED-0009) and waits over
// the network maximum (SCHED-0010), the Network feasibility inquiry (PRD 4.23.14).
func (a *API) networkFeasibility(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.NetworkFeasibilityFilter{Limit: 100}
	fields := map[string]string{}
	if raw := q.Get("networkId"); raw != "" {
		if id := strings.ToLower(raw); ids.Valid(id) {
			f.NetworkID = id
		} else {
			fields["networkId"] = "must be a UUID"
		}
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				fields[name] = "must be an RFC 3339 timestamp"
				continue
			}
			*dst = &t
		}
	}
	if f.From != nil && f.To != nil && !f.To.After(*f.From) {
		fields["to"] = "must be after from"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxNetworkFindings {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", store.MaxNetworkFindings)
		} else {
			f.Limit = n
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	out, err := a.store.NetworkFeasibility(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
