package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

type backorderPage struct {
	Items      []backorderItem `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// backorderItem is one short-picked line with the remainder still owed.
type backorderItem struct {
	store.BackorderRow
	Remainder decimal.Decimal `json:"qtyRemainder"`
	Open      bool            `json:"open"`
}

// listBackorders answers GET /api/v1/fulfillment/backorders, the short picks
// and backorders report (FR-28.2, section 5.28.14): lines a pick confirmed
// below plan, newest short pick first, by warehouse and short-pick date.
// Only open backorders are listed unless status=all. nextCursor is the last
// line of the page when another page exists.
func (a *API) listBackorders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.BackorderFilter{WarehouseID: q.Get("warehouseId"), AfterLineID: q.Get("cursor")}
	if f.WarehouseID != "" && !ids.Valid(f.WarehouseID) {
		fail(w, r, "warehouseId", "must be a UUID")
		return
	}
	switch q.Get("status") {
	case "", "open":
	case "all":
		f.IncludeClosed = true
	default:
		fail(w, r, "status", "must be open or all")
		return
	}
	if !dateRange(w, r, &f.From, &f.To) {
		return
	}
	if f.AfterLineID != "" && !ids.Valid(f.AfterLineID) {
		fail(w, r, "cursor", "must be the nextCursor of a previous page")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit + 1 // one more row tells whether another page exists
	rows, err := a.store.ListBackorders(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	page := backorderPage{Items: []backorderItem{}}
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = rows[limit-1].ShipmentLineID
	}
	for _, row := range rows {
		remainder, open := domain.Backorder(row.Status, row.Planned, row.Picked)
		page.Items = append(page.Items, backorderItem{BackorderRow: row, Remainder: remainder, Open: open})
	}
	httpx.JSON(w, http.StatusOK, page)
}

// dateRange reads the inclusive ?from= and ?to= dates; to may not be before from.
func dateRange(w http.ResponseWriter, r *http.Request, from, to *time.Time) bool {
	for _, bound := range []struct {
		name string
		into *time.Time
	}{{"from", from}, {"to", to}} {
		if v := r.URL.Query().Get(bound.name); v != "" {
			t, err := time.Parse(dateLayout, v)
			if err != nil {
				fail(w, r, bound.name, "must be a YYYY-MM-DD date")
				return false
			}
			*bound.into = t
		}
	}
	if !from.IsZero() && !to.IsZero() && to.Before(*from) {
		fail(w, r, "to", "must not be before from")
		return false
	}
	return true
}

type agingResponse struct {
	AsOf       string `json:"asOf"`
	ChargeDays int    `json:"chargeDays"`
	// Count and ChargeDueCount cover every outstanding balance that matches;
	// Items holds at most limit of them, oldest first.
	Count          int         `json:"count"`
	ChargeDueCount int         `json:"chargeDueCount"`
	Items          []agingItem `json:"items"`
}

// agingItem is one outstanding returnable balance with its age (FR-28.16).
type agingItem struct {
	store.ReturnableBalance
	OldestOutstanding *string `json:"oldestOutstandingDate"`
	DaysOutstanding   int     `json:"daysOutstanding"`
	ChargeDue         bool    `json:"chargeDue"`
}

// returnableAging answers GET /api/v1/fulfillment/returnable-packaging/aging
// (FR-28.16, section 5.28.14 "Returnable packaging balances ... days
// outstanding"): every balance above zero, aged first in, first out as of
// today, and flagged chargeDue once older than chargeDays (rtp_charge_days,
// default 90, 30 to 365). chargeDue=true lists only those; wave 30 charges them.
func (a *API) returnableAging(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	customerID := q.Get("customerId")
	if customerID != "" && !ids.Valid(customerID) {
		fail(w, r, "customerId", "must be a UUID")
		return
	}
	chargeDays := domain.DefaultRTPChargeDays
	if v := q.Get("chargeDays"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < domain.MinRTPChargeDays || n > domain.MaxRTPChargeDays {
			fail(w, r, "chargeDays", "must be an integer from 30 to 365")
			return
		}
		chargeDays = n
	}
	dueOnly := false
	switch q.Get("chargeDue") {
	case "", "false":
	case "true":
		dueOnly = true
	default:
		fail(w, r, "chargeDue", "must be true or false")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	rows, err := a.store.ReturnableAging(r.Context(), customerID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	asOf := dateOnly(a.now())
	all := make([]agingItem, 0, len(rows))
	out := agingResponse{AsOf: asOf.Format(dateLayout), ChargeDays: chargeDays, Items: []agingItem{}}
	for _, row := range rows {
		age := domain.AgeReturnables(row.Balance, row.Shipped, asOf, chargeDays)
		if dueOnly && !age.ChargeDue {
			continue
		}
		item := agingItem{ReturnableBalance: row.ReturnableBalance, DaysOutstanding: age.DaysOutstanding, ChargeDue: age.ChargeDue}
		if age.OldestOutstanding != nil {
			day := age.OldestOutstanding.Format(dateLayout)
			item.OldestOutstanding = &day
		}
		if age.ChargeDue {
			out.ChargeDueCount++
		}
		all = append(all, item)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].DaysOutstanding > all[j].DaysOutstanding })
	out.Count = len(all)
	if len(all) > limit {
		all = all[:limit]
	}
	out.Items = append(out.Items, all...)
	httpx.JSON(w, http.StatusOK, out)
}
