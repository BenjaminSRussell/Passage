package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// Stateless planning calculators (FR-22.16 DDMRP, FR-22.17 quota, FR-22.19
// strategy consumption). They read the request and return the result; nothing
// is stored, so they take no Idempotency-Key and write no outbox rows.

const (
	maxCalcDays       = 3650
	maxCalcBuckets    = 1000
	maxCalcSales      = 10000
	maxCalcHistory    = 100000
	maxQuotaSources   = 100
	codeNoUsableSrc   = "PLAN-0009"
	defaultPastDays   = 90
	defaultBackward   = 30
	defaultSpikePct   = 50
	maxSpikeThreshold = 500
)

// dayQtyDTO is one dated quantity, relative to day 0 of the calculation.
type dayQtyDTO struct {
	Day int             `json:"day"`
	Qty decimal.Decimal `json:"qty"`
}

type bufferProfileDTO struct {
	LeadTimeFactor    decimal.Decimal  `json:"leadTimeFactor"`
	VariabilityFactor decimal.Decimal  `json:"variabilityFactor"`
	OrderCycleDays    int              `json:"orderCycleDays"`
	MinOrderQty       decimal.Decimal  `json:"minOrderQty"`
	SpikeThresholdPct *decimal.Decimal `json:"spikeThresholdPct"`
}

type bufferEvalRequest struct {
	Profile          bufferProfileDTO `json:"profile"`
	ADU              *decimal.Decimal `json:"adu"`
	History          []dayQtyDTO      `json:"history"`
	PastDays         *int             `json:"pastDays"`
	FutureDays       *int             `json:"futureDays"`
	DAF              *decimal.Decimal `json:"daf"`
	DLTDays          decimal.Decimal  `json:"dltDays"`
	OnHand           decimal.Decimal  `json:"onHand"`
	OpenSupply       decimal.Decimal  `json:"openSupply"`
	Demand           []dayQtyDTO      `json:"demand"`
	SpikeHorizonDays *int             `json:"spikeHorizonDays"`
	OrderMultiple    decimal.Decimal  `json:"orderMultiple"`
}

func (req bufferEvalRequest) validate() map[string]string {
	f := map[string]string{}
	p := req.Profile
	nonNeg := func(name string, d decimal.Decimal) {
		if d.IsNegative() {
			f[name] = "must not be negative"
		}
	}
	nonNeg("profile.leadTimeFactor", p.LeadTimeFactor)
	nonNeg("profile.variabilityFactor", p.VariabilityFactor)
	nonNeg("profile.minOrderQty", p.MinOrderQty)
	if p.OrderCycleDays < 0 || p.OrderCycleDays > maxCalcDays {
		f["profile.orderCycleDays"] = "must be 0 to 3650"
	}
	if p.SpikeThresholdPct != nil && (p.SpikeThresholdPct.IsNegative() || p.SpikeThresholdPct.GreaterThan(decimal.NewFromInt(maxSpikeThreshold))) {
		f["profile.spikeThresholdPct"] = "must be 0 to 500"
	}
	if req.ADU != nil {
		nonNeg("adu", *req.ADU)
	} else if len(req.History) == 0 {
		f["adu"] = "send adu or a history to derive it from"
	}
	if req.PastDays != nil && (*req.PastDays < 0 || *req.PastDays > maxCalcDays) {
		f["pastDays"] = "must be 0 to 3650"
	}
	if req.FutureDays != nil && (*req.FutureDays < 0 || *req.FutureDays > maxCalcDays) {
		f["futureDays"] = "must be 0 to 3650"
	}
	if req.DAF != nil {
		nonNeg("daf", *req.DAF)
	}
	nonNeg("dltDays", req.DLTDays)
	if req.DLTDays.GreaterThan(decimal.NewFromInt(maxCalcDays)) {
		f["dltDays"] = "must be 0 to 3650"
	}
	nonNeg("openSupply", req.OpenSupply)
	nonNeg("orderMultiple", req.OrderMultiple)
	if req.SpikeHorizonDays != nil && (*req.SpikeHorizonDays < 0 || *req.SpikeHorizonDays > maxCalcDays) {
		f["spikeHorizonDays"] = "must be 0 to 3650"
	}
	if len(req.History) > maxCalcHistory {
		f["history"] = "must hold at most 100000 days"
	}
	if len(req.Demand) > maxCalcSales {
		f["demand"] = "must hold at most 10000 days"
	}
	return f
}

