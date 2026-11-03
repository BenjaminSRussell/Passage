package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func statCell(item, site, start, end, qty string) store.ForecastCellInput {
	return store.ForecastCellInput{ItemID: item, SiteID: site, PeriodStart: start, PeriodEnd: end, Source: "statistical", Qty: decimal.RequireFromString(qty)}
}

// TestGenerateStatisticalCells covers FR-22.9 on the database: a generation
// creates the model, replaces earlier statistical cells but never a manual one,
// recomputes the reduced quantity, bumps the version, writes
// forecast.generated.v1 in the same transaction, locks out a submitted model and
// stays inside the tenant.
func TestGenerateStatisticalCells(t *testing.T) {
	st, admin, _ := setup(t)
	alice := asUser(1, "alice")
	item, site := ids.New(), ids.New()

	out, err := st.GenerateStatisticalCells(alice, "STAT", "", 0, []store.ForecastCellInput{
		statCell(item, site, "2026-12-01", "2026-12-31", "10"),
		statCell(item, site, "2027-01-01", "2027-01-31", "20"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.CellsWritten != 2 || out.CellsKept != 0 || out.Model.Version != 1 {
		t.Fatalf("first generation = %+v", out)
	}

	// A planner overrides January; the next generation must keep it.
	manual := statCell(item, site, "2027-01-01", "2027-01-31", "99")
	manual.Source = "manual"
	if _, err := st.UpsertForecastLines(alice, "STAT", "", 1, nil, []store.ForecastCellInput{manual}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GenerateStatisticalCells(alice, "STAT", "", 1, nil); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	out, err = st.GenerateStatisticalCells(alice, "STAT", "", 2, []store.ForecastCellInput{
		statCell(item, site, "2026-12-01", "2026-12-31", "11"),
		statCell(item, site, "2027-01-01", "2027-01-31", "21"),
		statCell(item, site, "2027-02-01", "2027-02-28", "31"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.CellsWritten != 2 || out.CellsKept != 1 || out.Model.Version != 3 {
		t.Fatalf("second generation = %+v", out)
	}
	_, lines, err := st.ListForecastLines(alice, "STAT", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.ForecastLine{}
	for _, l := range lines {
		got[l.PeriodStart] = l
	}
	if len(lines) != 3 || got["2026-12-01"].Qty != "11.000000" || got["2026-12-01"].ReducedQty != "11.000000" || got["2026-12-01"].Source != "statistical" ||
		got["2027-01-01"].Qty != "99.000000" || got["2027-01-01"].Source != "manual" || got["2027-02-01"].Qty != "31.000000" {
		t.Fatalf("lines = %+v", lines)
	}

	var n int
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select count(*), max(payload::text) from outbox where type = $1`, planevents.ForecastGenerated).Scan(&n, &raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data planevents.ForecastGeneratedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	if n != 2 || evt.Data.ModelCode != "STAT" || evt.Data.Version != 3 || evt.Data.Series != 1 || evt.Data.CellsWritten != 2 || evt.Data.CellsKept != 1 ||
		evt.Data.PeriodFrom != "2026-12-01" || evt.Data.PeriodTo != "2027-02-28" || evt.Data.GeneratedBy != "alice" {
		t.Fatalf("outbox has %d forecast.generated events, last %+v", n, evt.Data)
	}

	if _, err := st.SubmitForecastConsensus(alice, "STAT", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GenerateStatisticalCells(alice, "STAT", "", 4, []store.ForecastCellInput{statCell(item, site, "2027-03-01", "2027-03-31", "1")}); problemCode(err) != store.CodeForecastLocked {
		t.Fatalf("generation on a submitted model: %v", err)
	}
	// Another tenant does not see the model; its own first generation starts at version 0.
	if _, err := st.GenerateStatisticalCells(asUser(2, "alice"), "STAT", "", 4, nil); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("tenant 2 at tenant 1's version: %v", err)
	}
}
