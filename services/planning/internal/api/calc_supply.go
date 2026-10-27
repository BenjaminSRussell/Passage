package api

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// Stateless calculators for the supply schedule and inventory forecast
// (FR-22.21, FR-22.22) and shelf-life aware allocation (FR-22.20). Like the
// calculators in calc.go they store nothing and take no Idempotency-Key.

const maxCalcBatches = 1000

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// flowDTO is a dated quantity; the schedule reads it as day offset from day 0.
type flowDTO = dayQtyDTO

type supplyScheduleRequest struct {
	HorizonDays   int             `json:"horizonDays"`
	Bucket        string          `json:"bucket"`
	OpeningOnHand decimal.Decimal `json:"openingOnHand"`
	SafetyStock   decimal.Decimal `json:"safetyStock"`
	UnitCost      decimal.Decimal `json:"unitCost"`
	Currency      string          `json:"currency"`
	Demand        []flowDTO       `json:"demand"`
	Supply        []flowDTO       `json:"supply"`
	Planned       []flowDTO       `json:"planned"`
}

func (req supplyScheduleRequest) validate() map[string]string {
	f := map[string]string{}
	if req.HorizonDays < 1 || req.HorizonDays > maxCalcDays {
		f["horizonDays"] = "must be 1 to 3650"
	}
	if _, err := domain.BucketDays(req.Bucket); err != nil {
		f["bucket"] = "must be day, week or month"
	}
	if req.OpeningOnHand.IsNegative() {
		f["openingOnHand"] = "must not be negative"
	}
	if req.SafetyStock.IsNegative() {
		f["safetyStock"] = "must not be negative"
	}
	if req.UnitCost.IsNegative() {
		f["unitCost"] = "must not be negative"
	}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 code such as EUR"
	}
	for name, flows := range map[string][]flowDTO{"demand": req.Demand, "supply": req.Supply, "planned": req.Planned} {
		if len(flows) > maxCalcSales {
			f[name] = "must hold at most 10000 lines"
		}
		for i, fl := range flows {
			prefix := name + "[" + strconv.Itoa(i) + "]."
			if fl.Day < -maxCalcDays || fl.Day > maxCalcDays {
				f[prefix+"day"] = "must be -3650 to 3650; past-due days count on day 0"
			}
			if fl.Qty.IsNegative() {
				f[prefix+"qty"] = "must not be negative"
			}
		}
	}
	return f
}

func toFlows(in []flowDTO) []domain.Flow {
	out := make([]domain.Flow, 0, len(in))
	for _, f := range in {
		out = append(out, domain.Flow{Day: f.Day, Qty: f.Qty})
	}
	return out
}

// projectSupply answers POST /v1/planning/supply-schedule:project (FR-22.21, FR-22.22).
func (a *API) projectSupply(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req supplyScheduleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, err := domain.Project(domain.ProjectionInput{
		OpeningOnHand: req.OpeningOnHand, SafetyStock: req.SafetyStock,
		UnitCost: money.Amount{Value: req.UnitCost, Currency: req.Currency},
		Demand:   toFlows(req.Demand), Supply: toFlows(req.Supply), Planned: toFlows(req.Planned),
		HorizonDays: req.HorizonDays, Bucket: req.Bucket,
	})
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeInput, "supply schedule parameters are inconsistent"))
		return
	}
	periods := make([]map[string]any, 0, len(p.Periods))
	for _, row := range p.Periods {
		var dos any
		if row.DaysOfSupply != nil {
			dos = row.DaysOfSupply.String()
		}
		periods = append(periods, map[string]any{
			"startDay": row.Start, "endDay": row.End,
			"demand": row.Demand.String(), "supply": row.Supply.String(), "planned": row.Planned.String(),
			"closingOnHand": row.ClosingOnHand.String(), "safetyStock": row.SafetyStock.String(),
			"shortage": row.Shortage, "value": row.Value.Value.StringFixed(money.MinorUnits(req.Currency)), "daysOfSupply": dos,
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"bucket": req.Bucket, "horizonDays": req.HorizonDays, "currency": req.Currency, "periods": periods,
		"totals": map[string]string{"demand": p.TotalDemand.String(), "supply": p.TotalSupply.String(),
			"planned": p.TotalPlanned.String(), "closingOnHand": p.ClosingOnHand.String()},
	})
}

type batchDTO struct {
	ID        string          `json:"id"`
	Qty       decimal.Decimal `json:"qty"`
	ExpiryDay int             `json:"expiryDay"`
}

type shelfLifeRequest struct {
	DemandDay             int             `json:"demandDay"`
	DemandQty             decimal.Decimal `json:"demandQty"`
	RequiredShelfLifeDays int             `json:"requiredShelfLifeDays"`
	HorizonDay            *int            `json:"horizonDay"`
	Batches               []batchDTO      `json:"batches"`
}

func (req shelfLifeRequest) validate() map[string]string {
	f := map[string]string{}
	if req.DemandDay < 0 || req.DemandDay > maxCalcDays {
		f["demandDay"] = "must be 0 to 3650"
	}
	if req.DemandQty.IsNegative() {
		f["demandQty"] = "must not be negative"
	}
	if req.RequiredShelfLifeDays < 0 || req.RequiredShelfLifeDays > maxCalcDays {
		f["requiredShelfLifeDays"] = "must be 0 to 3650"
	}
	if req.HorizonDay != nil && (*req.HorizonDay < 0 || *req.HorizonDay > maxCalcDays) {
		f["horizonDay"] = "must be 0 to 3650"
	}
	if len(req.Batches) > maxCalcBatches {
		f["batches"] = "must hold at most 1000 batches"
	}
	seen := map[string]bool{}
	for i, b := range req.Batches {
		prefix := "batches[" + strconv.Itoa(i) + "]."
		if b.ID == "" || len(b.ID) > 64 || seen[b.ID] {
			f[prefix+"id"] = "must be unique and 1 to 64 characters"
		}
		seen[b.ID] = true
		if b.Qty.IsNegative() {
			f[prefix+"qty"] = "must not be negative"
		}
		if b.ExpiryDay < -maxCalcDays || b.ExpiryDay > maxCalcDays {
			f[prefix+"expiryDay"] = "must be -3650 to 3650"
		}
	}
	return f
}

// allocateShelfLife answers POST /v1/planning/shelf-life:allocate (FR-22.20, BR-22.15).
func (a *API) allocateShelfLife(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req shelfLifeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	batches := make([]domain.Batch, 0, len(req.Batches))
	for _, b := range req.Batches {
		batches = append(batches, domain.Batch{ID: b.ID, Qty: b.Qty, ExpiryDay: b.ExpiryDay})
	}
	allocs, short := domain.AllocateBatches(batches, req.DemandQty, req.DemandDay, req.RequiredShelfLifeDays)
	lines := make([]map[string]string, 0, len(allocs))
	for _, al := range allocs {
		lines = append(lines, map[string]string{"batchId": al.BatchID, "qty": al.Qty.String()})
	}
	expiring := []map[string]any{}
	if req.HorizonDay != nil {
		for _, b := range domain.UnpeggedExpiring(batches, allocs, *req.HorizonDay) {
			expiring = append(expiring, map[string]any{"batchId": b.ID, "qty": b.Qty.String(), "expiryDay": b.ExpiryDay})
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"allocations": lines, "shortfallQty": short.String(), "expiringUnpegged": expiring,
	})
}

// registerSupplyCalculators mounts the supply schedule and shelf-life calculators.
func (a *API) registerSupplyCalculators(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/planning/supply-schedule:project", a.projectSupply)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/planning/shelf-life:allocate", a.allocateShelfLife)
}