type zonesDTO struct {
	RedBase     string `json:"redBase"`
	RedSafety   string `json:"redSafety"`
	Yellow      string `json:"yellow"`
	Green       string `json:"green"`
	TopOfRed    string `json:"topOfRed"`
	TopOfYellow string `json:"topOfYellow"`
	TopOfGreen  string `json:"topOfGreen"`
}

func toZonesDTO(z domain.BufferZones) zonesDTO {
	return zonesDTO{RedBase: z.RedBase.String(), RedSafety: z.RedSafety.String(), Yellow: z.Yellow.String(), Green: z.Green.String(),
		TopOfRed: z.TopOfRed.String(), TopOfYellow: z.TopOfYellow.String(), TopOfGreen: z.TopOfGreen.String()}
}

// bufferResult is one evaluated buffer: the ADU and adjustment factor used, and the status.
type bufferResult struct {
	ADU    decimal.Decimal
	DAF    decimal.Decimal
	Status domain.BufferStatus
}

// evaluateBufferInput validates and evaluates one buffer request (FR-22.16). Field
// problems come back as a map; a window or parameter problem as an error. The
// stored recalculation shares this path with the stateless evaluate endpoint.
func evaluateBufferInput(req bufferEvalRequest) (bufferResult, map[string]string, error) {
	if fields := req.validate(); len(fields) > 0 {
		return bufferResult{}, fields, nil
	}
	pastDays, futureDays := defaultPastDays, 0
	if req.PastDays != nil {
		pastDays = *req.PastDays
	}
	if req.FutureDays != nil {
		futureDays = *req.FutureDays
	}
	history := toDayQty(req.History)
	adu := decimal.Zero
	if req.ADU != nil {
		adu = *req.ADU
	} else {
		var err error
		if adu, err = domain.AverageDailyUsage(history, pastDays, futureDays); err != nil {
			return bufferResult{}, nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeInput, "the ADU window is empty")
		}
	}
	daf := decimal.NewFromInt(1)
	if req.DAF != nil {
		daf = *req.DAF
	}
	horizon := domain.DLTDays(req.DLTDays)
	if req.SpikeHorizonDays != nil {
		horizon = *req.SpikeHorizonDays
	}
	threshold := decimal.NewFromInt(defaultSpikePct)
	if req.Profile.SpikeThresholdPct != nil {
		threshold = *req.Profile.SpikeThresholdPct
	}
	st, err := domain.EvaluateBuffer(domain.BufferInput{
		Profile: domain.BufferProfile{
			LeadTimeFactor: req.Profile.LeadTimeFactor, VariabilityFactor: req.Profile.VariabilityFactor,
			OrderCycleDays: req.Profile.OrderCycleDays, MinOrderQty: req.Profile.MinOrderQty, SpikeThresholdPct: threshold,
		},
		ADU: adu, DLT: req.DLTDays, DAF: daf, OnHand: req.OnHand, OpenSupply: req.OpenSupply,
		Demand: toDayQty(req.Demand), SpikeHorizonDays: horizon, OrderMultiple: req.OrderMultiple,
	})
	if err != nil {
		return bufferResult{}, nil, httpx.NewProblem(http.StatusUnprocessableEntity, codeInput, "buffer parameters are inconsistent")
	}
	return bufferResult{ADU: adu, DAF: daf, Status: st}, nil, nil
}

// evaluateBuffer answers POST /v1/planning/ddmrp/buffers:evaluate.
func (a *API) evaluateBuffer(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req bufferEvalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, fields, err := evaluateBufferInput(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	st := res.Status
	httpx.JSON(w, http.StatusOK, map[string]any{
		"adu": res.ADU.String(), "adjustedAdu": st.AdjustedADU.String(), "zones": toZonesDTO(st.Zones),
		"qualifiedDemand": st.QualifiedDemand.String(), "netFlow": st.NetFlow.String(),
		"plannedQty": st.PlannedQty.String(), "priority": st.Priority.String(), "zone": st.Zone,
	})
}

func toDayQty(in []dayQtyDTO) []domain.DayDemand {
	out := make([]domain.DayDemand, 0, len(in))
	for _, d := range in {
		out = append(out, domain.DayDemand{Day: d.Day, Qty: d.Qty})
	}
	return out
}

type quotaSourceDTO struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Quota        decimal.Decimal `json:"quota"`
	BaseQty      decimal.Decimal `json:"baseQty"`
	AllocatedQty decimal.Decimal `json:"allocatedQty"`
	Priority     int             `json:"priority"`
	Blocked      bool            `json:"blocked"`
}

