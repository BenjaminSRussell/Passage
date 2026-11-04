package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func allocSpec(code, a, b string) domain.AllocationKeySpec {
	return domain.AllocationKeySpec{Code: code, Name: "Family " + code, ForecastUom: "CASE", Lines: []domain.AllocationLine{
		{ItemID: a, Share: decimal.RequireFromString("60"), UomFactor: decimal.NewFromInt(1)},
		{ItemID: b, Share: decimal.RequireFromString("40"), UomFactor: decimal.NewFromInt(6)},
	}}
}

// TestAllocationKeys covers key setup on the database: create with lines, an
// Idempotency-Key replay, a duplicate code, update under If-Match replacing the lines,
// listing without lines, and isolation between tenants.
func TestAllocationKeys(t *testing.T) {
	st, _, _ := setup(t)
	alice := asUser(1, "alice")
	a, b := ids.New(), ids.New()

	created, replayed, err := st.CreateAllocationKey(alice, store.AllocationKeyCreate{IdempotencyKey: "k1", InputHash: "h1", Spec: allocSpec("FAM-A", a, b)})
	if err != nil || replayed || created.Version != 1 || created.LineCount != 2 {
		t.Fatalf("create = %+v replayed %v err %v", created, replayed, err)
	}
	again, replayed, err := st.CreateAllocationKey(alice, store.AllocationKeyCreate{IdempotencyKey: "k1", InputHash: "h1", Spec: allocSpec("FAM-A", a, b)})
	if err != nil || !replayed || again.ID != created.ID || len(again.Spec.Lines) != 2 {
		t.Fatalf("replay = %+v replayed %v err %v", again, replayed, err)
	}
	if _, _, err := st.CreateAllocationKey(alice, store.AllocationKeyCreate{IdempotencyKey: "k1", InputHash: "other", Spec: allocSpec("FAM-A", a, b)}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: %v", err)
	}
	if _, _, err := st.CreateAllocationKey(alice, store.AllocationKeyCreate{Spec: allocSpec("FAM-A", a, b)}); problemCode(err) != store.CodeAllocationKeyExists {
		t.Fatalf("duplicate code: %v", err)
	}

	got, err := st.GetAllocationKey(alice, "FAM-A")
	if err != nil || got.Spec.ForecastUom != "CASE" || len(got.Spec.Lines) != 2 {
		t.Fatalf("get = %+v err %v", got, err)
	}
	for _, l := range got.Spec.Lines {
		if l.ItemID == b && (!l.Share.Equal(decimal.NewFromInt(40)) || !l.UomFactor.Equal(decimal.NewFromInt(6))) {
			t.Fatalf("line of b = %+v", l)
		}
	}
	list, err := st.ListAllocationKeys(alice, 10)
	if err != nil || len(list) != 1 || list[0].LineCount != 2 || len(list[0].Spec.Lines) != 0 {
		t.Fatalf("list = %+v err %v", list, err)
	}

	c := ids.New()
	next := allocSpec("FAM-A", a, c)
	next.Name = "Renamed"
	if _, err := st.UpdateAllocationKey(alice, 5, next); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	updated, err := st.UpdateAllocationKey(alice, 1, next)
	if err != nil || updated.Version != 2 {
		t.Fatalf("update = %+v err %v", updated, err)
	}
	got, _ = st.GetAllocationKey(alice, "FAM-A")
	seen := map[string]bool{}
	for _, l := range got.Spec.Lines {
		seen[l.ItemID] = true
	}
	if got.Spec.Name != "Renamed" || !seen[a] || !seen[c] || seen[b] || len(got.Spec.Lines) != 2 {
		t.Fatalf("lines not replaced: %+v", got)
	}
	if _, err := st.UpdateAllocationKey(alice, 1, allocSpec("NOPE", a, b)); problemCode(err) != store.CodeAllocationKeyNotFound {
		t.Fatalf("unknown key update: %v", err)
	}

	if _, err := st.GetAllocationKey(asUser(2, "alice"), "FAM-A"); problemCode(err) != store.CodeAllocationKeyNotFound {
		t.Fatalf("tenant 2 reads tenant 1's key: %v", err)
	}
	if other, err := st.ListAllocationKeys(asUser(2, "alice"), 10); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 list = %+v err %v", other, err)
	}
}

