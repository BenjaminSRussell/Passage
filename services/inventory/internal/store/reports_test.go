package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func TestCountVarianceReportValuesPostedAndOpenSheets(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product, other := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "50", "2", dims()), line(other, "10", "3", dims()))

	// A posted sheet: 46 counted against 50, the 8 % variance needs an approver.
	plan := countPlan(t, s, ctx, false)
	sheet, _, err := s.GenerateCountSheet(ctx, plan.ID, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	var mine, theirs domain.CountLine
	for _, l := range sheet.Lines {
		if l.ProductID == product {
			mine = l
		} else {
			theirs = l
		}
	}
	counted, err := s.EnterCount(ctx, sheet.ID, mine.ID, sheet.Version, dec("46"))
	if err != nil {
		t.Fatal(err)
	}
	counted, err = s.EnterCount(ctx, sheet.ID, theirs.ID, counted.Version, dec("10"))
	if err != nil {
		t.Fatal(err)
	}
	if rows, _, err := s.ListCountVariance(ctx, store.CountVarianceFilter{SheetID: sheet.ID}); err != nil || len(rows) != 2 {
		t.Fatalf("open sheet: %v %d rows", err, len(rows))
	} else {
		for _, r := range rows {
			if r.ValueBasis != domain.ValueBasisEstimate || r.PostedVariance != nil || r.SheetStatus != domain.CountOpen {
				t.Fatalf("open line %+v", r)
			}
			if r.ProductID == product && !r.UnitCost.Equal(dec("2")) {
				t.Fatalf("estimate cost %s, want the running average 2", r.UnitCost)
			}
		}
	}
	if _, err := s.PostCountSheet(approverCtx(ctx), sheet.ID, counted.Version, true, postParams()); err != nil {
		t.Fatal(err)
	}

	rows, truncated, err := s.ListCountVariance(ctx, store.CountVarianceFilter{PlanID: plan.ID})
	if err != nil || truncated || len(rows) != 2 {
		t.Fatalf("posted sheet: %v truncated=%v %d rows", err, truncated, len(rows))
	}
	for _, r := range rows {
		if r.SheetStatus != domain.CountPosted || r.Warehouse != "W1" || r.LegalEntityID != legalEntity(ctx) || r.CountedBy == "" || r.CountedAt.IsZero() {
			t.Fatalf("line %+v", r)
		}
		switch r.ProductID {
		case product:
			if r.PostedVariance == nil || !r.PostedVariance.Equal(dec("-4")) || r.ValueBasis != domain.ValueBasisJournal || !r.UnitCost.Equal(dec("2")) ||
				!r.Expected.Equal(dec("50")) || !r.Counted.Equal(dec("46")) {
				t.Fatalf("variance line %+v", r)
			}
		default:
			// A matching count posts no journal line; it is still a posted (JOURNAL) line valued at zero.
			if r.PostedVariance == nil || !r.PostedVariance.IsZero() || r.ValueBasis != domain.ValueBasisJournal {
				t.Fatalf("a matching count on a posted sheet: %+v", r)
			}
		}
	}

	filters := map[string]store.CountVarianceFilter{
		"counter":        {Counter: rows[0].CountedBy},
		"warehouse":      {Warehouse: "W1"},
		"legal entity":   {LegalEntityID: legalEntity(ctx)},
		"counted today":  {From: dayPtr(time.Now().UTC()), To: dayPtr(time.Now().UTC())},
		"nothing at W9":  {Warehouse: "W9"},
		"other counter":  {Counter: "nobody"},
		"counted before": {To: dayPtr(time.Now().UTC().AddDate(0, 0, -2))},
		"other plan":     {PlanID: ids.New()},
	}
	want := map[string]int{"counter": 2, "warehouse": 2, "legal entity": 2, "counted today": 2}
	for name, f := range filters {
		got, _, err := s.ListCountVariance(ctx, f)
		if err != nil || len(got) != want[name] {
			t.Errorf("%s: %v %d rows, want %d", name, err, len(got), want[name])
		}
	}
	if got, _, err := s.ListCountVariance(entityCtx(1, ids.New()), store.CountVarianceFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity sees the sheet: %v %d", err, len(got))
	}
	if got, _, err := s.ListCountVariance(entityCtx(2, legalEntity(ctx)), store.CountVarianceFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another tenant sees the sheet: %v %d", err, len(got))
	}
}

func dayPtr(t time.Time) *time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return &d
}