type quotaAllocateRequest struct {
	Sources     []quotaSourceDTO `json:"sources"`
	Qty         decimal.Decimal  `json:"qty"`
	Split       bool             `json:"split"`
	MinSplitQty *decimal.Decimal `json:"minSplitQty"`
}

var quotaSourceTypes = map[string]bool{domain.SourceVendor: true, domain.SourceProduction: true, domain.SourceTransfer: true}

func (req quotaAllocateRequest) validate() map[string]string {
	f := map[string]string{}
	if n := len(req.Sources); n == 0 || n > maxQuotaSources {
		f["sources"] = "must hold 1 to 100 sources"
	}
	seen := map[string]bool{}
	for i, s := range req.Sources {
		prefix := "sources[" + strconv.Itoa(i) + "]."
		if s.ID == "" || len(s.ID) > 64 || seen[s.ID] {
			f[prefix+"id"] = "must be unique and 1 to 64 characters"
		}
		seen[s.ID] = true
		if !quotaSourceTypes[s.Type] {
			f[prefix+"type"] = "must be vendor, production or transfer"
		}
		if s.Quota.IsNegative() || s.BaseQty.IsNegative() || s.AllocatedQty.IsNegative() {
			f[prefix+"quota"] = "quota, baseQty and allocatedQty must not be negative"
		}
	}
	if !req.Qty.IsPositive() {
		f["qty"] = "must be positive"
	}
	if req.MinSplitQty != nil && req.MinSplitQty.IsNegative() {
		f["minSplitQty"] = "must not be negative"
	}
	return f
}

// allocateQuota answers POST /v1/planning/quota-arrangements:allocate (BR-22.12).
func (a *API) allocateQuota(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req quotaAllocateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	sources := make([]domain.QuotaSource, 0, len(req.Sources))
	for _, s := range req.Sources {
		sources = append(sources, domain.QuotaSource{ID: s.ID, Type: s.Type, Quota: s.Quota, BaseQty: s.BaseQty,
			Allocated: s.AllocatedQty, Priority: s.Priority, Blocked: s.Blocked})
	}
	minSplit := decimal.Zero
	if req.MinSplitQty != nil {
		minSplit = *req.MinSplitQty
	}
	got, err := domain.Allocate(sources, req.Qty, req.Split, minSplit)
	if err != nil {
		if errors.Is(err, domain.ErrNoUsableSource) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeNoUsableSrc, "the quota arrangement has no usable source for this quantity"))
			return
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeInput, "quota source or quantity is invalid"))
		return
	}
	lines := make([]map[string]string, 0, len(got))
	for _, g := range got {
		lines = append(lines, map[string]string{"sourceId": g.SourceID, "qty": g.Qty.String(), "ratingAfter": g.RatingAfter.String()})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"assignments": lines, "qty": req.Qty.String()})
}

type bucketDTO struct {
	ID        string          `json:"id"`
	StartDate string          `json:"startDate"`
	EndDate   string          `json:"endDate"`
	Qty       decimal.Decimal `json:"qty"`
}

type salesDTO struct {
	ID   string          `json:"id"`
	Date string          `json:"date"`
	Qty  decimal.Decimal `json:"qty"`
}

type consumeRequest struct {
	Mode         string      `json:"mode"`
	BackwardDays *int        `json:"backwardDays"`
	ForwardDays  *int        `json:"forwardDays"`
	Forecast     []bucketDTO `json:"forecast"`
	Sales        []salesDTO  `json:"sales"`
}

