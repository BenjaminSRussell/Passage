package store_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func testFund() domain.Fund {
	return domain.Fund{LegalEntityID: legalEntity, Name: "Spring TPM", Type: "TRADE", Currency: "USD",
		Owner: "tm-lead", BudgetPerPeriod: decimal.NewFromInt(1000)}
}

func usage(fundID, ref string, amount int64) store.FundAttribution {
	return store.FundAttribution{FundID: fundID, Period: "2027-03", Source: domain.UsageClaim, Reference: ref, Amount: decimal.NewFromInt(amount)}
}

// TestPostgres_FundBudgetCloseAndEvents covers FR-26.19 / BR-26.15 against the database
// with RLS enforced: the budget holds, a replayed Idempotency-Key adds no usage, close
// freezes the fund and reopen restores it, and each change writes its outbox event.
func TestPostgres_FundBudgetCloseAndEvents(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	f, err := s.CreateFund(ctx, testFund(), "fund-1")
	if err != nil || f.Version != 1 || f.Closed {
		t.Fatalf("create = %+v err %v; want open at version 1", f, err)
	}
	if again, err := s.CreateFund(ctx, testFund(), "fund-1"); err != nil || again.ID != f.ID {
		t.Fatalf("replayed create must return the original fund: id=%s err=%v", again.ID, err)
	}
	changed := testFund()
	changed.Name = "Other"
	if _, err := s.CreateFund(ctx, changed, "fund-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other body = %v; want idempotency_key_reuse", err)
	}

	pos, err := s.AttributeFund(ctx, usage(f.ID, "CLM-1", 600), "use-1")
	if err != nil || !pos.Used.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("attribution = used %v err %v; want 600", pos.Used, err)
	}
	if replay, err := s.AttributeFund(ctx, usage(f.ID, "CLM-1", 600), "use-1"); err != nil || !replay.Used.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("replayed attribution must not add usage: used %v err %v", replay.Used, err)
	}
	if _, err := s.AttributeFund(ctx, usage(f.ID, "CLM-9", 1), "use-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, other attribution = %v; want idempotency_key_reuse", err)
	}
	if _, err := s.AttributeFund(ctx, usage(f.ID, "CLM-2", 500), "use-2"); problemCode(err) != domain.CodeFundOverBudget {
		t.Fatalf("usage above budget = %v; want PRC-0012", err)
	}
	fund, lines, err := s.FundUsage(ctx, f.ID)
	if err != nil || fund.ID != f.ID || len(lines) != 1 || !lines[0].Amount.Equal(decimal.NewFromInt(600)) {
		t.Fatalf("usage lines = %v err %v; want one CLAIM line of 600", lines, err)
	}

	if _, err := s.ReviseFund(ctx, store.FundRevision{ID: f.ID, Version: 1, BudgetPerPeriod: decimal.NewFromInt(500)}, ""); problemCode(err) != domain.CodeFundOverBudget {
		t.Fatalf("budget below the period's usage = %v; want PRC-0012", err)
	}
	revised, err := s.ReviseFund(ctx, store.FundRevision{ID: f.ID, Version: 1, BudgetPerPeriod: decimal.NewFromInt(1200)}, "rev-1")
	if err != nil || revised.Version != 2 || !revised.BudgetPerPeriod.Equal(decimal.NewFromInt(1200)) {
		t.Fatalf("revise = %+v err %v", revised, err)
	}

	if _, err := s.CloseFund(ctx, f.ID, 1, ""); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale close = %v; want 412", err)
	}
	closed, err := s.CloseFund(ctx, f.ID, 2, "close-1")
	if err != nil || !closed.Closed || closed.Version != 3 {
		t.Fatalf("close = %+v err %v", closed, err)
	}
	if again, err := s.CloseFund(ctx, f.ID, 2, "close-1"); err != nil || again.Version != 3 {
		t.Fatalf("replayed close = %+v err %v; want the first response", again, err)
	}
	if _, err := s.AttributeFund(ctx, usage(f.ID, "CLM-3", 10), "use-3"); problemCode(err) != domain.CodeFundClosed {
		t.Fatalf("a closed fund must reject usage = %v; want PRC-0011", err)
	}
	if _, err := s.ReopenFund(ctx, f.ID, 3, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AttributeFund(ctx, usage(f.ID, "CLM-4", 10), ""); err != nil {
		t.Fatalf("a reopened fund takes usage: %v", err)
	}

	var types []string
	for _, row := range outboxRows(t, ctx, pool) {
		types = append(types, row.Type)
		if row.Type == pevents.FundUsageRecorded && row.Data["reference"] == "CLM-1" &&
			(row.Data["period_usage"] != "600" || row.Data["budget"] != "1000" || row.Data["currency"] != "USD") {
			t.Fatalf("usage event = %v", row.Data)
		}
	}
	want := []string{pevents.FundUsageRecorded, pevents.FundClosed, pevents.FundReopened, pevents.FundUsageRecorded}
	if len(types) != len(want) {
		t.Fatalf("outbox = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("outbox = %v; want %v", types, want)
		}
	}

	if _, err := s.GetFund(principal(2, ""), f.ID); problemCode(err) != domain.CodeFundNotFound {
		t.Fatalf("another tenant must not see the fund: %v", err)
	}
}

