package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

var reportDims = domain.Dimensions{domain.DimSite: "S1", domain.DimWarehouse: "W1", domain.DimBatch: "B1"}

func (f *fakeStore) ListCountVariance(_ context.Context, flt store.CountVarianceFilter) ([]store.CountVarianceRow, bool, error) {
	f.variance = flt
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	posted := decimal.RequireFromString("-4")
	base := store.CountVarianceRow{SheetID: ids.New(), SheetNumber: "CNT-1", PlanID: ids.New(), PlanName: "Aisle", LegalEntityID: ids.New(),
		ProductID: ids.New(), Dims: reportDims, Unit: "EA", Currency: "USD", CountedAt: at}
	posting := base
	posting.SheetStatus, posting.LineNo, posting.CountedBy, posting.ValueBasis = "POSTED", 1, "ann", domain.ValueBasisJournal
	posting.Expected, posting.Counted, posting.PostedVariance, posting.UnitCost = decimal.NewFromInt(50), decimal.NewFromInt(46), &posted, decimal.NewFromInt(2)
	open := base
	open.SheetStatus, open.LineNo, open.CountedBy, open.ValueBasis = "COUNTED", 2, "ann", domain.ValueBasisEstimate
	open.Expected, open.Counted, open.UnitCost = decimal.NewFromInt(10), decimal.NewFromInt(12), decimal.RequireFromString("1.5")
	return []store.CountVarianceRow{posting, open}, false, nil
}

func (f *fakeStore) ListNegativeOnHand(_ context.Context, flt store.NegativeOnHandFilter) ([]store.NegativeOnHandRow, bool, error) {
	f.negative = flt
	d := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	return []store.NegativeOnHandRow{{LegalEntityID: ids.New(), ProductID: ids.New(), Dims: reportDims, PhysicalInvent: decimal.NewFromInt(-3),
		PostedValue: decimal.NewFromInt(-6), Currency: "USD", FirstNegative: &d}, {LegalEntityID: ids.New(), ProductID: ids.New(), Dims: reportDims,
		PhysicalInvent: decimal.NewFromInt(-1)}}, false, nil
}

func (f *fakeStore) TraceTracking(_ context.Context, flt store.TraceFilter) ([]store.TransactionRow, bool, error) {
	f.trace = flt
	p := ids.New()
	mk := func(qty int64, issue, receipt string) store.TransactionRow {
		return store.TransactionRow{ID: ids.New(), ProductID: p, Dims: reportDims, Qty: decimal.NewFromInt(qty), EnteredQty: decimal.NewFromInt(qty),
			EnteredUnit: "EA", IssueStatus: issue, ReceiptStatus: receipt, PhysicalDate: "2026-09-01", Currency: "USD", SourceType: "INVENT_JOURNAL_LINE"}
	}
	return []store.TransactionRow{mk(10, "", "PURCHASED"), mk(-4, "SOLD", ""), mk(-2, "ON_ORDER", "")}, false, nil
}

func (f *fakeStore) ListDimensionCombinations(_ context.Context, flt store.DimensionCombinationFilter) ([]store.DimensionCombinationRow, bool, error) {
	f.combos = flt
	d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return []store.DimensionCombinationRow{{LegalEntityID: ids.New(), ProductID: ids.New(), Hash: "ab", Dims: reportDims, FirstUse: d, LastUse: d.AddDate(0, 0, 5), TransactionCount: 3}}, false, nil
}

