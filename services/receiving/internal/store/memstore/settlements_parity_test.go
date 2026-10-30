package memstore_test

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func postedInvoice(t *testing.T, b backend, p tenant.Principal) store.Invoice {
	t.Helper()
	r := registered(t, b.st, p, "PS-1", "S1", "S2", "S3")
	if _, err := b.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-1")); err != nil {
		t.Fatal(err)
	}
	in := store.Invoice{
		ID: ids.New(), LegalEntityID: p.LegalEntityID, VendorID: vendorID, InvoiceNumber: "INV-1", InvoiceDate: day(3, 2), PostingDate: day(3, 2),
		Currency: "USD", LedgerID: locationA, FiscalPeriodID: locationA,
		Lines: []store.InvoiceLine{
			{ReceiptID: r.ID, ReceiptLineNo: 1, Qty: decimal.NewFromInt(2), UnitPrice: decimal.RequireFromString("2.6"), Amount: decimal.RequireFromString("5.2")},
			{ReceiptID: r.ID, ReceiptLineNo: 1, Qty: decimal.NewFromInt(1), UnitPrice: decimal.RequireFromString("2.5"), Amount: decimal.RequireFromString("2.5"), StandardCost: true},
		},
	}
	evt, err := events.New(events.Type("payables", "invoice", "posted", 1), "/passage/receiving/receiving", in.ID, p.TenantID, map[string]string{"invoice": "x"})
	if err != nil {
		t.Fatal(err)
	}
	posted, err := b.st.PostInvoice(ctxOf(p), in, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		items[0].InvoicedQty = items[0].InvoicedQty.Add(decimal.NewFromInt(2))
		items[1].InvoicedQty = items[1].InvoicedQty.Add(decimal.NewFromInt(1))
		items[0].ClearedAmount.Value = items[0].ClearedAmount.Value.Add(decimal.RequireFromString("5"))
		return store.InvoiceOutcome{LedgerEntryID: "inv-entry", Total: decimal.RequireFromString("7.7"), Event: evt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return posted
}

func adjustmentEvent(t *testing.T, p tenant.Principal, id string) events.Event {
	t.Helper()
	evt, err := events.New(events.Type("payables", "invoice", "adjusted", 1), "/passage/receiving/receiving", id, p.TenantID, map[string]string{"adjustment": "x"})
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func adjustment(p tenant.Principal, invoice store.Invoice, kind, number string, lines ...domain.AdjustmentLine) store.InvoiceAdjustment {
	return store.InvoiceAdjustment{
		ID: ids.New(), OriginalInvoiceID: invoice.ID, LegalEntityID: p.LegalEntityID, Kind: kind, DocumentNumber: number,
		DocumentDate: day(3, 5), PostingDate: day(3, 6), LedgerID: locationA, FiscalPeriodID: locationA, Lines: lines,
	}
}

func line(no int, amount string) domain.AdjustmentLine {
	return domain.AdjustmentLine{InvoiceLineNo: no, Amount: decimal.RequireFromString(amount)}
}

var settlementCases = []struct {
	name string
	run  func(*testing.T, backend)
}{
	{"an adjustment sees the invoice lines and earlier credits and debits, stores its own and emits once", func(t *testing.T, b backend) {
		p := principal(1)
		inv := postedInvoice(t, b, p)
		eventsBefore := b.events(p)
		var seen store.InvoiceBase
		first := adjustment(p, inv, domain.AdjustmentCredit, "CN-1", line(1, "1.25"), line(2, "0.50"))
		out, err := b.st.PostInvoiceAdjustment(ctxOf(p), first, func(base store.InvoiceBase) (store.AdjustmentOutcome, error) {
			seen = base
			return store.AdjustmentOutcome{LedgerEntryID: "adj-entry-1", Total: decimal.RequireFromString("1.75"), Event: adjustmentEvent(t, p, first.ID)}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen.Invoice.VendorID != vendorID || seen.Invoice.Currency != "USD" || len(seen.Lines) != 2 || !seen.Lines[0].Amount.Equal(decimal.RequireFromString("5.2")) || seen.Lines[1].LineNo != 2 || !seen.Lines[1].StandardCost ||
			!seen.Lines[0].Credited.IsZero() || !seen.Lines[0].Debited.IsZero() {
			t.Fatalf("first base %+v", seen)
		}
		if out.VendorID != vendorID || out.Currency != "USD" || out.LedgerEntryID != "adj-entry-1" || !out.Total.Equal(decimal.RequireFromString("1.75")) || out.CreatedAt.IsZero() || len(out.Lines) != 2 {
			t.Fatalf("stored adjustment %+v", out)
		}

		second := adjustment(p, inv, domain.AdjustmentDebit, "DN-1", line(1, "0.40"))
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(p), second, func(store.InvoiceBase) (store.AdjustmentOutcome, error) {
			return store.AdjustmentOutcome{LedgerEntryID: "adj-entry-2", Total: decimal.RequireFromString("0.40"), Event: adjustmentEvent(t, p, second.ID)}, nil
		}); err != nil {
			t.Fatal(err)
		}
		third := adjustment(p, inv, domain.AdjustmentCredit, "CN-2", line(1, "0.10"))
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(p), third, func(base store.InvoiceBase) (store.AdjustmentOutcome, error) {
			seen = base
			return store.AdjustmentOutcome{LedgerEntryID: "adj-entry-3", Total: decimal.RequireFromString("0.10"), Event: adjustmentEvent(t, p, third.ID)}, nil
		}); err != nil {
			t.Fatal(err)
		}
		l1, l2 := seen.Lines[0], seen.Lines[1]
		if !l1.Credited.Equal(decimal.RequireFromString("1.25")) || !l1.Debited.Equal(decimal.RequireFromString("0.40")) || !l2.Credited.Equal(decimal.RequireFromString("0.50")) || !l2.Debited.IsZero() {
			t.Fatalf("third base lines %+v %+v: want line 1 credited 1.25 debited 0.40, line 2 credited 0.50", l1, l2)
		}
		if got := b.events(p) - eventsBefore; got != 3 {
			t.Fatalf("emitted %d events, want 3", got)
		}
	}},
	{"a failing adjustment function stores and emits nothing and a duplicate number is refused case-insensitively", func(t *testing.T, b backend) {
		p := principal(1)
		inv := postedInvoice(t, b, p)
		eventsBefore := b.events(p)
		boom := errors.New("ledger down")
		in := adjustment(p, inv, domain.AdjustmentDebit, "DN-1", line(1, "1.00"))
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(p), in, func(store.InvoiceBase) (store.AdjustmentOutcome, error) { return store.AdjustmentOutcome{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("err=%v, want boom", err)
		}
		ok := func(store.InvoiceBase) (store.AdjustmentOutcome, error) {
			return store.AdjustmentOutcome{LedgerEntryID: "e", Total: decimal.NewFromInt(1), Event: adjustmentEvent(t, p, in.ID)}, nil
		}
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(p), in, ok); err != nil {
			t.Fatalf("the same document must post after a failure: %v", err)
		}
		dup := adjustment(p, inv, domain.AdjustmentDebit, "dn-1", line(1, "1.00"))
		_, err := b.st.PostInvoiceAdjustment(ctxOf(p), dup, ok)
		wantCode(t, err, domain.CodeDuplicateInvoice)
		if got := b.events(p) - eventsBefore; got != 1 {
			t.Fatalf("emitted %d events, want 1", got)
		}
	}},
	{"an adjustment needs the original invoice in its own tenant and legal entity", func(t *testing.T, b backend) {
		p := principal(1)
		inv := postedInvoice(t, b, p)
		called := false
		fn := func(store.InvoiceBase) (store.AdjustmentOutcome, error) {
			called = true
			return store.AdjustmentOutcome{}, nil
		}
		missing := adjustment(p, inv, domain.AdjustmentDebit, "DN-1", line(1, "1.00"))
		missing.OriginalInvoiceID = ids.New()
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(p), missing, fn); !errors.Is(err, store.ErrInvoiceNotFound) {
			t.Fatalf("unknown invoice err=%v", err)
		}
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(principal(2)), adjustment(principal(2), inv, domain.AdjustmentDebit, "DN-1", line(1, "1.00")), fn); !errors.Is(err, store.ErrInvoiceNotFound) {
			t.Fatalf("other tenant err=%v", err)
		}
		foreign := adjustment(p, inv, domain.AdjustmentDebit, "DN-2", line(1, "1.00"))
		foreign.LegalEntityID = locationA
		_, err := b.st.PostInvoiceAdjustment(ctxOf(p), foreign, fn)
		wantCode(t, err, domain.CodeInvalidReceipt)
		if called {
			t.Fatal("the function ran although the invoice was not visible")
		}
	}},
	{"a settlement is stored once per source, replays without running again and can be looked up", func(t *testing.T, b backend) {
		p := principal(1)
		eventsBefore := b.events(p)
		evt := func(id string) events.Event {
			e, err := events.New(events.Type("payables", "settlement", "posted", 1), "/passage/receiving/receiving", id, p.TenantID, map[string]string{"s": "x"})
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
		in := store.Settlement{
			ID: ids.New(), Kind: domain.SettlementConsignment, SourceRef: "journal-1", LegalEntityID: p.LegalEntityID, VendorID: "V-1", Currency: "USD",
			Amount: decimal.RequireFromString("1250.00"), PostingDate: day(3, 6), LedgerID: locationA, FiscalPeriodID: locationA,
		}
		boom := errors.New("ledger down")
		if _, _, err := b.st.PostSettlement(ctxOf(p), in, func() (store.SettlementOutcome, error) { return store.SettlementOutcome{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("err=%v, want boom", err)
		}
		if settled, err := b.st.SettledSources(ctxOf(p), domain.SettlementConsignment, []string{"journal-1"}); err != nil || len(settled) != 0 {
			t.Fatalf("a failed settlement is recorded: %v %v", settled, err)
		}

		out, replayed, err := b.st.PostSettlement(ctxOf(p), in, func() (store.SettlementOutcome, error) {
			return store.SettlementOutcome{LedgerEntryID: "set-entry", Event: evt(in.ID)}, nil
		})
		if err != nil || replayed || out.LedgerEntryID != "set-entry" || out.CreatedAt.IsZero() || out.ID != in.ID {
			t.Fatalf("out=%+v replayed=%v err=%v", out, replayed, err)
		}
		again := in
		again.ID = ids.New()
		ran := false
		prior, replayed, err := b.st.PostSettlement(ctxOf(p), again, func() (store.SettlementOutcome, error) {
			ran = true
			return store.SettlementOutcome{}, nil
		})
		if err != nil || !replayed || ran || prior.ID != in.ID || prior.LedgerEntryID != "set-entry" || !prior.Amount.Equal(in.Amount) || prior.VendorID != "V-1" || prior.Kind != in.Kind || prior.SourceRef != "journal-1" {
			t.Fatalf("prior=%+v replayed=%v ran=%v err=%v", prior, replayed, ran, err)
		}
		settled, err := b.st.SettledSources(ctxOf(p), domain.SettlementConsignment, []string{"journal-1", "journal-2"})
		if err != nil || len(settled) != 1 {
			t.Fatalf("settled=%v err=%v", settled, err)
		}
		if _, ok := settled["journal-1"]; !ok {
			t.Fatalf("settled=%v, want journal-1", settled)
		}
		if other, _ := b.st.SettledSources(ctxOf(p), domain.SettlementPlanRow, []string{"journal-1"}); len(other) != 0 {
			t.Fatalf("a consignment source is settled for plan rows: %v", other)
		}
		if other, _ := b.st.SettledSources(ctxOf(principal(2)), domain.SettlementConsignment, []string{"journal-1"}); len(other) != 0 {
			t.Fatalf("another tenant sees the settlement: %v", other)
		}
		if got := b.events(p) - eventsBefore; got != 1 {
			t.Fatalf("emitted %d events, want 1", got)
		}
	}},
}

func TestSettlementStoreParity(t *testing.T) {
	backends := []struct {
		name string
		make func(*testing.T) backend
	}{{"memstore", newMemstore}, {"postgres", newPostgres}}
	for _, be := range backends {
		for _, tc := range settlementCases {
			t.Run(be.name+"/"+tc.name, func(t *testing.T) { tc.run(t, be.make(t)) })
		}
	}
}
