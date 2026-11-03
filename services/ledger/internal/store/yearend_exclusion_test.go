package store

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func serviceCtx(tenantID, legalEntityID, service string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, UserID: "svc", Service: service, Duties: testkit.ScopeDuties(legalEntityID)})
}

func closingEntry(ledgerID, key, amount string) PostInput {
	in := vendorInvoice(ledgerID, key, amount)
	in.Source = ledgerevents.Source{System: "close", Type: YearEndClosingSourceType, ID: key}
	in.Lines = []domain.LineInput{
		{Account: "6010", Side: domain.SideCredit, Amount: decimal.RequireFromString(amount), Currency: "USD"},
		{Account: "2100", Side: domain.SideDebit, Amount: decimal.RequireFromString(amount), Currency: "USD"},
	}
	in.DocumentID = ids.New()
	return in
}

func TestPostgres_ExcludeYearEndClosingTrustsOnlyTheCloseService(t *testing.T) {
	e, _ := setup(t)
	closeCtx := serviceCtx(e.tenant, e.le, ClosePostingService)
	expense := func(key string) {
		t.Helper()
		if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, key, "100.00")); err != nil {
			t.Fatal(err)
		}
	}
	expense("EXP-1")
	first, err := e.store.PostEntry(closeCtx, closingEntry(e.ledger.ID, "CLOSE-1", "500.00"))
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := domain.HashPayload("reverse CLOSE-1")
	if _, err := e.store.ReverseEntry(closeCtx, ReverseInput{EntryID: first.Entries[0].ID, Key: "REV-CLOSE-1", Hash: hash, Actor: "svc", TraceID: testTrace, Today: testToday}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.PostEntry(closeCtx, closingEntry(e.ledger.ID, "CLOSE-2", "500.00")); err != nil {
		t.Fatal(err)
	}
	spoof := vendorInvoice(e.ledger.ID, "SPOOF-1", "70.00")
	spoof.Source.Type = YearEndClosingSourceType
	if _, err := e.store.PostEntry(e.ctx, spoof); err != nil {
		t.Fatal(err)
	}
	spoofedAsClose := closingEntry(e.ledger.ID, "SPOOF-2", "30.00")
	if _, err := e.store.PostEntry(serviceCtx(e.tenant, e.le, "receiving"), spoofedAsClose); err != nil {
		t.Fatal(err)
	}

	row := func(tb TrialBalance, account string) BalanceRow {
		t.Helper()
		for _, r := range tb.Rows {
			if r.Account == account {
				return r
			}
		}
		t.Fatalf("no row for %s in %+v", account, tb)
		return BalanceRow{}
	}
	all, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, PeriodFrom: "2026-03", PeriodTo: "2026-03"})
	if err != nil || all.ExcludesYearEndClosing || row(all, "6010").Credit != "1030.00" {
		t.Fatalf("without the option every entry counts: %+v %v", all, err)
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID, PeriodFrom: "2026-03", PeriodTo: "2026-03", ExcludeYearEndClosing: true})
	if err != nil || !tb.ExcludesYearEndClosing || !tb.Balanced {
		t.Fatalf("excluded balance: %+v %v", tb, err)
	}
	got := row(tb, "6010")
	if got.Debit != "170.00" || got.Credit != "30.00" || got.Net != "140.00" || got.LineCount != 3 {
		t.Fatalf("a re-run keeps nothing of the close entries, a spoofed closing entry stays in the cost: %+v", got)
	}
	if row(tb, "2100").Net != "-140.00" || tb.Net != "0.00" {
		t.Fatalf("totals must follow the rows: %+v", tb)
	}
}
