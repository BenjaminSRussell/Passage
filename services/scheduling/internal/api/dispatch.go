package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// maxDispatchDays bounds the days one dispatch list covers.
const maxDispatchDays = 7

// dispatchResource is the header of a dispatch list.
type dispatchResource struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	TimeZone string `json:"timeZone"`
}

// dispatchList is the per-resource dispatch list for a day or shift (PRD
// 4.23.15): the jobs booked on the resource in the window in start order,
// numbered from 1. The window is the resource's local date and a local
// minute-of-day range, so a night shift is asked as startMinute 1320,
// endMinute 1800.
func (a *API) dispatchList(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	id := strings.ToLower(q.Get("resourceId"))
	if !ids.Valid(id) {
		fields["resourceId"] = "must be a resource UUID"
	}
	date, err := time.Parse(time.DateOnly, q.Get("date"))
	if err != nil {
		fields["date"] = "must be a local calendar date, YYYY-MM-DD"
	}
	num := func(name string, def, lo, hi int) int {
		raw := q.Get(name)
		if raw == "" {
			return def
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < lo || n > hi {
			fields[name] = "must be a whole number from " + strconv.Itoa(lo) + " to " + strconv.Itoa(hi)
			return def
		}
		return n
	}
	days := num("days", 1, 1, maxDispatchDays)
	startMinute := num("startMinute", 0, 0, 1439)
	endMinute := num("endMinute", 1440, 1, 2880)
	if _, bad := fields["startMinute"]; !bad && days == 1 && endMinute <= startMinute {
		fields["endMinute"] = "must be after startMinute"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	res, err := a.store.GetResource(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// A wrong zone would shift every line of the list, so it fails loudly
	// instead of falling back to UTC (zones are validated when saved).
	loc, err := time.LoadLocation(res.TimeZone)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	from, to := domain.DispatchWindow(loc, date, days, startMinute, endMinute)
	if !to.After(from) {
		invalid(w, r, map[string]string{"endMinute": "the window falls inside a daylight-saving gap of " + res.TimeZone})
		return
	}
	rows, truncated, err := a.store.DispatchRows(r.Context(), id, from, to)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	lines, total := domain.DispatchLines(rows, from, to)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"resource":     dispatchResource{ID: res.ID, Code: res.Code, Name: res.Name, TimeZone: res.TimeZone},
		"date":         date.Format(time.DateOnly),
		"days":         days,
		"windowStart":  from.Format(time.RFC3339),
		"windowEnd":    to.Format(time.RFC3339),
		"totalMinutes": total,
		"lines":        lines,
		"truncated":    truncated,
	})
}
