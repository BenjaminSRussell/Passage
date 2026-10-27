package store_test

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/events"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func commissionLine(line, reverses, net string) domain.CommissionLine {
	return domain.CommissionLine{InvoiceLineID: line, ReversesInvoiceLineID: reverses, InvoiceDate: day("2026-03-10"),
		CustomerID: "C1", Currency: "USD", LineNet: decimal.RequireFromString(net)}
}

func sumAmounts(txns []store.CommissionTxn) decimal.Decimal {
	sum := decimal.Zero
	for _, t := range txns {
		sum = sum.Add(t.Amount)
	}
	return sum
}

// AC-26.12 against Postgres: a 10,000.00 USD line at 5% splits 300.00 / 200.00 across R1 60% /
// R2 40% (Dr 6200 / Cr 2170), a redelivered line adds nothing, the credit note posts the
// negative split, and over-crediting is refused. Every rep row writes commission.calculated.
func TestPostgres_CommissionPostsOnceAndReverses(t *testing.T) {
	pool := appPool(t)
	ctx := principal(4, "")
	s := store.New(pool)
	reps := []domain.Share{{RepID: "R1", SharePct: decimal.NewFromInt(60)}, {RepID: "R2", SharePct: decimal.NewFromInt(40)}}
	g, err := s.CreateSalesGroup(ctx, store.SalesGroup{LegalEntityID: legalEntity, Name: "SG-E", Reps: reps}, "sg-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.CreateSalesGroup(ctx, store.SalesGroup{LegalEntityID: legalEntity, Name: "SG-E", Reps: reps}, "sg-1"); err != nil || replay.ID != g.ID {
		t.Fatalf("replayed create = %+v, %v; want %s", replay, err, g.ID)
	}
	if _, err := s.CreateSalesGroup(ctx, store.SalesGroup{LegalEntityID: legalEntity, Name: "SG-X", Reps: reps}, "sg-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("reused key with another body = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, _, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: commissionLine("INV-0", "", "1")}); problemCode(err) != domain.CodeCommissionRuleMissing {
		t.Fatalf("posting without a rule = %v; want PRC-0013", err)
	}
	rule, err := s.CreateCommissionRule(ctx, domain.CommissionRule{SalesGroupID: g.ID, Basis: domain.CommissionRevenue, RatePct: decimal.NewFromInt(5)}, "rule-1")
	if err != nil || rule.LegalEntityID != legalEntity {
		t.Fatalf("rule = %+v, %v", rule, err)
	}
	if _, err := s.CreateCommissionRule(ctx, domain.CommissionRule{SalesGroupID: g.ID, Basis: domain.CommissionMargin, RatePct: decimal.NewFromInt(7)}, ""); problemCode(err) != domain.CodeCommissionRuleExists {
		t.Fatalf("a second (all, all) rule = %v; want PRC-0038", err)
	}

	inv := store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: commissionLine("INV-1", "", "10000")}
	txns, created, err := s.PostCommission(ctx, inv)
	if err != nil || !created || len(txns) != 2 || txns[0].RepID != "R1" || !txns[0].Amount.Equal(decimal.NewFromInt(300)) || !txns[1].Amount.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("post = %+v created=%v err=%v", txns, created, err)
	}
	if txns[0].DebitAccount != "6200" || txns[0].CreditAccount != "2170" || txns[0].PeriodKey != "2026-03" {
		t.Fatalf("posting accounts and period = %+v", txns[0])
	}
	if again, created, err := s.PostCommission(ctx, inv); err != nil || created || len(again) != 2 {
		t.Fatalf("redelivery = %d rows created=%v err=%v; want the stored rows", len(again), created, err)
	}
	changed := inv
	changed.Line.LineNet = decimal.NewFromInt(9000)
	if _, _, err := s.PostCommission(ctx, changed); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("redelivery with another net = %v; want %s", err, store.CodeIdempotencyUse)
	}

	// The rule changes after the invoice: the credit still reverses at the original 5%.
	if _, err := s.CreateCommissionRule(ctx, domain.CommissionRule{SalesGroupID: g.ID, CustomerGroup: "CG", Basis: domain.CommissionRevenue, RatePct: decimal.NewFromInt(9)}, ""); err != nil {
		t.Fatal(err)
	}
	half, created, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: commissionLine("CN-1", "INV-1", "-4000")})
	if err != nil || !created || !sumAmounts(half).Equal(decimal.NewFromInt(-200)) || half[0].ReversesInvoiceLineID != "INV-1" {
		t.Fatalf("partial credit = %+v, %v", half, err)
	}
	rest, _, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: commissionLine("CN-2", "INV-1", "-6000")})
	if err != nil || !sumAmounts(rest).Equal(decimal.NewFromInt(-300)) {
		t.Fatalf("remaining credit = %+v, %v", rest, err)
	}
	if _, _, err := s.PostCommission(ctx, store.CommissionPosting{LegalEntityID: legalEntity, SalesGroupID: g.ID, Line: commissionLine("CN-3", "INV-1", "-0.01")}); problemCode(err) != domain.CodeCommissionCredit {
		t.Fatalf("over-credit = %v; want PRC-0037", err)
	}

	all, err := s.ListCommissionTransactions(ctx, store.CommissionFilter{InvoiceLineID: "INV-1"})
	if err != nil || len(all) != 6 || !sumAmounts(all).IsZero() {
		t.Fatalf("inquiry by line = %d rows summing %s, %v; want 6 netting to zero", len(all), sumAmounts(all), err)
	}
	byRep, err := s.ListCommissionTransactions(ctx, store.CommissionFilter{RepID: "R1", PeriodKey: "2026-03"})
	if err != nil || len(byRep) != 3 || !sumAmounts(byRep).IsZero() {
		t.Fatalf("inquiry by rep and period = %+v, %v", byRep, err)
	}

	var calculated []outboxRow
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == events.CommissionCalculated {
			calculated = append(calculated, row)
		}
	}
	if len(calculated) != 6 {
		t.Fatalf("commission events = %d; want 6", len(calculated))
	}
	first := calculated[0].Data
	if first["sales_rep_id"] != "R1" || first["amount"] != "300" || first["debit_account"] != "6200" || first["invoice_line_id"] != "INV-1" {
		t.Fatalf("event payload = %v", first)
	}
}