// TestAllocateForecast covers FR-22.8 and BR-22.10 on the database: an aggregate split
// by share and converted to the inventory unit becomes manual cells, a re-allocation
// replaces them but keeps the sales quantity, the reduced quantity follows, the model
// version and forecast.allocated.v1 move in one transaction, an unknown key creates no
// model, a submitted model is locked and an overflow writes nothing.
func TestAllocateForecast(t *testing.T) {
	st, admin, _ := setup(t)
	alice := asUser(1, "alice")
	a, b, site := ids.New(), ids.New(), ids.New()
	if _, _, err := st.CreateAllocationKey(alice, store.AllocationKeyCreate{Spec: allocSpec("FAM-A", a, b)}); err != nil {
		t.Fatal(err)
	}
	periods := []store.AllocationPeriod{
		{Start: "2026-12-01", End: "2026-12-31", Qty: decimal.RequireFromString("100")},
		{Start: "2027-01-01", End: "2027-01-31", Qty: decimal.RequireFromString("10.5")},
	}

	if _, err := st.AllocateForecast(alice, store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "NOPE", ExpectedVersion: 0, SiteID: site, Periods: periods}); problemCode(err) != store.CodeAllocationKeyNotFound {
		t.Fatalf("unknown key: %v", err)
	}
	if _, _, err := st.ListForecastLines(alice, "ALLOC", "", "", 1); problemCode(err) != store.CodeForecastNotFound {
		t.Fatalf("an unknown key must not create the model: %v", err)
	}

	out, err := st.AllocateForecast(alice, store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "FAM-A", ExpectedVersion: 0, SiteID: site, Periods: periods})
	if err != nil {
		t.Fatal(err)
	}
	if out.CellsWritten != 4 || out.Items != 2 || out.Model.Version != 1 || out.Key.Version != 1 {
		t.Fatalf("allocation = %+v", out)
	}
	read := func() map[string]store.ForecastLine {
		_, lines, err := st.ListForecastLines(alice, "ALLOC", "", "", 20)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]store.ForecastLine{}
		for _, l := range lines {
			m[l.ItemID+l.PeriodStart] = l
		}
		return m
	}
	got := read()
	// 100 cases: 60% = 60 of item a (factor 1), 40% = 40 of item b at 6 units a case = 240.
	// 10.5 cases: 6.3 and 4.2 cases, 25.2 units of b.
	want := map[string]string{a + "2026-12-01": "60.000000", b + "2026-12-01": "240.000000", a + "2027-01-01": "6.300000", b + "2027-01-01": "25.200000"}
	if len(got) != 4 {
		t.Fatalf("cells = %+v", got)
	}
	for k, qty := range want {
		if got[k].Qty != qty || got[k].Source != "manual" || got[k].ReducedQty != qty || got[k].SiteID != site {
			t.Errorf("cell %s = %+v, want qty %s", k, got[k], qty)
		}
	}

	// A planner records sales against item a in December; a re-allocation keeps it and the
	// reduction (transactions) follows the new quantity.
	if _, err := st.UpsertForecastLines(alice, "ALLOC", "", 1, nil, []store.ForecastCellInput{
		{ItemID: a, SiteID: site, PeriodStart: "2026-12-01", PeriodEnd: "2026-12-31", Source: "manual", Qty: decimal.NewFromInt(60), SalesQty: decimal.NewFromInt(25)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllocateForecast(alice, store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "FAM-A", ExpectedVersion: 1, SiteID: site, Periods: periods[:1]}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale model version: %v", err)
	}
	periods[0].Qty = decimal.RequireFromString("50")
	out, err = st.AllocateForecast(alice, store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "FAM-A", ExpectedVersion: 2, SiteID: site, Periods: periods[:1]})
	if err != nil || out.Model.Version != 3 {
		t.Fatalf("re-allocation = %+v err %v", out, err)
	}
	got = read()
	if l := got[a+"2026-12-01"]; l.Qty != "30.000000" || l.SalesQty != "25.000000" || l.ReducedQty != "5.000000" {
		t.Fatalf("a in December after re-allocation = %+v", l)
	}
	if l := got[a+"2027-01-01"]; l.Qty != "6.300000" {
		t.Fatalf("January was touched by an allocation of December: %+v", l)
	}

	var n int
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select count(*), max(payload::text) from outbox where type = $1`, planevents.ForecastAllocated).Scan(&n, &raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data planevents.ForecastAllocatedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	if n != 2 || evt.Data.ModelCode != "ALLOC" || evt.Data.KeyCode != "FAM-A" || evt.Data.Version != 3 || evt.Data.Items != 2 || evt.Data.Periods != 1 ||
		evt.Data.CellsWritten != 2 || evt.Data.PeriodFrom != "2026-12-01" || evt.Data.PeriodTo != "2026-12-31" || evt.Data.AllocatedBy != "alice" || evt.Data.SiteID != site {
		t.Fatalf("outbox has %d forecast.allocated events, last %+v", n, evt.Data)
	}

	// 999,999,999,999 cases at a factor of 6 does not fit the cell; nothing is written and the version stays.
	huge := []store.AllocationPeriod{{Start: "2027-02-01", End: "2027-02-28", Qty: decimal.RequireFromString("999999999999")}}
	if _, err := st.AllocateForecast(alice, store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "FAM-A", ExpectedVersion: 3, SiteID: site, Periods: huge}); problemCode(err) != store.CodeAllocationRange {
		t.Fatalf("overflow: %v", err)
	}
	if m, lines, _ := st.ListForecastLines(alice, "ALLOC", "", "", 20); m.Version != 3 || len(lines) != 4 {
		t.Fatalf("overflow changed the model: version %d, %d cells", m.Version, len(lines))
	}

	if _, err := st.SubmitForecastConsensus(alice, "ALLOC", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllocateForecast(alice, store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "FAM-A", ExpectedVersion: 4, SiteID: site, Periods: periods[:1]}); problemCode(err) != store.CodeForecastLocked {
		t.Fatalf("allocation into a submitted model: %v", err)
	}
	if _, err := st.AllocateForecast(asUser(2, "alice"), store.AllocateRequest{ModelCode: "ALLOC", KeyCode: "FAM-A", ExpectedVersion: 0, SiteID: site, Periods: periods[:1]}); problemCode(err) != store.CodeAllocationKeyNotFound {
		t.Fatalf("tenant 2 allocating with tenant 1's key: %v", err)
	}
}
