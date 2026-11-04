package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// The rebate statement against Postgres: the period state, the exact settled total, the
// lines in invoice order, a retroactive recompute's rows included, and RLS.
func TestPostgres_RebateStatementInput(t *testing.T) {
	admin, pool := adminAndAppPools(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "1"), tier("5000", "2.5")), "")
	if err != nil {
		t.Fatal(err)
	}
	for i, value := range []string{"3000", "3000"} {
		if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "S"+string(rune('1'+i)), "2026-03-1"+string(rune('0'+i)), value)); err != nil {
			t.Fatal(err)
		}
	}
	// Settlement belongs to w30; until it exists the test marks the row as its consumer would.
	if _, err := admin.Exec(ctx, `update rebate_accrual set status = 'SETTLED' where agreement_id = $1 and invoice_line_id = 'S1'`, a.ID); err != nil {
		t.Fatal(err)
	}
	got, in, err := s.RebateStatementInput(ctx, a.ID, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != a.ID || !in.Period.Value.Equal(decimal.NewFromInt(6000)) || !in.Period.Accrued.Equal(decimal.NewFromInt(150)) {
		t.Fatalf("period state = %+v for %s; want 6000 basis and 150 accrued", in.Period, got.ID)
	}
	if !in.Settled.Equal(decimal.NewFromInt(30)) || in.LineCount != 2 || len(in.Lines) != 2 || in.Lines[0].InvoiceLineID != "S1" || in.Lines[0].InvoiceDate != "2026-03-10" {
		t.Fatalf("settled %s lines %+v count %d; want 30 settled and S1, S2 in invoice order", in.Settled, in.Lines, in.LineCount)
	}
	st := domain.BuildRebateStatement(got, "2026-03", in)
	if !st.Reconciled || !st.Open.Equal(decimal.NewFromInt(120)) {
		t.Fatalf("statement open %s reconciled %v; want 120 and reconciled", st.Open, st.Reconciled)
	}

	_, quiet, err := s.RebateStatementInput(ctx, a.ID, "2026-04")
	if err != nil || len(quiet.Lines) != 0 || !quiet.Period.Accrued.IsZero() {
		t.Fatalf("a period without accruals = %+v, %v; want an empty input", quiet, err)
	}
	if _, _, err := s.RebateStatementInput(ctx, a.ID, "2026-Q1"); problemCode(err) != domain.CodeValidation {
		t.Fatalf("a quarter key on a monthly agreement = %v; want %s", err, domain.CodeValidation)
	}
	if _, _, err := s.RebateStatementInput(principal(2, ""), a.ID, "2026-03"); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("other tenant = %v; want %s", err, store.CodeAgreementNotFound)
	}
	// A caller scoped to another legal entity of the same tenant does not see the agreement (RLS).
	if _, _, err := s.RebateStatementInput(principal(1, otherLegalEntity), a.ID, "2026-03"); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("other legal entity = %v; want %s", err, store.CodeAgreementNotFound)
	}
	if _, _, err := s.RebateStatementInput(principal(1, legalEntity), a.ID, "2026-03"); err != nil {
		t.Fatalf("own legal entity = %v", err)
	}
}

// The rows are the independent side of the reconciliation: their amounts and basis are summed
// over every row (credits included), and an accrual row that drifts from the period shows.
func TestPostgres_RebateStatementRowsReconcile(t *testing.T) {
	admin, pool := adminAndAppPools(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "1"), tier("5000", "2.5")), "")
	if err != nil {
		t.Fatal(err)
	}
	// 3,000 + 3,000 crosses 5,000 (30 then 120); a 2,000 credit drops back to 1% on 4,000 (-110).
	for _, l := range [][3]string{{"R1", "2026-03-02", "3000"}, {"R2", "2026-03-09", "3000"}, {"R3", "2026-03-20", "-2000"}} {
		if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, l[0], l[1], l[2])); err != nil {
			t.Fatal(err)
		}
	}
	got, in, err := s.RebateStatementInput(ctx, a.ID, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if !in.RowsAccrued.Equal(decimal.NewFromInt(40)) || !in.RowsBasisValue.Equal(decimal.NewFromInt(4000)) || !in.Period.Accrued.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("rows %s basis %s period %+v; want 40, 4000 and a period of 40", in.RowsAccrued, in.RowsBasisValue, in.Period)
	}
	if st := domain.BuildRebateStatement(got, "2026-03", in); !st.Reconciled {
		t.Fatalf("statement after a credit = %+v; want reconciled", st)
	}
	if _, err := admin.Exec(ctx, `update rebate_accrual set accrued_amount = accrued_amount + 0.01 where agreement_id = $1 and invoice_line_id = 'R2'`, a.ID); err != nil {
		t.Fatal(err)
	}
	got, in, err = s.RebateStatementInput(ctx, a.ID, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	if st := domain.BuildRebateStatement(got, "2026-03", in); st.Reconciled || !st.RowsAccrued.Equal(decimal.RequireFromString("40.01")) {
		t.Fatalf("a drifted row = rows %s reconciled %v; want 40.01 and not reconciled", st.RowsAccrued, st.Reconciled)
	}
}

