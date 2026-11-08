package store_test

import (
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// FR-26.18 against Postgres: recording an export freezes the rows it summed, a period cannot go
// to the same target twice until the export is voided, and each change writes its event.
func TestPostgres_SettlementExportLifecycle(t *testing.T) {
	pool := appPool(t)
	ctx := principal(21, "")
	s := store.New(pool)
	g, err := s.CreateSalesGroup(ctx, store.SalesGroup{LegalEntityID: legalEntity, Name: "SG-X",
		Reps: []domain.Share{{RepID: "R1", SharePct: decimal.NewFromInt(60)}, {RepID: "R2", SharePct: decimal.NewFromInt(40)}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCommissionRule(ctx, domain.CommissionRule{SalesGroupID: g.ID, Basis: domain.CommissionRevenue, RatePct: decimal.NewFromInt(5)}, ""); err != nil {
		t.Fatal(err)
	}
	post := func(line, date, net string) {
		t.Helper()
		l := commissionLine(line, "", net)
		l.InvoiceDate = day(date)
		if _, _, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: l}); err != nil {
			t.Fatal(err)
		}
	}
	post("INV-1", "2027-03-01", "10000") // R1 300, R2 200
	post("INV-2", "2027-03-31", "2000")  // R1 60, R2 40
	march := store.SettlementExportInput{LegalEntityID: legalEntity, Target: domain.SettlementTargetPayroll, From: day("2027-03-01"), To: day("2027-03-31")}

	e, err := s.CreateSettlementExport(ctx, march, "exp-1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Status != domain.ExportIssued || e.Version != 1 || len(e.Lines) != 2 || e.From != "2027-03-01" || e.To != "2027-03-31" ||
		!e.Lines[0].Amount.Equal(decimal.NewFromInt(360)) || !e.Lines[1].Amount.Equal(decimal.NewFromInt(240)) || len(e.CSVSHA256) != 64 {
		t.Fatalf("export = %+v", e)
	}
	if again, err := s.CreateSettlementExport(ctx, march, "exp-1"); err != nil || again.ID != e.ID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	changed := march
	changed.To = day("2027-03-30")
	if _, err := s.CreateSettlementExport(ctx, changed, "exp-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("key reuse with another period = %v", err)
	}

	// Later postings change the preview but not the recorded rows.
	post("INV-3", "2027-03-15", "1000")
	got, err := s.GetSettlementExport(ctx, e.ID)
	if err != nil || len(got.Lines) != 2 || !got.Lines[0].Amount.Equal(decimal.NewFromInt(360)) || got.CSVSHA256 != e.CSVSHA256 {
		t.Fatalf("recorded rows changed: %+v, %v", got, err)
	}
	// The file rebuilt from the stored rows and dates is the one whose digest was recorded.
	if raw, err := domain.SettlementCSV(got.Target, got.From, got.To, got.Lines); err != nil || domain.CSVDigest(raw) != e.CSVSHA256 {
		t.Fatalf("rebuilt file digest = %s, %v; want %s", domain.CSVDigest(raw), err, e.CSVSHA256)
	}
	if len(got.CreatedAt) == 0 || got.CreatedAt[len(got.CreatedAt)-1] != 'Z' {
		t.Fatalf("createdAt = %q; want UTC RFC 3339", got.CreatedAt)
	}
	if live, _ := s.SettlementTotals(ctx, legalEntity, day("2027-03-01"), day("2027-03-31")); !live[0].Amount.Equal(decimal.NewFromInt(390)) {
		t.Fatalf("preview = %+v; want R1 390", live)
	}

	// Overlap on the same entity and target is PRC-0065; another target, entity or tenant is not.
	overlap := march
	overlap.From, overlap.To = day("2027-03-31"), day("2027-04-30")
	if _, err := s.CreateSettlementExport(ctx, overlap, ""); problemCode(err) != domain.CodeExportOverlap {
		t.Fatalf("overlap = %v; want PRC-0065", err)
	}
	ap := march
	ap.Target = domain.SettlementTargetAP
	if _, err := s.CreateSettlementExport(ctx, ap, ""); err != nil {
		t.Fatalf("another target: %v", err)
	}
	april := store.SettlementExportInput{LegalEntityID: legalEntity, Target: domain.SettlementTargetPayroll, From: day("2027-04-01"), To: day("2027-04-30")}
	if _, err := s.CreateSettlementExport(ctx, april, ""); problemCode(err) != domain.CodeExportEmpty {
		t.Fatalf("empty period = %v; want PRC-0066", err)
	}

	list, err := s.ListSettlementExports(ctx, store.SettlementExportFilter{Target: domain.SettlementTargetPayroll, Status: domain.ExportIssued})
	if err != nil || len(list) != 1 || list[0].ID != e.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}

	// Void: stale version 412, then voided with the reason, then a second void is PRC-0031.
	if _, err := s.VoidSettlementExport(ctx, e.ID, 5, "wrong", ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale void = %v", err)
	}
	v, err := s.VoidSettlementExport(ctx, e.ID, 1, "sent the wrong month", "void-1")
	if err != nil || v.Status != domain.ExportVoided || v.Version != 2 || v.VoidReason != "sent the wrong month" {
		t.Fatalf("void = %+v, %v", v, err)
	}
	if again, err := s.VoidSettlementExport(ctx, e.ID, 1, "sent the wrong month", "void-1"); err != nil || again.Version != 2 {
		t.Fatalf("void replay = %+v, %v", again, err)
	}
	if _, err := s.VoidSettlementExport(ctx, e.ID, 2, "again", ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("second void = %v; want PRC-0031", err)
	}
	if _, err := s.CreateSettlementExport(ctx, march, ""); err != nil {
		t.Fatalf("re-export after void: %v", err)
	}

	var types []string
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Data["export_id"] == e.ID {
			types = append(types, row.Type)
			if row.Type == events.CommissionExportVoided && row.Data["void_reason"] != "sent the wrong month" {
				t.Fatalf("voided event = %v", row.Data)
			}
			if _, leaked := row.Data["lines"]; leaked || row.Data["csv_sha256"] != e.CSVSHA256 || row.Data["line_count"] != float64(2) {
				t.Fatalf("event = %v", row.Data)
			}
		}
	}
	if len(types) != 2 || types[0] != events.CommissionExportIssued || types[1] != events.CommissionExportVoided {
		t.Fatalf("events of the export = %v", types)
	}

	// Row security: another tenant and another legal-entity scope do not see it.
	if _, err := s.GetSettlementExport(principal(22, ""), e.ID); problemCode(err) != domain.CodeExportNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := s.GetSettlementExport(principal(21, otherLegalEntity), e.ID); problemCode(err) != domain.CodeExportNotFound {
		t.Fatalf("other legal entity scope: %v", err)
	}
}

// Two callers record the same period at once: the advisory lock lets exactly one through.
func TestPostgres_SettlementExportConcurrentSamePeriod(t *testing.T) {
	pool := appPool(t)
	ctx := principal(23, "")
	s := store.New(pool)
	g, err := s.CreateSalesGroup(ctx, store.SalesGroup{LegalEntityID: legalEntity, Name: "SG-C",
		Reps: []domain.Share{{RepID: "R1", SharePct: decimal.NewFromInt(100)}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCommissionRule(ctx, domain.CommissionRule{SalesGroupID: g.ID, Basis: domain.CommissionRevenue, RatePct: decimal.NewFromInt(5)}, ""); err != nil {
		t.Fatal(err)
	}
	l := commissionLine("INV-C", "", "1000")
	l.InvoiceDate = day("2027-06-10")
	if _, _, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: l}); err != nil {
		t.Fatal(err)
	}
	in := store.SettlementExportInput{LegalEntityID: legalEntity, Target: domain.SettlementTargetAP, From: day("2027-06-01"), To: day("2027-06-30")}
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.CreateSettlementExport(ctx, in, "")
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case problemCode(err) != domain.CodeExportOverlap:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d exports were recorded for one period; want exactly 1", ok)
	}
}