func (req consumeRequest) validate() map[string]string {
	f := map[string]string{}
	switch req.Mode {
	case domain.ConsumptionBackward, domain.ConsumptionForward, domain.ConsumptionBackwardForward:
	default:
		f["mode"] = "must be backward, forward or backward-forward"
	}
	if req.BackwardDays != nil && (*req.BackwardDays < 0 || *req.BackwardDays > maxCalcDays) {
		f["backwardDays"] = "must be 0 to 3650"
	}
	if req.ForwardDays != nil && (*req.ForwardDays < 0 || *req.ForwardDays > maxCalcDays) {
		f["forwardDays"] = "must be 0 to 3650"
	}
	if len(req.Forecast) > maxCalcBuckets {
		f["forecast"] = "must hold at most 1000 periods"
	}
	if len(req.Sales) > maxCalcSales {
		f["sales"] = "must hold at most 10000 lines"
	}
	for i, b := range req.Forecast {
		prefix := "forecast[" + strconv.Itoa(i) + "]."
		start, errS := time.Parse(dateLayout, b.StartDate)
		end, errE := time.Parse(dateLayout, b.EndDate)
		if errS != nil {
			f[prefix+"startDate"] = "must be a date as YYYY-MM-DD"
		}
		if errE != nil {
			f[prefix+"endDate"] = "must be a date as YYYY-MM-DD"
		}
		if errS == nil && errE == nil && end.Before(start) {
			f[prefix+"endDate"] = "must not be before startDate"
		}
		if b.Qty.IsNegative() {
			f[prefix+"qty"] = "must not be negative"
		}
	}
	for i, s := range req.Sales {
		prefix := "sales[" + strconv.Itoa(i) + "]."
		if _, err := time.Parse(dateLayout, s.Date); err != nil {
			f[prefix+"date"] = "must be a date as YYYY-MM-DD"
		}
		if s.Qty.IsNegative() {
			f[prefix+"qty"] = "must not be negative"
		}
	}
	return f
}

// epochDay maps a UTC calendar date to a day index (days since 1970-01-01).
func epochDay(t time.Time) int { return int(t.Unix() / 86400) }

// consumeForecast answers POST /v1/planning/strategies:consume-forecast (BR-22.13).
func (a *API) consumeForecast(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req consumeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	back, fwd := defaultBackward, 0
	if req.BackwardDays != nil {
		back = *req.BackwardDays
	}
	if req.ForwardDays != nil {
		fwd = *req.ForwardDays
	}
	buckets := make([]domain.ForecastBucket, 0, len(req.Forecast))
	for _, b := range req.Forecast {
		start, _ := time.Parse(dateLayout, b.StartDate)
		end, _ := time.Parse(dateLayout, b.EndDate)
		buckets = append(buckets, domain.ForecastBucket{ID: b.ID, StartDay: epochDay(start), EndDay: epochDay(end), Qty: b.Qty})
	}
	sales := make([]domain.SalesLine, 0, len(req.Sales))
	for _, s := range req.Sales {
		d, _ := time.Parse(dateLayout, s.Date)
		sales = append(sales, domain.SalesLine{ID: s.ID, Day: epochDay(d), Qty: s.Qty})
	}
	res, err := domain.ConsumeForecast(req.Mode, back, fwd, buckets, sales)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeInput, "consumption setup is invalid"))
		return
	}
	fc := make([]map[string]string, 0, len(res.Forecast))
	for i, rem := range res.Forecast {
		fc = append(fc, map[string]string{"id": buckets[i].ID, "remainingQty": rem.String()})
	}
	sl := make([]map[string]string, 0, len(res.Sales))
	for _, s := range res.Sales {
		sl = append(sl, map[string]string{"id": s.ID, "consumedQty": s.Consumed.String(), "unconsumedQty": s.Unconsumed.String()})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"forecast": fc, "sales": sl,
		"grossQty": domain.GrossQty(res, sales).String()})
}

// registerCalculators mounts the stateless calculators on the router.
func (a *API) registerCalculators(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/planning/ddmrp/buffers:evaluate", a.evaluateBuffer)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/planning/quota-arrangements:allocate", a.allocateQuota)
	r.With(authz.RequireDuty(dutyRead)).Post("/v1/planning/strategies:consume-forecast", a.consumeForecast)
	a.registerSupplyCalculators(r)
}
