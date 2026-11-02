package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	maxProjectionItems   = 20
	defaultProjectionDay = 84
	maxProjectionWindow  = 730
	// daysOfSupplyLookAhead is how far past the horizon demand is read so that
	// days of supply in the last bucket still sees the following 30 days.
	daysOfSupplyLookAhead = 30
)

// projectionQuery is the validated query of GET /plans/{planCode}/projection.
type projectionQuery struct {
	itemIDs     []string
	bucket      string
	horizonDays int
	unitCost    decimal.Decimal
	currency    string // empty: no valuation
}

func parseProjectionQuery(r *http.Request) (projectionQuery, map[string]string) {
	q := r.URL.Query()
	f := map[string]string{}
	out := projectionQuery{bucket: q.Get("bucket"), horizonDays: defaultProjectionDay}
	if out.bucket == "" {
		out.bucket = domain.BucketWeek
	}
	if _, err := domain.BucketDays(out.bucket); err != nil {
		f["bucket"] = "must be day, week or month"
	}
	seen := map[string]bool{}
	for _, id := range q["itemId"] {
		if !ids.Valid(id) {
			f["itemId"] = "every itemId must be a UUID"
			break
		}
		if !seen[id] {
			seen[id] = true
			out.itemIDs = append(out.itemIDs, id)
		}
	}
	if len(out.itemIDs) == 0 && f["itemId"] == "" {
		f["itemId"] = "at least one itemId is required"
	}
	if len(out.itemIDs) > maxProjectionItems {
		f["itemId"] = "at most 20 items per request"
	}
	if raw := q.Get("horizonDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxProjectionWindow {
			f["horizonDays"] = "must be 1 to 730"
		} else {
			out.horizonDays = n
		}
	}
	decimalParam := func(name string) decimal.Decimal {
		raw := q.Get(name)
		if raw == "" {
			return decimal.Zero
		}
		d, err := decimal.NewFromString(raw)
		if err != nil || d.IsNegative() {
			f[name] = "must be a decimal that is not negative"
			return decimal.Zero
		}
		return d
	}
	out.unitCost = decimalParam("unitCost")
	out.currency = q.Get("currency")
	if out.currency != "" && !currencyCode.MatchString(out.currency) {
		f["currency"] = "must be an ISO 4217 code such as EUR"
	}
	if (out.currency == "") != (q.Get("unitCost") == "") {
		f["currency"] = "unitCost and currency are given together or not at all"
	}
	return out, f
}

// planProjection serves the supply schedule workbench and the inventory
// forecast of FR-22.21 and FR-22.22 over the current version of a plan (one
// site): per item, the bucketed demand, scheduled and planned supply, projected balance,
// shortage (a balance below zero; the run already nets the safety stock as a
// demand, so the balance is after it), days of supply and (given a unit cost)
// value, all read from the stored net requirements and not entered.
func (a *API) planProjection(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "planCode")
	q, fields := parseProjectionQuery(r)
	if !planCodePattern.MatchString(code) {
		fields["planCode"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	src, err := a.store.ReadProjectionSource(r.Context(), code, q.itemIDs, q.horizonDays+daysOfSupplyLookAhead)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	asOf, err := time.Parse("2006-01-02", src.AsOf)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	valued := q.currency != ""
	cost := money.Amount{Value: q.unitCost, Currency: q.currency}
	if !valued {
		cost = money.Amount{Currency: "XXX"}
	}
	date := func(day int) string { return asOf.AddDate(0, 0, day).Format("2006-01-02") }
	// A month bucket of a dated plan is a calendar month; the first runs from
	// the as-of date to the end of its month.
	// A week bucket is an ISO week (Monday to Sunday); the first runs from the
	// as-of date to the Sunday of its week.
	var periodEnds []int
	switch q.bucket {
	case domain.BucketMonth:
		periodEnds = domain.CalendarMonthEnds(asOf, q.horizonDays)
	case domain.BucketWeek:
		periodEnds = domain.CalendarWeekEnds(asOf, q.horizonDays)
	}

	items := make([]map[string]any, 0, len(q.itemIDs))
	for _, id := range q.itemIDs {
		days, perr := netDays(src.Rows[id], asOf)
		if perr != nil {
			httpx.Fail(w, r, perr)
			return
		}
		opening, p, perr := domain.ProjectPlanned(days, domain.ProjectionInput{
			UnitCost: cost, HorizonDays: q.horizonDays, Bucket: q.bucket, PeriodEnds: periodEnds,
		})
		if perr != nil {
			// The query is validated and stored quantities are not negative,
			// so this is a server fault, not the caller's.
			httpx.Fail(w, r, perr)
			return
		}
		periods := make([]map[string]any, 0, len(p.Periods))
		for _, row := range p.Periods {
			var dos, value any
			if row.DaysOfSupply != nil {
				dos = row.DaysOfSupply.String()
			}
			if valued {
				value = row.Value.Value.StringFixed(money.MinorUnits(q.currency))
			}
			periods = append(periods, map[string]any{
				"startDay": row.Start, "endDay": row.End, "startDate": date(row.Start), "endDate": date(row.End),
				"demand": row.Demand.String(), "supply": row.Supply.String(), "planned": row.Planned.String(),
				"closingOnHand": row.ClosingOnHand.String(),
				"shortage":      row.Shortage, "value": value, "daysOfSupply": dos,
			})
		}
		shortages := 0
		for _, row := range p.Periods {
			if row.Shortage {
				shortages++
			}
		}
		items = append(items, map[string]any{
			"itemId": id, "planned": len(days) > 0, "openingOnHand": opening.String(), "shortagePeriods": shortages,
			"periods": periods,
			"totals": map[string]string{"demand": p.TotalDemand.String(), "supply": p.TotalSupply.String(),
				"planned": p.TotalPlanned.String(), "closingOnHand": p.ClosingOnHand.String()},
		})
	}
	var currency any
	if valued {
		currency = q.currency
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"planCode": code, "version": src.Version, "asOf": src.AsOf, "siteId": src.SiteID, "bucket": q.bucket,
		"horizonDays": q.horizonDays, "currency": currency, "items": items,
	})
}

// netDays turns stored net requirement rows into day offsets from the version's
// as-of date.
func netDays(rows []store.NetRequirement, asOf time.Time) ([]domain.NetDay, error) {
	out := make([]domain.NetDay, 0, len(rows))
	for _, n := range rows {
		day, err := time.Parse("2006-01-02", n.Date)
		if err != nil {
			return nil, err
		}
		d := domain.NetDay{Day: int(day.Sub(asOf).Hours() / 24)}
		for dst, raw := range map[*decimal.Decimal]string{&d.Gross: n.GrossRequirements, &d.Scheduled: n.ScheduledReceipts, &d.Planned: n.PlannedReceipts, &d.PAB: n.PAB} {
			v, err := decimal.NewFromString(raw)
			if err != nil {
				return nil, err
			}
			*dst = v
		}
		out = append(out, d)
	}
	return out, nil
}
