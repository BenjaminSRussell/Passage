package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Report routes (PRD 3.16.14), all read-only and gated on the inquiry duty.
const (
	countVariancePath = "/v1/inventory/reports/count-variance"
	negativePath      = "/v1/inventory/reports/negative-onhand"
	tracePath         = "/v1/inventory/reports/tracking-trace"
	combinationsPath  = "/v1/inventory/reports/dimension-combinations"
)

func (a *API) registerReports(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(countVariancePath, a.countVarianceReport)
	r.With(authz.RequireDuty(dutyRead)).Get(negativePath, a.negativeOnHandReport)
	r.With(authz.RequireDuty(dutyRead)).Get(tracePath, a.trackingTrace)
	r.With(authz.RequireDuty(dutyRead)).Get(combinationsPath, a.dimensionCombinations)
}

// optionalUUID reads a query parameter that must be a UUID when present.
func optionalUUID(q map[string][]string, name string, fields map[string]string) string {
	v := ""
	if vs := q[name]; len(vs) > 0 {
		v = strings.TrimSpace(vs[0])
	}
	if v != "" && !ids.Valid(v) {
		fields[name] = "must be a UUID"
	}
	return v
}

func queryText(r *http.Request, name string) string {
	return strings.TrimSpace(r.URL.Query().Get(name))
}

type countVarianceLine struct {
	SheetID       string            `json:"sheetId"`
	SheetNumber   string            `json:"sheetNumber"`
	SheetStatus   string            `json:"sheetStatus"`
	PlanID        string            `json:"planId"`
	PlanName      string            `json:"planName"`
	Warehouse     string            `json:"warehouseId,omitempty"`
	LegalEntityID string            `json:"legalEntityId"`
	LineID        string            `json:"lineId"`
	LineNo        int               `json:"lineNo"`
	ProductID     string            `json:"productId"`
	Dimensions    map[string]string `json:"dimensions"`
	Unit          string            `json:"unit"`
	Currency      string            `json:"currency"`
	Expected      string            `json:"expectedQty"`
	Counted       string            `json:"countedQty"`
	VarianceQty   string            `json:"varianceQty"`
	UnitCost      string            `json:"unitCost"`
	VarianceValue string            `json:"varianceValue"`
	ValueBasis    string            `json:"valueBasis"`
	CountedBy     string            `json:"countedBy,omitempty"`
	CountedAt     time.Time         `json:"countedAt"`
}

type counterTotals struct {
	Counter       string `json:"counter"`
	Currency      string `json:"currency"`
	Lines         int    `json:"lines"`
	AbsVarianceQt string `json:"absVarianceQty"`
	GainValue     string `json:"gainValue"`
	LossValue     string `json:"lossValue"`
	NetValue      string `json:"netValue"`
}

func (a *API) countVarianceReport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.CountVarianceFilter{LegalEntityID: optionalUUID(q, "legalEntityId", fields), PlanID: optionalUUID(q, "planId", fields),
		SheetID: optionalUUID(q, "sheetId", fields), Warehouse: queryText(r, "warehouseId"), Counter: queryText(r, "counter")}
	f.From, f.To = parseDateRange(q.Get("from"), q.Get("to"), fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	rows, truncated, err := a.store.ListCountVariance(r.Context(), f)
	if err != nil {
		a.failed(w, r, "count variance report", err)
		return
	}
	items := make([]countVarianceLine, 0, len(rows))
	byCurrency := map[string]struct {
		counters []string
		qty, val []decimal.Decimal
	}{}
	order := []string{}
	for _, row := range rows {
		variance := domain.CountVarianceQty(row.Expected, row.Counted, row.PostedVariance)
		value := variance.Mul(row.UnitCost).Round(6)
		items = append(items, countVarianceLine{SheetID: row.SheetID, SheetNumber: row.SheetNumber, SheetStatus: row.SheetStatus, PlanID: row.PlanID,
			PlanName: row.PlanName, Warehouse: row.Warehouse, LegalEntityID: row.LegalEntityID, LineID: row.LineID, LineNo: row.LineNo,
			ProductID: row.ProductID, Dimensions: row.Dims, Unit: row.Unit, Currency: row.Currency, Expected: row.Expected.String(),
			Counted: row.Counted.String(), VarianceQty: variance.String(), UnitCost: row.UnitCost.String(), VarianceValue: value.String(),
			ValueBasis: row.ValueBasis, CountedBy: row.CountedBy, CountedAt: row.CountedAt})
		g, seen := byCurrency[row.Currency]
		if !seen {
			order = append(order, row.Currency)
		}
		g.counters, g.qty, g.val = append(g.counters, row.CountedBy), append(g.qty, variance), append(g.val, value)
		byCurrency[row.Currency] = g
	}
	totals := []counterTotals{}
	for _, cur := range order {
		g := byCurrency[cur]
		for _, t := range domain.SummarizeVarianceByCounter(g.counters, g.qty, g.val) {
			totals = append(totals, counterTotals{Counter: t.Counter, Currency: cur, Lines: t.Lines, AbsVarianceQt: t.AbsVarianceQt.String(),
				GainValue: t.GainValue.String(), LossValue: t.LossValue.String(), NetValue: t.NetValue.String()})
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "totalsByCounter": totals, "truncated": truncated})
}