// TestPostgres_PromotionPlan covers FR-26.20: the 13-week baseline round-trips, the fund
// must match and be open, and actuals produce effectiveness against the baseline.
func TestPostgres_PromotionPlan(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	f, err := s.CreateFund(ctx, testFund(), "")
	if err != nil {
		t.Fatal(err)
	}
	weekly := make([]decimal.Decimal, domain.BaselineWeeks)
	for i := range weekly {
		weekly[i] = decimal.RequireFromString("10.5")
	}
	start := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	plan := domain.PromotionPlan{LegalEntityID: legalEntity, Name: "Spring push", EventCategory: "FEATURE", EventType: "TPR",
		FundID: f.ID, ProductID: "P-1", Currency: "USD", Start: start, End: start.AddDate(0, 0, 27), BaselineWeekly: weekly,
		LiftPct: decimal.NewFromInt(25), PlannedSpend: decimal.NewFromInt(500)}

	eur := plan
	eur.Currency = "EUR"
	if _, err := s.CreatePromotion(ctx, eur, ""); problemStatus(err) != http.StatusUnprocessableEntity {
		t.Fatalf("currency mismatch = %v; want 422", err)
	}
	p, err := s.CreatePromotion(ctx, plan, "promo-1")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.CreatePromotion(ctx, plan, "promo-1"); err != nil || again.ID != p.ID {
		t.Fatalf("replayed create = %s err %v", again.ID, err)
	}
	got, err := s.RecordPromotionActuals(ctx, p.ID, domain.PromotionActuals{Units: decimal.NewFromInt(252), Weeks: 4, Spend: decimal.NewFromInt(480)})
	if err != nil || got.Actuals == nil || got.Actuals.Weeks != 4 {
		t.Fatalf("actuals = %+v err %v", got.Actuals, err)
	}
	read, err := s.GetPromotion(ctx, p.ID)
	if err != nil || read.FundID != f.ID || read.EventType != "TPR" || len(read.BaselineWeekly) != domain.BaselineWeeks ||
		!read.BaselineWeekly[0].Equal(decimal.RequireFromString("10.5")) {
		t.Fatalf("read promotion = %+v err %v", read, err)
	}
	base, _ := domain.Baseline(read.BaselineWeekly)
	// 252 / 4 = 63 a week against 10.5: (63 − 10.5) / 10.5 = 500%.
	if eff, err := domain.Effectiveness(base, *read.Actuals); err != nil || !eff.Equal(decimal.NewFromInt(500)) {
		t.Fatalf("effectiveness = %s err %v; want 500", eff, err)
	}
	if _, err := s.RecordPromotionActuals(ctx, "00000000-0000-0000-0000-00000000dead", domain.PromotionActuals{Units: decimal.Zero, Weeks: 1}); problemCode(err) != domain.CodePromoNotFound {
		t.Fatalf("missing promotion = %v; want PRC-0040", err)
	}

	if _, err := s.CloseFund(ctx, f.ID, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePromotion(ctx, plan, ""); problemCode(err) != domain.CodeFundClosed {
		t.Fatalf("promotion on a closed fund = %v; want PRC-0011", err)
	}
	list, err := s.ListPromotions(ctx, legalEntity, 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d err %v; want 1", len(list), err)
	}
}

func problemStatus(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}
