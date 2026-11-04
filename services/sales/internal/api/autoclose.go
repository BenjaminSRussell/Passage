package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	// defaultAutoCloseDays is how long an INVOICED order must stay unchanged
	// before it closes. The PRD gives no figure (the payment signal of wave 30
	// is not available), so it is a request parameter with this default.
	defaultAutoCloseDays  = 30
	maxAutoCloseDays      = 365
	defaultAutoCloseBatch = 100
	maxAutoCloseBatch     = 500
)

// autoCloseOrders is the nightly order auto-close (PRD 5.27.16): INVOICED
// orders unchanged for graceDays move to CLOSED (BR-27.5), oldest first, up to
// limit. It is a plain endpoint that the scheduler calls, safe to repeat; with
// dryRun=true it only lists what would close.
func (a *API) autoCloseOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.AutoCloseFilter{LegalEntityID: legalEntity, GraceDays: defaultAutoCloseDays, Limit: defaultAutoCloseBatch}
	fields := map[string]string{}
	if raw := q.Get("graceDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxAutoCloseDays {
			fields["graceDays"] = fmt.Sprintf("must be 0 to %d", maxAutoCloseDays)
		} else {
			f.GraceDays = n
		}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxAutoCloseBatch {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxAutoCloseBatch)
		} else {
			f.Limit = n
		}
	}
	if raw := q.Get("dryRun"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			fields["dryRun"] = "must be true or false"
		}
		f.DryRun = b
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	res, err := a.store.AutoCloseOrders(r.Context(), f, p.UserID, a.now())
	if err != nil {
		a.fail(w, r, "auto-close orders", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