func TestNegativeOnHandListShowsShortfallAndFirstNegativeDate(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	short, fine := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(short, "5", "2", dims()), line(fine, "5", "2", dims()), line(short, "9", "2", dims("warehouse", "W2")))
	j, err := newJournal(ctx, s, domain.JournalAdjustment, line(short, "-8", "2", dims()))
	if err != nil {
		t.Fatal(err)
	}
	allow := postParams()
	allow.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, allow); err != nil {
		t.Fatal(err)
	}

	rows, truncated, err := s.ListNegativeOnHand(ctx, store.NegativeOnHandFilter{})
	if err != nil || truncated || len(rows) != 1 {
		t.Fatalf("list: %v truncated=%v %d rows", err, truncated, len(rows))
	}
	r := rows[0]
	if r.ProductID != short || !r.PhysicalInvent.Equal(dec("-3")) || r.Dims["warehouse"] != "W1" || r.LegalEntityID != le || r.Currency != "USD" {
		t.Fatalf("row %+v", r)
	}
	if r.FirstNegative == nil || r.FirstNegative.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("first negative %v, want the journal's posting date 2026-09-01", r.FirstNegative)
	}
	for name, f := range map[string]store.NegativeOnHandFilter{"product": {ProductID: short}, "site": {SiteID: "S1"}, "warehouse": {WarehouseID: "W1"}, "entity": {LegalEntityID: le}} {
		if got, _, err := s.ListNegativeOnHand(ctx, f); err != nil || len(got) != 1 {
			t.Errorf("%s filter: %v %d rows", name, err, len(got))
		}
	}
	for name, f := range map[string]store.NegativeOnHandFilter{"other product": {ProductID: fine}, "other warehouse": {WarehouseID: "W2"}, "other site": {SiteID: "S9"}} {
		if got, _, err := s.ListNegativeOnHand(ctx, f); err != nil || len(got) != 0 {
			t.Errorf("%s filter: %v %d rows", name, err, len(got))
		}
	}
	if got, _, err := s.ListNegativeOnHand(entityCtx(1, ids.New()), store.NegativeOnHandFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity sees negative stock: %v %d", err, len(got))
	}
	if got, _, err := s.ListNegativeOnHand(tenantWideCtx(2), store.NegativeOnHandFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another tenant sees negative stock: %v %d", err, len(got))
	}
}

func TestTrackingTraceFollowsABatchAndASerial(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2", dims("batch", "B1")), line(product, "4", "2", dims("batch", "B2")),
		line(product, "1", "9", dims("serial", "SN1")))
	post(t, ctx, s, domain.JournalAdjustment, line(product, "-3", "2", dims("batch", "B1")))

	both, truncated, err := s.TraceTracking(ctx, store.TraceFilter{Batch: "B1", Direction: domain.TraceBoth})
	if err != nil || truncated || len(both) != 2 {
		t.Fatalf("both: %v %d rows", err, len(both))
	}
	if !both[0].Qty.Equal(dec("10")) || !both[1].Qty.Equal(dec("-3")) || both[0].Dims["batch"] != "B1" || both[0].SourceType != "INVENT_JOURNAL_LINE" || both[0].JournalID == "" {
		t.Fatalf("rows %+v", both)
	}
	if fwd, _, err := s.TraceTracking(ctx, store.TraceFilter{Batch: "B1", Direction: domain.TraceForward}); err != nil || len(fwd) != 1 || !fwd[0].Qty.Equal(dec("-3")) {
		t.Fatalf("forward: %v %+v", err, fwd)
	}
	if back, _, err := s.TraceTracking(ctx, store.TraceFilter{Batch: "B1", Direction: domain.TraceBackward}); err != nil || len(back) != 1 || !back[0].Qty.Equal(dec("10")) {
		t.Fatalf("backward: %v %+v", err, back)
	}
	if sn, _, err := s.TraceTracking(ctx, store.TraceFilter{Serial: "SN1", ProductID: product, Direction: domain.TraceBoth}); err != nil || len(sn) != 1 {
		t.Fatalf("serial: %v %d", err, len(sn))
	}
	if none, _, err := s.TraceTracking(ctx, store.TraceFilter{Batch: "B1", ProductID: ids.New(), Direction: domain.TraceBoth}); err != nil || len(none) != 0 {
		t.Fatalf("other product: %v %d", err, len(none))
	}
	if none, _, err := s.TraceTracking(entityCtx(1, ids.New()), store.TraceFilter{Batch: "B1", Direction: domain.TraceBoth}); err != nil || len(none) != 0 {
		t.Fatalf("another legal entity traces the batch: %v %d", err, len(none))
	}
	if none, _, err := s.TraceTracking(entityCtx(2, le), store.TraceFilter{Batch: "B1", Direction: domain.TraceBoth}); err != nil || len(none) != 0 {
		t.Fatalf("another tenant traces the batch: %v %d", err, len(none))
	}
}

