package store

import (
	"testing"
)

func statusesOf(ps []VendorPayment) map[string]string {
	out := map[string]string{}
	for _, p := range ps {
		out[p.InvoiceNumber] = p.Status
	}
	return out
}

func TestVendorPayments_AVendorSeesItsOwnInvoicesWithPaymentStatusAndNothingElse(t *testing.T) {
	s := setup(t)
	ctx := ctxAs(1, leA, "portal")
	open := seed(t, s, ctx, leA, vendor1, 1, "2027-03-10", "USD", "100.00")
	partly := seed(t, s, ctx, leA, vendor1, 2, "2027-03-11", "USD", "200.00")
	other := seed(t, s, ctx, leA, vendor2, 3, "2027-03-12", "EUR", "300.00")
	if _, err := s.Settle(ctx, partly.ID, partly.Version, dec("50.00"), "settlement-1"); err != nil {
		t.Fatal(err)
	}

	mine, err := s.VendorPayments(ctx, vendor1, VendorPaymentFilter{Limit: 50})
	if err != nil || len(mine) != 2 {
		t.Fatalf("vendor 1 sees %+v %v, want its two invoices", mine, err)
	}
	st := statusesOf(mine)
	if st[open.InvoiceNumber] != PaymentOpen || st[partly.InvoiceNumber] != PaymentPartiallyPaid {
		t.Errorf("statuses %v", st)
	}
	for _, p := range mine {
		if p.InvoiceNumber == partly.InvoiceNumber && (!p.PaidAmount.Equal(dec("50")) || !p.OpenAmount.Equal(dec("150")) || p.PaidAt == "") {
			t.Errorf("partly paid invoice: %+v", p)
		}
		if p.InvoiceNumber == open.InvoiceNumber && (p.PaidAt != "" || p.ScheduledFor != "" || p.InvoiceID != open.SourceInvoiceID) {
			t.Errorf("open invoice: %+v", p)
		}
	}
	if theirs, _ := s.VendorPayments(ctx, vendor2, VendorPaymentFilter{Limit: 50}); len(theirs) != 1 || theirs[0].InvoiceNumber != other.InvoiceNumber {
		t.Errorf("vendor 2 sees %+v", theirs)
	}
	if none, _ := s.VendorPayments(ctx, "00000000-0000-0000-0000-0000000000c9", VendorPaymentFilter{}); len(none) != 0 {
		t.Errorf("an unknown vendor sees %+v", none)
	}
	// Another legal entity's caller sees nothing of this vendor, and another tenant neither.
	for name, c := range map[string]*struct {
		n  int
		le string
	}{"other legal entity": {1, leB}, "other tenant": {2, leA}} {
		if got, _ := s.VendorPayments(ctxAs(c.n, c.le, "x"), vendor1, VendorPaymentFilter{}); len(got) != 0 {
			t.Errorf("%s sees %d invoices of vendor 1", name, len(got))
		}
	}
	// A status filter and a page of one.
	if paid, _ := s.VendorPayments(ctx, vendor1, VendorPaymentFilter{Status: PaymentPartiallyPaid}); len(paid) != 1 || paid[0].InvoiceNumber != partly.InvoiceNumber {
		t.Errorf("status filter: %+v", paid)
	}
	page, _ := s.VendorPayments(ctx, vendor1, VendorPaymentFilter{Limit: 1})
	if len(page) != 2 {
		t.Fatalf("a page of one reads two rows so the caller sees a next page, got %d", len(page))
	}
	date, id := page[0].Cursor()
	next, _ := s.VendorPayments(ctx, vendor1, VendorPaymentFilter{Limit: 1, CursorDate: date, CursorID: id})
	if len(next) != 1 || next[0].InvoiceNumber == page[0].InvoiceNumber {
		t.Errorf("second page: %+v", next)
	}
}

func TestVendorPayments_ScheduledWhileAnApprovedProposalHoldsItAndPaidWhenTheRunSettles(t *testing.T) {
	s := setup(t)
	proposal, erin := approvedProposal(t, s, "run-1", threeItems(t, s))
	before, _ := s.VendorPayments(erin, vendor1, VendorPaymentFilter{Limit: 50})
	if len(before) != 2 {
		t.Fatalf("vendor 1 has %d invoices", len(before))
	}
	for _, p := range before {
		if p.Status != PaymentScheduled || p.ScheduledFor == "" {
			t.Errorf("approved but not paid: %+v", p)
		}
	}
	run, _, err := s.StartRun(erin, runInput(proposal.ID, leA))
	if err != nil {
		t.Fatal(err)
	}
	runThrough(t, s, erin, run, proposal)
	after, _ := s.VendorPayments(erin, vendor1, VendorPaymentFilter{Limit: 50})
	for _, p := range after {
		if p.Status != PaymentPaid || p.PaidAt == "" || !p.OpenAmount.IsZero() || p.ScheduledFor != "" {
			t.Errorf("settled by the run: %+v", p)
		}
	}
	if paid, _ := s.VendorPayments(erin, vendor2, VendorPaymentFilter{Status: PaymentPaid}); len(paid) != 1 {
		t.Errorf("vendor 2 paid: %+v", paid)
	}
}