func TestCountVarianceReportValuesAndTotalsPerCounter(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	plan := ids.New()
	rec := do(t, h, http.MethodGet, "/v1/inventory/reports/count-variance?planId="+plan+"&counter=ann&warehouseId=W1&from=2026-09-01&to=2026-09-30", "", []string{"inventory.read"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.variance.PlanID != plan || fs.variance.Counter != "ann" || fs.variance.Warehouse != "W1" || fs.variance.From == nil || fs.variance.To == nil {
		t.Fatalf("filter %+v", fs.variance)
	}
	var out struct {
		Items []struct {
			VarianceQty, VarianceValue, ValueBasis string
		} `json:"items"`
		Totals []struct {
			Counter, Currency, NetValue, GainValue, LossValue, AbsVarianceQty string
			Lines                                                             int
		} `json:"totalsByCounter"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// Posted line: -4 at the journal cost 2 = -8; open line: 12 - 10 = +2 at 1.5 = +3.
	if len(out.Items) != 2 || out.Items[0].VarianceQty != "-4" || out.Items[0].VarianceValue != "-8" || out.Items[1].VarianceQty != "2" ||
		out.Items[1].VarianceValue != "3" || out.Items[1].ValueBasis != domain.ValueBasisEstimate {
		t.Fatalf("items %+v", out.Items)
	}
	if len(out.Totals) != 1 || out.Totals[0].Counter != "ann" || out.Totals[0].Lines != 2 || out.Totals[0].NetValue != "-5" ||
		out.Totals[0].GainValue != "3" || out.Totals[0].LossValue != "8" || out.Totals[0].AbsVarianceQty != "6" || out.Totals[0].Currency != "USD" {
		t.Fatalf("totals %+v", out.Totals)
	}
}

func TestReportsRejectBadInputAndNeedTheReadDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	read := []string{"inventory.read"}
	for _, path := range []string{
		"/v1/inventory/reports/count-variance?planId=x&sheetId=y&legalEntityId=z&from=2026-13-01",
		"/v1/inventory/reports/count-variance?from=2026-09-10&to=2026-09-01",
		"/v1/inventory/reports/negative-onhand?productId=nope&legalEntityId=nope",
		"/v1/inventory/reports/tracking-trace",
		"/v1/inventory/reports/tracking-trace?batchId=B1&direction=sideways",
		"/v1/inventory/reports/dimension-combinations?productId=nope",
	} {
		if rec := do(t, h, http.MethodGet, path, "", read, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", path, rec.Code, rec.Body.String())
		}
		if rec := do(t, h, http.MethodGet, path, "", []string{"inventory.transfer.write"}, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", path, rec.Code)
		}
	}
}

func TestNegativeOnHandReportShowsShortfall(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/inventory/reports/negative-onhand?siteId=S1&warehouseId=W1", "", []string{"inventory.read"}, nil)
	var out struct {
		Items []struct {
			ShortfallQty, PhysicalInvent string
			FirstNegative                string `json:"firstNegativeDate"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 2 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if out.Items[0].ShortfallQty != "3" || out.Items[0].PhysicalInvent != "-3" || out.Items[0].FirstNegative != "2026-09-02" || out.Items[1].FirstNegative != "" {
		t.Fatalf("items %+v", out.Items)
	}
	if fs.negative.SiteID != "S1" || fs.negative.WarehouseID != "W1" {
		t.Fatalf("filter %+v", fs.negative)
	}
}

func TestTrackingTraceRunsTheBalanceOverPhysicalMovesOnly(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	rec := do(t, h, http.MethodGet, "/v1/inventory/reports/tracking-trace?batchId=B1", "", []string{"inventory.read"}, nil)
	var out struct {
		Direction string `json:"direction"`
		Items     []struct {
			Qty     string  `json:"qty"`
			Running *string `json:"running"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 3 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if fs.trace.Batch != "B1" || fs.trace.Direction != domain.TraceBoth || out.Direction != "BOTH" {
		t.Fatalf("filter %+v", fs.trace)
	}
	if out.Items[0].Running == nil || *out.Items[0].Running != "10" || out.Items[1].Running == nil || *out.Items[1].Running != "6" || out.Items[2].Running != nil {
		t.Fatalf("running %+v", out.Items)
	}
	rec = do(t, h, http.MethodGet, "/v1/inventory/reports/tracking-trace?serialId=SN1&direction=forward", "", []string{"inventory.read"}, nil)
	out.Items = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || fs.trace.Serial != "SN1" || fs.trace.Direction != domain.TraceForward {
		t.Fatalf("forward: status %d filter %+v", rec.Code, fs.trace)
	}
	if out.Items[0].Running != nil {
		t.Fatal("a one-direction trace must not carry a running balance")
	}
}

func TestDimensionCombinationsListActiveDimensions(t *testing.T) {
	h := newServer(t, &fakeStore{})
	rec := do(t, h, http.MethodGet, "/v1/inventory/reports/dimension-combinations", "", []string{"inventory.read"}, nil)
	var out struct {
		Items []struct {
			ActiveDimensions []string `json:"activeDimensions"`
			FirstUse         string   `json:"firstUse"`
			LastUse          string   `json:"lastUse"`
			TransactionCount int      `json:"transactionCount"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 1 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	got := out.Items[0]
	if len(got.ActiveDimensions) != 3 || got.FirstUse != "2026-09-01" || got.LastUse != "2026-09-06" || got.TransactionCount != 3 {
		t.Fatalf("item %+v", got)
	}
}