// parseDateRange reads inclusive from / to dates, recording errors in fields.
func parseDateRange(from, to string, fields map[string]string) (*time.Time, *time.Time) {
	var out [2]*time.Time
	for i, c := range []struct{ name, raw string }{{"from", from}, {"to", to}} {
		if c.raw == "" {
			continue
		}
		d, err := time.Parse(dateFormat, c.raw)
		if err != nil {
			fields[c.name] = "must be a date (YYYY-MM-DD)"
			continue
		}
		out[i] = &d
	}
	if out[0] != nil && out[1] != nil && out[1].Before(*out[0]) {
		fields["to"] = "must not be before from"
	}
	return out[0], out[1]
}

type negativeOnHandLine struct {
	LegalEntityID  string            `json:"legalEntityId"`
	ProductID      string            `json:"productId"`
	Dimensions     map[string]string `json:"dimensions"`
	PhysicalInvent string            `json:"physicalInvent"`
	ShortfallQty   string            `json:"shortfallQty"`
	PostedValue    string            `json:"postedValue"`
	Currency       string            `json:"currency,omitempty"`
	FirstNegative  string            `json:"firstNegativeDate,omitempty"`
}

func (a *API) negativeOnHandReport(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.NegativeOnHandFilter{LegalEntityID: optionalUUID(q, "legalEntityId", fields), ProductID: optionalUUID(q, "productId", fields),
		SiteID: queryText(r, "siteId"), WarehouseID: queryText(r, "warehouseId")}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	rows, truncated, err := a.store.ListNegativeOnHand(r.Context(), f)
	if err != nil {
		a.failed(w, r, "negative on-hand list", err)
		return
	}
	items := make([]negativeOnHandLine, 0, len(rows))
	for _, row := range rows {
		line := negativeOnHandLine{LegalEntityID: row.LegalEntityID, ProductID: row.ProductID, Dimensions: row.Dims,
			PhysicalInvent: row.PhysicalInvent.String(), ShortfallQty: row.PhysicalInvent.Neg().String(), PostedValue: row.PostedValue.String(), Currency: row.Currency}
		if row.FirstNegative != nil {
			line.FirstNegative = row.FirstNegative.Format(dateFormat)
		}
		items = append(items, line)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

type traceLine struct {
	transactionResponse
	// Running is the physical balance of the traced stock for the legal entity and product after this
	// movement; absent for movements that do not change physical stock (ordered or reserved quantities).
	Running *string `json:"running,omitempty"`
}

func (a *API) trackingTrace(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.TraceFilter{LegalEntityID: optionalUUID(q, "legalEntityId", fields), ProductID: optionalUUID(q, "productId", fields),
		Batch: queryText(r, "batchId"), Serial: queryText(r, "serialId"), Direction: strings.ToUpper(queryText(r, "direction"))}
	if f.Direction == "" {
		f.Direction = domain.TraceBoth
	}
	if !domain.ValidTraceDirection(f.Direction) {
		fields["direction"] = "must be both, forward or backward"
	}
	if f.Batch == "" && f.Serial == "" {
		fields["batchId"] = "give a batch or a serial to trace"
		fields["serialId"] = "give a batch or a serial to trace"
	}
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, domain.CodeTraceKey, validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	rows, truncated, err := a.store.TraceTracking(r.Context(), f)
	if err != nil {
		a.failed(w, r, "tracking trace", err)
		return
	}
	balance := map[string]decimal.Decimal{}
	items := make([]traceLine, 0, len(rows))
	for _, row := range rows {
		line := traceLine{transactionResponse: toTransactionResponse(row)}
		if f.Direction == domain.TraceBoth && domain.MovesPhysicalStock(row.IssueStatus, row.ReceiptStatus) {
			key := row.LegalEntityID + "|" + row.ProductID
			b := balance[key].Add(row.Qty)
			balance[key] = b
			s := b.String()
			line.Running = &s
		}
		items = append(items, line)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "direction": f.Direction, "truncated": truncated})
}

type combinationLine struct {
	LegalEntityID    string            `json:"legalEntityId"`
	ProductID        string            `json:"productId"`
	DimensionSetHash string            `json:"dimensionSetHash"`
	Dimensions       map[string]string `json:"dimensions"`
	ActiveDimensions []string          `json:"activeDimensions"`
	FirstUse         string            `json:"firstUse"`
	LastUse          string            `json:"lastUse"`
	TransactionCount int               `json:"transactionCount"`
}

func (a *API) dimensionCombinations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.DimensionCombinationFilter{LegalEntityID: optionalUUID(q, "legalEntityId", fields), ProductID: optionalUUID(q, "productId", fields)}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	rows, truncated, err := a.store.ListDimensionCombinations(r.Context(), f)
	if err != nil {
		a.failed(w, r, "dimension combinations", err)
		return
	}
	items := make([]combinationLine, 0, len(rows))
	for _, row := range rows {
		active := []string{}
		for _, n := range domain.DimensionNames() {
			if row.Dims[n] != "" {
				active = append(active, n)
			}
		}
		items = append(items, combinationLine{LegalEntityID: row.LegalEntityID, ProductID: row.ProductID, DimensionSetHash: row.Hash,
			Dimensions: row.Dims, ActiveDimensions: active, FirstUse: row.FirstUse.Format(dateFormat), LastUse: row.LastUse.Format(dateFormat),
			TransactionCount: row.TransactionCount})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}
