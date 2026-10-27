package api

import (
	"net/http"
	"regexp"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var (
	companyPrefixPattern = regexp.MustCompile(`^[0-9]{6,12}$`)
	ssccCursorPattern    = regexp.MustCompile(`^[0-9]{18}$`)
)

type ssccPage struct {
	Items      []ssccItem `json:"items"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

// ssccItem is one register row with the first date its SSCC could be issued
// again (BR-28.5, sscc_no_reuse_months).
type ssccItem struct {
	store.SSCCRow
	NoReuseUntil string `json:"noReuseUntil"`
}

// listSSCCs answers GET /api/v1/fulfillment/sscc-register (section 5.28.14):
// issued SSCCs ordered by SSCC, filtered by GS1 company prefix, customer and
// issue-date range, one page of limit rows at a time. nextCursor is the last
// SSCC of the page when another page exists.
func (a *API) listSSCCs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SSCCFilter{CompanyPrefix: q.Get("companyPrefix"), CustomerID: q.Get("customerId"), AfterSSCC: q.Get("cursor")}
	if f.CompanyPrefix != "" && !companyPrefixPattern.MatchString(f.CompanyPrefix) {
		fail(w, r, "companyPrefix", "must be 6 to 12 digits")
		return
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fail(w, r, "customerId", "must be a UUID")
		return
	}
	for _, bound := range []struct {
		name string
		into *time.Time
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(bound.name); v != "" {
			t, err := time.Parse(dateLayout, v)
			if err != nil {
				fail(w, r, bound.name, "must be a YYYY-MM-DD date")
				return
			}
			*bound.into = t
		}
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		fail(w, r, "to", "must not be before from")
		return
	}
	if f.AfterSSCC != "" && !ssccCursorPattern.MatchString(f.AfterSSCC) {
		fail(w, r, "cursor", "must be the 18-digit SSCC of a previous page")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit + 1 // one more row tells whether another page exists
	rows, err := a.store.ListSSCCs(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	page := ssccPage{Items: []ssccItem{}}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = rows[limit-1].SSCC
	}
	for _, row := range rows {
		until := domain.SSCCNoReuseUntil(row.IssuedAt, domain.SSCCNoReuseMonths)
		page.Items = append(page.Items, ssccItem{SSCCRow: row, NoReuseUntil: until.Format(dateLayout)})
	}
	httpx.JSON(w, http.StatusOK, page)
}