// Sales groups, rules and commission rows are tenant- and legal-entity-scoped by RLS.
func TestPostgres_CommissionIsolation(t *testing.T) {
	pool := appPool(t)
	s := store.New(pool)
	g, err := s.CreateSalesGroup(principal(5, ""), store.SalesGroup{LegalEntityID: legalEntity, Name: "SG",
		Reps: []domain.Share{{RepID: "R1", SharePct: decimal.NewFromInt(100)}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSalesGroup(principal(6, ""), g.ID); problemCode(err) != domain.CodeSalesGroupNotFound {
		t.Fatalf("another tenant reading the group = %v; want not found", err)
	}
	if groups, err := s.ListSalesGroups(principal(5, otherLegalEntity), "", 0); err != nil || len(groups) != 0 {
		t.Fatalf("a caller scoped to another legal entity sees %v, %v", groups, err)
	}
	if _, err := s.CreateCommissionRule(principal(6, ""), domain.CommissionRule{SalesGroupID: g.ID, Basis: domain.CommissionRevenue, RatePct: decimal.NewFromInt(1)}, ""); problemCode(err) != domain.CodeSalesGroupNotFound {
		t.Fatalf("another tenant adding a rule = %v; want not found", err)
	}
}

// AC-26.14 against Postgres: an agreement created (activated) today with valid_from in the past
// is retroactive; 50,000.00 USD of invoices posted before it at 2% post one 1,000.00 delta with
// retroactive=true. A line accrued before is not counted twice; a repeated run key returns
// the stored delta and a new run over the same invoices posts nothing.
func TestPostgres_RetroRecompute(t *testing.T) {
	pool := appPool(t)
	ctx := principal(7, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "")
	if err != nil {
		t.Fatal(err)
	}
	// One invoice was accrued through the regular feed (200.00).
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-A", "2026-03-05", "10000")); err != nil {
		t.Fatal(err)
	}
	lines := []store.RetroLine{
		{InvoiceLineID: "INV-A", InvoiceDate: day("2026-03-05"), Value: decimal.NewFromInt(10000), Currency: "USD"},
		{InvoiceLineID: "INV-B", InvoiceDate: day("2026-03-12"), Value: decimal.NewFromInt(40000), Currency: "USD"},
	}
	res, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: a.ID, RunKey: "run-1", Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	if res.ReaccruedLines != 1 || len(res.Deltas) != 1 || res.Deltas[0].Period != "2026-03" || !res.Deltas[0].Delta.Equal(decimal.NewFromInt(800)) ||
		!res.Deltas[0].Target.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("recompute = %+v; want one 800.00 delta to a 1,000.00 target", res)
	}
	acc, err := s.ListAccruals(ctx, store.AccrualFilter{AgreementID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	total, flagged := decimal.Zero, 0
	for _, row := range acc {
		total = total.Add(row.Accrued)
		if row.Retroactive {
			flagged++
		}
	}
	if !total.Equal(decimal.NewFromInt(1000)) || flagged != 2 {
		t.Fatalf("accruals = %+v; want 1,000.00 in total and two retroactive rows", acc)
	}
	var retro []outboxRow
	for _, row := range outboxRows(t, ctx, pool) {
		if row.Type == events.RebateAccrualPosted && row.Data["retroactive"] == true {
			retro = append(retro, row)
		}
	}
	if len(retro) != 1 || retro[0].Data["amount"] != "800" || !strings.HasPrefix(retro[0].Data["invoice_line_id"].(string), "retro:run-1:") {
		t.Fatalf("retroactive events = %+v", retro)
	}

	replay, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: a.ID, RunKey: "run-1", Lines: lines})
	if err != nil || !replay.Replayed || len(replay.Deltas) != 1 || !replay.Deltas[0].Posted.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	again, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: a.ID, RunKey: "run-2", Lines: lines})
	if err != nil || again.ReaccruedLines != 0 || len(again.Deltas) != 0 {
		t.Fatalf("second run = %+v, %v; want nothing", again, err)
	}
	future := store.RetroLine{InvoiceLineID: "INV-F", InvoiceDate: day("2026-12-31"), Value: decimal.NewFromInt(1), Currency: "USD"}
	if res.ActivatedOn.Before(day("2026-12-31")) {
		if _, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: a.ID, RunKey: "run-3", Lines: []store.RetroLine{future}}); problemCode(err) != domain.CodeValidation {
			t.Fatalf("a line after the activation = %v; want refused", err)
		}
	}
	notRetro := agreement(tier("0", "2"))
	notRetro.ValidFrom, notRetro.ValidTo = day("2099-01-01"), day("2099-12-31")
	b, err := s.CreateAgreement(ctx, notRetro, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: b.ID, RunKey: "run-4"}); problemCode(err) != domain.CodeValidation {
		t.Fatalf("a non-retroactive agreement = %v; want refused", err)
	}
}
