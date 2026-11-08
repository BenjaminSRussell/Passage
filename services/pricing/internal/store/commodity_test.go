package store_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func commodityBasis(code string) domain.CommodityBasis {
	return domain.CommodityBasis{LegalEntityID: legalEntity, Code: code, Name: "LME copper cash", Currency: "USD", Unit: "MT"}
}

func commodityCalc(basisID string) domain.CommodityCalc {
	return domain.CommodityCalc{LegalEntityID: legalEntity, Name: "Copper rod EA", BasisID: basisID, Factor: decimal.RequireFromString("0.0012"),
		MarginKind: domain.MarginPercent, Margin: decimal.NewFromInt(10), Rounding: 2, MaxIndexAgeDays: 7,
		CustomerKind: domain.CustomerKindAll, ProductID: "P1", Unit: "EA", Currency: "USD"}
}

func index(basisID, asOf, value string) domain.IndexValue {
	return domain.IndexValue{BasisID: basisID, AsOf: day(asOf), Value: decimal.RequireFromString(value), Source: "LME"}
}

// TestPostgres_CommodityRunBuildsReviewableJournal covers FR-26.21 against the database as the
// application role: basis codes, index revisions, the calculation, preview, the run's
// VALIDATED journal with the posted result, replay, withdraw and the outbox events.
func TestPostgres_CommodityRunBuildsReviewableJournal(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)

	basis, err := s.CreateCommodityBasis(ctx, commodityBasis("CU-LME"), "b-1")
	if err != nil || basis.Version != 1 {
		t.Fatalf("create basis = %+v err %v", basis, err)
	}
	if again, err := s.CreateCommodityBasis(ctx, commodityBasis("CU-LME"), "b-1"); err != nil || again.ID != basis.ID {
		t.Fatalf("replayed basis = %+v err %v", again, err)
	}
	if _, err := s.CreateCommodityBasis(ctx, commodityBasis("CU-LME"), ""); problemCode(err) != domain.CodeBasisCodeTaken {
		t.Fatalf("duplicate code = %v; want PRC-0054", err)
	}
	if _, err := s.CreateCommodityBasis(ctx, commodityBasis("CU-LME"), "b-1"); err != nil {
		t.Fatalf("replay after a duplicate: %v", err)
	}
	other := commodityBasis("CU-LME")
	other.Name = "different"
	if _, err := s.CreateCommodityBasis(ctx, other, "b-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key other body = %v; want idempotency_key_reuse", err)
	}

	calc, err := s.CreateCommodityCalc(ctx, commodityCalc(basis.ID), "c-1")
	if err != nil || calc.Version != 1 || !calc.Factor.Equal(decimal.RequireFromString("0.0012")) {
		t.Fatalf("create calculation = %+v err %v", calc, err)
	}
	if _, err := s.CreateCommodityCalc(ctx, commodityCalc("00000000-0000-0000-0000-00000000dead"), ""); problemCode(err) != domain.CodeBasisNotFound {
		t.Fatalf("calculation on unknown basis = %v; want PRC-0050", err)
	}

	rec := entry("2026-01-01", "2026-12-31")
	rec.CustomerKind, rec.CustomerKey = domain.CustomerKindAll, ""
	created, err := s.CreateRecord(ctx, rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRecord(ctx, created.ID, 1, ""); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.PreviewCommodityRun(ctx, calc.ID, day("2026-06-10")); problemCode(err) != domain.CodeNoIndexValue {
		t.Fatalf("preview without an index = %v; want PRC-0052", err)
	}
	if _, err := s.RecordIndexValue(ctx, index(basis.ID, "2026-06-08", "9000"), "i-1"); err != nil {
		t.Fatal(err)
	}
	if again, err := s.RecordIndexValue(ctx, index(basis.ID, "2026-06-08", "9000"), "i-1"); err != nil || again.Revision != 1 {
		t.Fatalf("replayed quote = %+v err %v", again, err)
	}
	v2, err := s.RecordIndexValue(ctx, index(basis.ID, "2026-06-08", "9100"), "")
	if err != nil || v2.Revision != 2 {
		t.Fatalf("correction = %+v err %v; want revision 2", v2, err)
	}
	if _, err := s.RecordIndexValue(ctx, index("00000000-0000-0000-0000-00000000dead", "2026-06-08", "1"), ""); problemCode(err) != domain.CodeBasisNotFound {
		t.Fatalf("quote on unknown basis = %v", err)
	}
	if vals, err := s.ListIndexValues(ctx, basis.ID, 0); err != nil || len(vals) != 2 || vals[0].Revision != 2 ||
		vals[0].AffectedAgreements != 1 || vals[1].AffectedAgreements != 1 {
		t.Fatalf("index values = %+v err %v; want revision 2 first, each affecting the one agreement on the calculation's key", vals, err)
	}

	_, plan, err := s.PreviewCommodityRun(ctx, calc.ID, day("2026-06-10"))
	if err != nil || plan.Index.Revision != 2 || !plan.Price.Equal(decimal.RequireFromString("12.012")) || len(plan.Changes) != 1 || plan.Stale {
		t.Fatalf("preview = %+v err %v; want revision 2, price 12.012, one change", plan, err)
	}
	if js, _ := s.ListJournals(ctx, store.JournalFilter{}); len(js) != 0 {
		t.Fatalf("preview wrote %d journals", len(js))
	}

	run, err := s.RunCommodityCalc(ctx, calc.ID, day("2026-06-10"), "r-1")
	if err != nil || run.ChangedCount != 1 || run.JournalID == "" || run.IndexRevision != 2 || !run.Price.Equal(decimal.RequireFromString("12.012")) {
		t.Fatalf("run = %+v err %v", run, err)
	}
	if again, err := s.RunCommodityCalc(ctx, calc.ID, day("2026-06-10"), "r-1"); err != nil || again.ID != run.ID {
		t.Fatalf("replayed run = %+v err %v", again, err)
	}
	if _, err := s.RunCommodityCalc(ctx, calc.ID, day("2026-06-11"), "r-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key other date = %v; want idempotency_key_reuse", err)
	}
	j, err := s.GetJournal(ctx, run.JournalID)
	if err != nil || j.Status != domain.JournalValidated || len(j.Lines) != 1 || j.Lines[0].Op != domain.OpUpdate || !j.Lines[0].After.Amount.Equal(decimal.RequireFromString("12.012")) {
		t.Fatalf("journal = %+v err %v; want VALIDATED with one UPDATE line", j, err)
	}
	if _, err := s.PostJournal(ctx, j.ID, ""); err != nil {
		t.Fatalf("post the run's journal: %v", err)
	}
	if got, err := s.GetRecord(ctx, created.ID); err != nil || !got.Amount.Equal(decimal.RequireFromString("12.012")) || got.Version != 3 {
		t.Fatalf("record after post = %+v err %v; want 12.012 at version 3", got, err)
	}

	// The record is at the price now: a second run changes nothing and writes no journal.
	again, err := s.RunCommodityCalc(ctx, calc.ID, day("2026-06-10"), "r-2")
	if err != nil || again.ChangedCount != 0 || again.UnchangedCount != 1 || again.JournalID != "" {
		t.Fatalf("run at price = %+v err %v", again, err)
	}
	// An index older than the window runs with the stale flag.
	stale, err := s.RunCommodityCalc(ctx, calc.ID, day("2026-07-30"), "r-3")
	if err != nil || !stale.Stale {
		t.Fatalf("stale run = %+v err %v", stale, err)
	}
	if runs, err := s.ListCommodityRuns(ctx, calc.ID, 0); err != nil || len(runs) != 3 {
		t.Fatalf("runs = %d err %v; want 3", len(runs), err)
	}

	if _, err := s.WithdrawCommodityCalc(ctx, calc.ID, 9, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale withdraw = %v", err)
	}
	w, err := s.WithdrawCommodityCalc(ctx, calc.ID, 1, "w-1")
	if err != nil || !w.Withdrawn || w.Version != 2 {
		t.Fatalf("withdraw = %+v err %v", w, err)
	}
	if again, err := s.WithdrawCommodityCalc(ctx, calc.ID, 1, "w-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed withdraw = %+v err %v", again, err)
	}
	if _, err := s.RunCommodityCalc(ctx, calc.ID, day("2026-06-10"), ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("run of a withdrawn calculation = %v; want PRC-0031", err)
	}
	// With its only calculation withdrawn, no quote prices any agreement.
	if vals, err := s.ListIndexValues(ctx, basis.ID, 0); err != nil || len(vals) != 2 || vals[0].AffectedAgreements != 0 || vals[1].AffectedAgreements != 0 {
		t.Fatalf("quotes after the withdraw = %+v err %v; want no affected agreements", vals, err)
	}

	counts := map[string]int{}
	for _, row := range outboxRows(t, ctx, pool) {
		counts[row.Type]++
	}
	for typ, want := range map[string]int{pevents.CommodityBasisCreated: 1, pevents.CommodityIndexRecorded: 2, pevents.CommodityCalculationCreated: 1,
		pevents.CommodityCalculationWithdrawn: 1, pevents.CommodityRunCompleted: 3} {
		if counts[typ] != want {
			t.Errorf("%s events = %d; want %d", typ, counts[typ], want)
		}
	}
}