// The audit extract read: the stored before and after values of a posted journal, line by
// line in one transaction, refused by the start callback before any line is read, and
// invisible to another tenant or legal entity.
func TestPostgres_StreamJournal(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	raised := activePrice(t, ctx, s, "C1", "7.41")
	j, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Audit", Lines: []domain.JournalLine{adjust(raised.ID, 2, "5")}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateJournal(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, ""); err != nil {
		t.Fatal(err)
	}
	// A later change to the record leaves the stored before and after of the journal as posted.
	if _, err := s.WithdrawRecord(ctx, raised.ID, 3, "audit test", ""); err != nil {
		t.Fatal(err)
	}
	var head store.Journal
	var lines []domain.JournalLine
	err = s.StreamJournal(ctx, j.ID, func(h store.Journal) error { head = h; return nil }, func(l domain.JournalLine) error {
		lines = append(lines, l)
		return nil
	})
	if err != nil || head.Status != domain.JournalPosted || len(head.Lines) != 0 || len(lines) != 1 {
		t.Fatalf("stream = %v, head %+v, %d lines; want POSTED and one line", err, head, len(lines))
	}
	if !lines[0].Before.Amount.Equal(decimal.RequireFromString("7.41")) || !lines[0].After.Amount.Equal(decimal.RequireFromString("7.7805")) ||
		lines[0].After.Status != domain.StatusActive {
		t.Fatalf("line = before %+v after %+v; want the values stored at post", lines[0].Before, lines[0].After)
	}
	refused := errors.New("refused")
	read := 0
	if err := s.StreamJournal(ctx, j.ID, func(store.Journal) error { return refused }, func(domain.JournalLine) error { read++; return nil }); !errors.Is(err, refused) || read != 0 {
		t.Fatalf("refused start = %v after %d lines; want the start error and no line read", err, read)
	}
	noop := func(store.Journal) error { return nil }
	for name, other := range map[string]context.Context{"other tenant": principal(2, ""), "other legal entity": principal(1, otherLegalEntity)} {
		if err := s.StreamJournal(other, j.ID, noop, func(domain.JournalLine) error { return nil }); problemCode(err) != store.CodeJournalNotFound {
			t.Errorf("%s = %v; want %s", name, err, store.CodeJournalNotFound)
		}
	}
}

// A retroactive recompute replays a line into the period basis (accruing nothing on its own row)
// and posts the period delta on a row that stores the cumulative basis. The statement counts
// the delta's amount but not its basis, so the period still reconciles after the tier crossing.
func TestPostgres_RebateStatementAfterRetroRecompute(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "1"), tier("20000", "2")), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-A", "2026-03-05", "10000")); err != nil {
		t.Fatal(err)
	}
	lines := []store.RetroLine{{InvoiceLineID: "INV-B", InvoiceDate: day("2026-03-12"), Value: decimal.NewFromInt(40000), Currency: "USD"}}
	if _, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: a.ID, RunKey: "run-1", Lines: lines}); err != nil {
		t.Fatal(err)
	}
	got, in, err := s.RebateStatementInput(ctx, a.ID, "2026-03")
	if err != nil {
		t.Fatal(err)
	}
	// 50,000 at 2% = 1,000: 100 on INV-A, 0 on the replayed INV-B, 900 on the delta.
	if !in.RowsAccrued.Equal(decimal.NewFromInt(1000)) || !in.RowsBasisValue.Equal(decimal.NewFromInt(50000)) || in.LineCount != 3 {
		t.Fatalf("rows %s basis %s count %d; want 1000, 50000 and three rows", in.RowsAccrued, in.RowsBasisValue, in.LineCount)
	}
	sources := map[string]string{}
	for _, l := range in.Lines {
		sources[l.InvoiceLineID] = l.Source()
	}
	if sources["INV-A"] != domain.SourceInvoice || sources["INV-B"] != domain.SourceReplayed || sources["retro:run-1:2026-03"] != domain.SourceRetroDelta {
		t.Fatalf("sources = %v", sources)
	}
	if st := domain.BuildRebateStatement(got, "2026-03", in); !st.Reconciled || !st.Target.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("statement = target %s reconciled %v; want 1000 and reconciled", st.Target, st.Reconciled)
	}
}