func TestDimensionCombinationsReportFirstAndLastUse(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	product, other := ids.New(), ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "2", dims()), line(product, "4", "2", dims("batch", "B1")), line(other, "1", "1", dims()))
	post(t, ctx, s, domain.JournalAdjustment, line(product, "-1", "2", dims()))

	rows, truncated, err := s.ListDimensionCombinations(ctx, store.DimensionCombinationFilter{ProductID: product})
	if err != nil || truncated || len(rows) != 2 {
		t.Fatalf("list: %v %d rows", err, len(rows))
	}
	counts := map[int]int{}
	for _, r := range rows {
		counts[r.TransactionCount]++
		if r.ProductID != product || len(r.Hash) != 64 || r.FirstUse.IsZero() || r.LastUse.Before(r.FirstUse) {
			t.Fatalf("row %+v", r)
		}
	}
	if counts[2] != 1 || counts[1] != 1 {
		t.Fatalf("transaction counts %v, want one combination used twice and one once", counts)
	}
	if all, _, err := s.ListDimensionCombinations(ctx, store.DimensionCombinationFilter{LegalEntityID: le}); err != nil || len(all) != 3 {
		t.Fatalf("all: %v %d rows", err, len(all))
	}
	if got, _, err := s.ListDimensionCombinations(entityCtx(1, ids.New()), store.DimensionCombinationFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity: %v %d", err, len(got))
	}
}

// A cancelled count sheet never posts a variance, so its counted lines stay
// out of the report and out of the counter totals.
func TestCountVarianceReportLeavesOutCancelledSheets(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "3", dims()))
	plan := countPlan(t, s, ctx, false)
	sheet, _, err := s.GenerateCountSheet(ctx, plan.ID, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	counted, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("17"))
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.ListCountVariance(ctx, store.CountVarianceFilter{SheetID: sheet.ID})
	if err != nil || len(rows) != 1 || !rows[0].UnitCost.Equal(dec("3")) || rows[0].ValueBasis != domain.ValueBasisEstimate {
		t.Fatalf("open sheet: %v %+v", err, rows)
	}
	if _, err := s.CancelCountSheet(ctx, sheet.ID, counted.Version); err != nil {
		t.Fatal(err)
	}
	if rows, _, err := s.ListCountVariance(ctx, store.CountVarianceFilter{SheetID: sheet.ID}); err != nil || len(rows) != 0 {
		t.Fatalf("cancelled sheet still reported: %v %+v", err, rows)
	}
}

// postOn creates and posts a journal dated day, letting stock go negative.
func postOn(t *testing.T, ctx context.Context, s *store.Postgres, typ domain.JournalType, day string, lines ...domain.JournalLine) {
	t.Helper()
	p, _ := tenant.FromContext(ctx)
	date, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := s.CreateJournal(ctx, domain.Journal{LegalEntityID: p.LegalEntityID, Type: typ, PostingDate: date, Currency: "USD"}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.AddJournalLines(ctx, created.ID, lines)
	if err != nil {
		t.Fatal(err)
	}
	allow := postParams()
	allow.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, allow); err != nil {
		t.Fatal(err)
	}
}

// The first negative date is the start of the current negative run: an
// earlier negative spell that was corrected does not count, and a same-day
// issue posted before that day's receipt is netted, not a negative.
func TestNegativeOnHandFirstNegativeDateIsTheCurrentRun(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	relapsed, netted := ids.New(), ids.New()
	postOn(t, ctx, s, domain.JournalOpeningBalance, "2026-08-01", line(relapsed, "5", "2", dims()), line(netted, "5", "2", dims()))
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-02", line(relapsed, "-8", "2", dims())) // -3: first spell
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-03", line(relapsed, "10", "2", dims())) // 7: corrected
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-05", line(relapsed, "-9", "2", dims())) // -2: current spell
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-06", line(relapsed, "-1", "2", dims())) // -3: still negative
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-02", line(netted, "-6", "2", dims()))   // issue first ...
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-02", line(netted, "4", "2", dims()))    // ... same-day receipt: 3
	postOn(t, ctx, s, domain.JournalAdjustment, "2026-08-04", line(netted, "-5", "2", dims()))   // -2

	rows, _, err := s.ListNegativeOnHand(ctx, store.NegativeOnHandFilter{})
	if err != nil || len(rows) != 2 {
		t.Fatalf("list: %v %d rows", err, len(rows))
	}
	want := map[string]string{relapsed: "2026-08-05", netted: "2026-08-04"}
	for _, r := range rows {
		if r.FirstNegative == nil || r.FirstNegative.Format("2006-01-02") != want[r.ProductID] {
			t.Errorf("product %s: first negative %v, want %s", r.ProductID, r.FirstNegative, want[r.ProductID])
		}
	}
	if !rows[0].PhysicalInvent.Equal(dec("-3")) || rows[0].ProductID != relapsed {
		t.Fatalf("largest shortfall first: %+v", rows[0])
	}
}