// TestPostgres_CommodityRowsAreScopedToLegalEntity checks row-level security on the new tables:
// another tenant and another legal entity see none of the basis, quote or calculation.
func TestPostgres_CommodityRowsAreScopedToLegalEntity(t *testing.T) {
	pool := appPool(t)
	s := store.New(pool)
	ctx := principal(1, "")
	basis, err := s.CreateCommodityBasis(ctx, commodityBasis("CU-LME"), "")
	if err != nil {
		t.Fatal(err)
	}
	calc, err := s.CreateCommodityCalc(ctx, commodityCalc(basis.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordIndexValue(ctx, index(basis.ID, "2026-06-08", "9000"), ""); err != nil {
		t.Fatal(err)
	}
	for label, scoped := range map[string]func() error{
		"other tenant basis":  func() error { _, err := s.GetCommodityBasis(principal(2, ""), basis.ID); return err },
		"other tenant calc":   func() error { _, err := s.GetCommodityCalc(principal(2, ""), calc.ID); return err },
		"other entity basis":  func() error { _, err := s.GetCommodityBasis(principal(1, otherLegalEntity), basis.ID); return err },
		"other entity calc":   func() error { _, err := s.GetCommodityCalc(principal(1, otherLegalEntity), calc.ID); return err },
		"other entity quotes": func() error { _, err := s.ListIndexValues(principal(1, otherLegalEntity), basis.ID, 0); return err },
		"other entity preview": func() error {
			_, _, err := s.PreviewCommodityRun(principal(1, otherLegalEntity), calc.ID, day("2026-06-10"))
			return err
		},
	} {
		if err := scoped(); err == nil {
			t.Errorf("%s: visible across the scope", label)
		}
	}
	if list, err := s.ListCommodityBases(principal(1, otherLegalEntity), "", 0); err != nil || len(list) != 0 {
		t.Errorf("other entity list = %+v err %v", list, err)
	}
}

// asUser is principal(1, "") acting as a named user.
func asUser(user string) context.Context {
	p, _ := tenant.FromContext(principal(1, ""))
	p.UserID = user
	return tenant.With(context.Background(), p)
}

// TestPostgres_CommodityRunIsReproducibleAndSegregated covers the run's audit properties: a
// calculation cannot price in another currency than its basis, a correction or a backdated
// quote recorded after a run leaves that run's stored index and journal unchanged (only the
// next run reads it), and the user who ran the calculation cannot post its journal while
// another user can (CommodityPriceRun vs PricingJournalPost, PRD 5.26.17).
func TestPostgres_CommodityRunIsReproducibleAndSegregated(t *testing.T) {
	pool := appPool(t)
	s := store.New(pool)
	runner, poster := asUser("u-runner"), asUser("u-poster")

	basis, err := s.CreateCommodityBasis(runner, commodityBasis("CU-LME"), "")
	if err != nil {
		t.Fatal(err)
	}
	eur := commodityCalc(basis.ID)
	eur.Currency = "EUR"
	if _, err := s.CreateCommodityCalc(runner, eur, ""); problemCode(err) != domain.CodeCurrencyMismatch || problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("EUR calculation on a USD basis = %v; want 422 PRC-0055", err)
	}
	calc, err := s.CreateCommodityCalc(runner, commodityCalc(basis.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	rec := entry("2026-01-01", "2026-12-31")
	rec.CustomerKind, rec.CustomerKey = domain.CustomerKindAll, ""
	created, err := s.CreateRecord(runner, rec, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRecord(runner, created.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordIndexValue(runner, index(basis.ID, "2026-06-08", "9100"), ""); err != nil {
		t.Fatal(err)
	}
	run, err := s.RunCommodityCalc(runner, calc.ID, day("2026-06-10"), "")
	if err != nil || run.RunBy != "u-runner" || run.JournalID == "" {
		t.Fatalf("run = %+v err %v; want a journal run by u-runner", run, err)
	}

	// A correction of the quote the run used and a quote for a day before it arrive later.
	if v, err := s.RecordIndexValue(poster, index(basis.ID, "2026-06-08", "9500"), ""); err != nil || v.Revision != 2 {
		t.Fatalf("correction = %+v err %v", v, err)
	}
	if _, err := s.RecordIndexValue(poster, index(basis.ID, "2026-06-05", "8000"), ""); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListCommodityRuns(runner, calc.ID, 0)
	if err != nil || len(runs) != 1 || runs[0].IndexRevision != 1 || !runs[0].IndexValue.Equal(decimal.RequireFromString("9100")) ||
		!runs[0].Price.Equal(decimal.RequireFromString("12.012")) || runs[0].IndexAsOf.Format("2006-01-02") != "2026-06-08" {
		t.Fatalf("stored run after corrections = %+v err %v; want 2026-06-08 revision 1 at 9100 priced 12.012", runs, err)
	}
	j, err := s.GetJournal(runner, run.JournalID)
	if err != nil || !j.Lines[0].After.Amount.Equal(decimal.RequireFromString("12.012")) {
		t.Fatalf("journal after corrections = %+v err %v; want the line still at 12.012", j, err)
	}
	// The next preview reads the latest day on or before the date at its highest revision.
	_, plan, err := s.PreviewCommodityRun(runner, calc.ID, day("2026-06-10"))
	if err != nil || plan.Index.Revision != 2 || !plan.Index.Value.Equal(decimal.RequireFromString("9500")) {
		t.Fatalf("preview after corrections = %+v err %v; want 2026-06-08 revision 2", plan.Index, err)
	}

	if _, err := s.PostJournal(runner, run.JournalID, ""); problemCode(err) != domain.CodeRunOwnJournal || problemStatus(err) != http.StatusForbidden {
		t.Fatalf("post by the runner = %v; want 403 PRC-0056", err)
	}
	if got, _ := s.GetJournal(runner, run.JournalID); got.Status != domain.JournalValidated {
		t.Fatalf("journal after the refused post = %s; want VALIDATED", got.Status)
	}
	if _, err := s.PostJournal(poster, run.JournalID, ""); err != nil {
		t.Fatalf("post by another user: %v", err)
	}
	if got, err := s.GetRecord(runner, created.ID); err != nil || !got.Amount.Equal(decimal.RequireFromString("12.012")) {
		t.Fatalf("record after post = %+v err %v", got, err)
	}
}

// The Commodity prices inquiry counts, per quote, the ACTIVE PRICE records valid on the quote's
// day on the key of a live calculation of the basis: a record on a key of two calculations
// counts once, a draft, another product's record and another legal entity's record never
// count, and a caller scoped to another legal entity cannot read the basis at all.
func TestPostgres_CommodityQuotesCountAffectedAgreements(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	basis, err := s.CreateCommodityBasis(ctx, commodityBasis("CU-LME"), "")
	if err != nil {
		t.Fatal(err)
	}
	onP3 := commodityCalc(basis.ID)
	onP3.ProductID = "P3"
	for _, c := range []domain.CommodityCalc{commodityCalc(basis.ID), commodityCalc(basis.ID), onP3} {
		if _, err := s.CreateCommodityCalc(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	record := func(product, from, to, le string, activate bool) {
		t.Helper()
		e := entry(from, to)
		e.CustomerKind, e.CustomerKey, e.ProductKey, e.LegalEntityID = domain.CustomerKindAll, "", product, le
		created, err := s.CreateRecord(ctx, e, "")
		if err != nil {
			t.Fatal(err)
		}
		if activate {
			if _, err := s.ActivateRecord(ctx, created.ID, 1, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	record("P1", "2026-01-01", "2026-06-14", legalEntity, true)      // counts on 06-08
	record("P1", "2026-06-15", "2026-12-31", legalEntity, true)      // counts on 06-20
	record("P3", "2026-06-10", "2026-12-31", legalEntity, true)      // counts on 06-20
	record("P1", "2026-01-01", "2026-12-31", legalEntity, false)     // draft
	record("P2", "2026-01-01", "2026-12-31", legalEntity, true)      // no calculation on P2
	record("P1", "2026-01-01", "2026-12-31", otherLegalEntity, true) // another entity
	for _, d := range []string{"2026-06-08", "2026-06-20"} {
		if _, err := s.RecordIndexValue(ctx, index(basis.ID, d, "9000"), ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, scoped := range []context.Context{ctx, principal(1, legalEntity)} {
		vals, err := s.ListIndexValues(scoped, basis.ID, 0)
		if err != nil || len(vals) != 2 || vals[0].AffectedAgreements != 2 || vals[1].AffectedAgreements != 1 {
			t.Fatalf("quotes = %+v err %v; want 2 on 2026-06-20 and 1 on 2026-06-08", vals, err)
		}
	}
	if _, err := s.ListIndexValues(principal(1, otherLegalEntity), basis.ID, 0); problemCode(err) != domain.CodeBasisNotFound {
		t.Fatalf("quotes for a caller of another entity = %v; want PRC-0050", err)
	}
}
