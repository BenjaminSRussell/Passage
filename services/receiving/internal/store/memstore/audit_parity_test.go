package memstore_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func invoiceEvent(t *testing.T, p tenant.Principal, verb, id string) events.Event {
	t.Helper()
	evt, err := events.New(events.Type("payables", "invoice", verb, 1), "/passage/receiving/receiving", id, p.TenantID, map[string]string{"invoice": id})
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func blockedInvoice(t *testing.T, b backend, p tenant.Principal, postedBy string) store.Invoice {
	t.Helper()
	r := registered(t, b.st, p, "PS-1", "S1", "S2", "S3")
	if _, err := b.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-1")); err != nil {
		t.Fatal(err)
	}
	in := store.Invoice{
		ID: ids.New(), LegalEntityID: p.LegalEntityID, VendorID: vendorID, InvoiceNumber: "ERS-RCV-1", InvoiceDate: day(3, 2), PostingDate: day(3, 2),
		Currency: "USD", LedgerID: locationA, FiscalPeriodID: locationA, Source: domain.InvoiceSourceERS, PostedBy: postedBy,
		Lines: []store.InvoiceLine{{ReceiptID: r.ID, ReceiptLineNo: 1, Qty: decimal.NewFromInt(3), UnitPrice: decimal.RequireFromString("2.6"), Amount: decimal.RequireFromString("7.8")}},
	}
	held, err := b.st.PostInvoice(ctxOf(p), in, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		items[0].InvoicedQty = items[0].InvoicedQty.Add(decimal.NewFromInt(3))
		items[0].ClearedAmount.Value = items[0].ClearedAmount.Value.Add(decimal.RequireFromString("7.5"))
		return store.InvoiceOutcome{Total: decimal.RequireFromString("7.8"), Blocked: true, Clearings: []decimal.Decimal{decimal.RequireFromString("7.5")}, Event: invoiceEvent(t, p, "sampled", in.ID)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func policyEvent(t *testing.T, p tenant.Principal) events.Event {
	t.Helper()
	return invoiceEvent(t, p, "sampled", p.LegalEntityID)
}

func setRate(t *testing.T, b backend, p tenant.Principal, entity, rate, by string) (old decimal.Decimal) {
	t.Helper()
	err := b.st.SetAuditRate(ctxOf(p), entity, decimal.RequireFromString(rate), by, func(o decimal.Decimal) (events.Event, error) {
		old = o
		return policyEvent(t, p), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return old
}

func setErs(t *testing.T, b backend, p tenant.Principal, entity, vendor string, enabled bool, by string) (old bool) {
	t.Helper()
	err := b.st.SetErsVendor(ctxOf(p), entity, vendor, enabled, by, func(o bool) (events.Event, error) {
		old = o
		return policyEvent(t, p), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return old
}

func reject(b backend, p tenant.Principal, inv store.Invoice, by, reason string) (store.Invoice, error) {
	return b.st.RejectInvoice(ctxOf(p), inv.ID, inv.LegalEntityID, by, reason, func(held store.Invoice) (events.Event, error) {
		return events.New(events.Type("payables", "invoice", "rejected", 1), "/passage/receiving/receiving", held.ID, p.TenantID, map[string]string{"invoice": held.ID})
	})
}

func release(t *testing.T, b backend, p tenant.Principal, inv store.Invoice, by string, seen *store.Invoice) (store.Invoice, error) {
	t.Helper()
	return b.st.ReleaseInvoice(ctxOf(p), inv.ID, inv.LegalEntityID, by, func(held store.Invoice) (store.InvoiceOutcome, error) {
		if seen != nil {
			*seen = held
		}
		return store.InvoiceOutcome{LedgerEntryID: "rel-entry", Total: held.Total, Event: invoiceEvent(t, p, "posted", held.ID)}, nil
	})
}

var auditCases = []struct {
	name string
	run  func(*testing.T, backend)
}{
	{"a drawn invoice is stored blocked without a ledger entry, reserves its GR/IR quantity and emits the sampled event once", func(t *testing.T, b backend) {
		p := principal(1)
		eventsBefore := b.events(p)
		held := blockedInvoice(t, b, p, "alice")
		if held.Status != domain.InvoiceStatusAuditBlocked || held.LedgerEntryID != "" || held.Source != domain.InvoiceSourceERS || held.PostedBy != "alice" || !held.Lines[0].GrirCleared.Equal(decimal.RequireFromString("7.5")) {
			t.Fatalf("held %+v", held)
		}
		page, err := b.st.ListGrir(ctxOf(p), store.GrirFilter{})
		if err != nil || len(page.Items) != 1 || page.Items[0].Status != "CLEARED" || !page.Items[0].InvoicedQty.Equal(decimal.NewFromInt(3)) {
			t.Fatalf("grir %+v err=%v", page, err)
		}
		if got := b.events(p) - eventsBefore; got != 2 {
			t.Fatalf("emitted %d events (receipt posted and invoice sampled), want 2", got)
		}
	}},
	{"release posts once with the stored clearings, records who and when, and refuses a second release", func(t *testing.T, b backend) {
		p := principal(1)
		held := blockedInvoice(t, b, p, "alice")
		eventsBefore := b.events(p)
		var seen store.Invoice
		out, err := release(t, b, p, held, "bob", &seen)
		if err != nil {
			t.Fatal(err)
		}
		if seen.Status != domain.InvoiceStatusAuditBlocked || len(seen.Lines) != 1 || seen.Lines[0].ReceiptLineNo != 1 || !seen.Lines[0].Qty.Equal(decimal.NewFromInt(3)) ||
			!seen.Lines[0].UnitPrice.Equal(decimal.RequireFromString("2.6")) || !seen.Lines[0].GrirCleared.Equal(decimal.RequireFromString("7.5")) {
			t.Fatalf("function saw %+v", seen)
		}
		if out.Status != domain.InvoiceStatusPosted || out.LedgerEntryID != "rel-entry" || out.ReleasedBy != "bob" || out.ReleasedAt == nil || out.ReleasedAt.IsZero() {
			t.Fatalf("released %+v", out)
		}
		if got := b.events(p) - eventsBefore; got != 1 {
			t.Fatalf("emitted %d events, want 1", got)
		}
		_, err = release(t, b, p, held, "carol", nil)
		wantCode(t, err, domain.CodeNotAuditBlocked)
		if got := b.events(p) - eventsBefore; got != 1 {
			t.Fatalf("a second release emitted: %d events", got)
		}
	}},
	{"release refuses the poster, a foreign legal entity, an unknown invoice and another tenant, and a failing function keeps it blocked", func(t *testing.T, b backend) {
		p := principal(1)
		held := blockedInvoice(t, b, p, "alice")
		eventsBefore := b.events(p)
		_, err := release(t, b, p, held, "alice", nil)
		wantCode(t, err, domain.CodeAuditSelfRelease)
		foreign := held
		foreign.LegalEntityID = locationA
		_, err = release(t, b, p, foreign, "bob", nil)
		wantCode(t, err, domain.CodeInvalidReceipt)
		unknown := held
		unknown.ID = ids.New()
		if _, err := release(t, b, p, unknown, "bob", nil); !errors.Is(err, store.ErrInvoiceNotFound) {
			t.Fatalf("unknown invoice err=%v", err)
		}
		if _, err := release(t, b, principal(2), held, "bob", nil); !errors.Is(err, store.ErrInvoiceNotFound) {
			t.Fatalf("other tenant err=%v", err)
		}
		boom := errors.New("ledger down")
		if _, err := b.st.ReleaseInvoice(ctxOf(p), held.ID, held.LegalEntityID, "bob", func(store.Invoice) (store.InvoiceOutcome, error) { return store.InvoiceOutcome{}, boom }); !errors.Is(err, boom) {
			t.Fatalf("err=%v, want boom", err)
		}
		if got := b.events(p) - eventsBefore; got != 0 {
			t.Fatalf("emitted %d events", got)
		}
		if _, err := release(t, b, p, held, "bob", nil); err != nil {
			t.Fatalf("release after the failure: %v", err)
		}
	}},
	{"a blocked invoice cannot be adjusted until released", func(t *testing.T, b backend) {
		p := principal(1)
		held := blockedInvoice(t, b, p, "alice")
		ok := func(store.InvoiceBase) (store.AdjustmentOutcome, error) {
			return store.AdjustmentOutcome{LedgerEntryID: "adj", Total: decimal.NewFromInt(1), Event: adjustmentEvent(t, p, held.ID)}, nil
		}
		_, err := b.st.PostInvoiceAdjustment(ctxOf(p), adjustment(p, held, domain.AdjustmentDebit, "DN-1", line(1, "1.00")), ok)
		wantCode(t, err, domain.CodeAuditBlocked)
		if _, err := release(t, b, p, held, "bob", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := b.st.PostInvoiceAdjustment(ctxOf(p), adjustment(p, held, domain.AdjustmentDebit, "DN-1", line(1, "1.00")), ok); err != nil {
			t.Fatalf("adjustment after release: %v", err)
		}
	}},
	{"ERS vendor flags and audit rates default off, are kept per legal entity and vendor, and are private to the tenant", func(t *testing.T, b backend) {
		p := principal(1)
		ctx := ctxOf(p)
		if on, err := b.st.ErsVendor(ctx, p.LegalEntityID, vendorID); err != nil || on {
			t.Fatalf("default ers=%v err=%v", on, err)
		}
		if rate, err := b.st.AuditRate(ctx, p.LegalEntityID); err != nil || !rate.IsZero() {
			t.Fatalf("default rate=%s err=%v", rate, err)
		}
		eventsBefore := b.events(p)
		if old := setErs(t, b, p, p.LegalEntityID, vendorID, true, "alice"); old {
			t.Fatal("the flag started on")
		}
		if old := setRate(t, b, p, p.LegalEntityID, "12.50", "alice"); !old.IsZero() {
			t.Fatalf("the rate started at %s", old)
		}
		for _, tc := range []struct {
			name   string
			ctx    tenant.Principal
			entity string
			vendor string
			ers    bool
			rate   string
		}{
			{"same entity and vendor", p, p.LegalEntityID, vendorID, true, "12.5"},
			{"other vendor", p, p.LegalEntityID, poID, false, "12.5"},
			{"other entity", p, locationA, vendorID, false, "0"},
			{"other tenant", principal(2), p.LegalEntityID, vendorID, false, "0"},
		} {
			on, err := b.st.ErsVendor(ctxOf(tc.ctx), tc.entity, tc.vendor)
			rate, rateErr := b.st.AuditRate(ctxOf(tc.ctx), tc.entity)
			if err != nil || rateErr != nil || on != tc.ers || !rate.Equal(decimal.RequireFromString(tc.rate)) {
				t.Fatalf("%s: ers=%v (%v) rate=%s (%v)", tc.name, on, err, rate, rateErr)
			}
		}
		if old := setErs(t, b, p, p.LegalEntityID, vendorID, false, "bob"); !old {
			t.Fatal("the builder was not told the flag was on")
		}
		if old := setRate(t, b, p, p.LegalEntityID, "0", "bob"); !old.Equal(decimal.RequireFromString("12.5")) {
			t.Fatalf("the builder saw old rate %s, want 12.5", old)
		}
		if got := b.events(p) - eventsBefore; got != 4 {
			t.Fatalf("emitted %d policy events, want 4", got)
		}
		if on, _ := b.st.ErsVendor(ctx, p.LegalEntityID, vendorID); on {
			t.Fatal("the flag was not turned off")
		}
		if rate, _ := b.st.AuditRate(ctx, p.LegalEntityID); !rate.IsZero() {
			t.Fatalf("rate %s after reset", rate)
		}
	}},
	{"release and reject fail closed when the poster is unknown and compare users by canonical id", func(t *testing.T, b backend) {
		p := principal(1)
		anonymous := blockedInvoice(t, b, p, "")
		_, err := release(t, b, p, anonymous, "bob", nil)
		wantCode(t, err, domain.CodeAuditSelfRelease)
		_, err = reject(b, p, anonymous, "bob", "no poster")
		wantCode(t, err, domain.CodeAuditSelfRelease)

		b2 := newMemstore(t)
		poster := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		held := blockedInvoice(t, b2, p, poster)
		_, err = release(t, b2, p, held, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", nil)
		wantCode(t, err, domain.CodeAuditSelfRelease)
		_, err = reject(b2, p, held, " Aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa ", "same person")
		wantCode(t, err, domain.CodeAuditSelfRelease)
	}},
	{"reject frees the GR/IR quantity, records who when and why, emits once and excludes release and adjustment", func(t *testing.T, b backend) {
		p := principal(1)
		held := blockedInvoice(t, b, p, "alice")
		eventsBefore := b.events(p)
		out, err := reject(b, p, held, "bob", "duplicate of INV-0")
		if err != nil {
			t.Fatal(err)
		}
		if out.Status != domain.InvoiceStatusAuditRejected || out.RejectedBy != "bob" || out.RejectReason != "duplicate of INV-0" || out.RejectedAt == nil || out.LedgerEntryID != "" {
			t.Fatalf("rejected %+v", out)
		}
		page, err := b.st.ListGrir(ctxOf(p), store.GrirFilter{})
		if err != nil || len(page.Items) != 1 || page.Items[0].Status != "OPEN" || !page.Items[0].InvoicedQty.IsZero() || !page.Items[0].ClearedAmount.IsZero() {
			t.Fatalf("grir %+v err=%v", page, err)
		}
		if got := b.events(p) - eventsBefore; got != 1 {
			t.Fatalf("emitted %d events, want 1", got)
		}
		_, err = release(t, b, p, held, "carol", nil)
		wantCode(t, err, domain.CodeNotAuditBlocked)
		_, err = reject(b, p, held, "carol", "again")
		wantCode(t, err, domain.CodeNotAuditBlocked)
		_, err = b.st.PostInvoiceAdjustment(ctxOf(p), adjustment(p, held, domain.AdjustmentDebit, "DN-1", line(1, "1.00")), func(store.InvoiceBase) (store.AdjustmentOutcome, error) {
			return store.AdjustmentOutcome{}, nil
		})
		wantCode(t, err, domain.CodeAuditBlocked)
		again := store.Invoice{
			ID: ids.New(), LegalEntityID: p.LegalEntityID, VendorID: vendorID, InvoiceNumber: "INV-AGAIN", InvoiceDate: day(3, 2), PostingDate: day(3, 2), Currency: "USD", LedgerID: locationA, FiscalPeriodID: locationA,
			Lines: held.Lines,
		}
		if _, err := b.st.PostInvoice(ctxOf(p), again, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
			if !items[0].InvoicedQty.IsZero() || !items[0].ClearedAmount.Value.IsZero() {
				t.Fatalf("freed item still reads %+v", items[0])
			}
			return store.InvoiceOutcome{LedgerEntryID: "again", Total: decimal.RequireFromString("7.8"), Event: invoiceEvent(t, p, "posted", again.ID)}, nil
		}); err != nil {
			t.Fatalf("invoicing the freed quantity: %v", err)
		}
	}},
	{"reject refuses a foreign legal entity, an unknown invoice and another tenant", func(t *testing.T, b backend) {
		p := principal(1)
		held := blockedInvoice(t, b, p, "alice")
		foreign := held
		foreign.LegalEntityID = locationA
		_, err := reject(b, p, foreign, "bob", "x")
		wantCode(t, err, domain.CodeInvalidReceipt)
		unknown := held
		unknown.ID = ids.New()
		if _, err := reject(b, p, unknown, "bob", "x"); !errors.Is(err, store.ErrInvoiceNotFound) {
			t.Fatalf("unknown err=%v", err)
		}
		if _, err := reject(b, principal(2), held, "bob", "x"); !errors.Is(err, store.ErrInvoiceNotFound) {
			t.Fatalf("other tenant err=%v", err)
		}
	}},
	{"ids are matched in canonical form, and rejected invoice numbers are counted for the ERS suffix", func(t *testing.T, b backend) {
		p := principal(1)
		held := blockedInvoice(t, b, p, "alice")
		upper := held
		upper.ID = strings.ToUpper(held.ID)
		if n, err := b.st.RejectedInvoiceCount(ctxOf(p), vendorID, "ERS-RCV-1"); err != nil || n != 0 {
			t.Fatalf("count before %d err=%v", n, err)
		}
		if _, err := reject(b, p, upper, "bob", "dup"); err != nil {
			t.Fatalf("reject by upper-case id: %v", err)
		}
		for number, want := range map[string]int{"ERS-RCV-1": 1, "ers-rcv-1": 1, "ERS-RCV-2": 0, "ERS-RCV": 0} {
			if n, err := b.st.RejectedInvoiceCount(ctxOf(p), vendorID, number); err != nil || n != want {
				t.Fatalf("count of %s = %d err=%v, want %d", number, n, err, want)
			}
		}
		if n, _ := b.st.RejectedInvoiceCount(ctxOf(p), poID, "ERS-RCV-1"); n != 0 {
			t.Fatalf("another vendor's count %d", n)
		}
		if n, _ := b.st.RejectedInvoiceCount(ctxOf(principal(2)), vendorID, "ERS-RCV-1"); n != 0 {
			t.Fatalf("another tenant's count %d", n)
		}
	}},
}

func TestAuditStoreParity(t *testing.T) {
	backends := []struct {
		name string
		make func(*testing.T) backend
	}{{"memstore", newMemstore}, {"postgres", newPostgres}}
	for _, be := range backends {
		for _, tc := range auditCases {
			t.Run(be.name+"/"+tc.name, func(t *testing.T) { tc.run(t, be.make(t)) })
		}
	}
}
