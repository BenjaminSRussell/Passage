package store

import (
	"net/http"
	"sort"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/taxclient"
)

var taxAccounts = map[string]map[string]string{legalEntity: {"DE19": "2310", "DE7": "2320"}}

const billingInvoiceID = "00000000-0000-0000-0000-0000000000b1"

func billedAs(s *Postgres, remaining map[string]decimal.Decimal) {
	bill := billingclient.NewFake()
	bill.Set(billingInvoiceID, billingclient.Basis{Number: "INV-30001", LegalEntityID: legalEntity, Remaining: remaining})
	s.WithBilling(bill)
}

func rmaTaxSetup(t *testing.T) (*Postgres, *failingLedger, *taxclient.Fake) {
	t.Helper()
	s, gl, _ := rmaSetup(t)
	tax := taxclient.NewFake()
	s.WithRMATax(taxAccounts, tax)
	billedAs(s, map[string]decimal.Decimal{"DE19": amt("500.00"), "DE7": amt("20.00")})
	return s, gl, tax
}

func taxedIn(invoice string) RMACredit {
	in := rmaIn(invoice)
	in.InvoiceID = billingInvoiceID
	in.Gross, in.Restocking, in.Net = amt("300.00"), amt("45.00"), amt("255.00")
	in.TaxLines = []RMATaxLine{{Code: "DE19", Amount: amt("57.00")}, {Code: "DE7", Amount: amt("7.00")}}
	return in
}

func TestPostRMACredit_ReversesTheInvoiceTaxInTheGLAndWithTheTaxService(t *testing.T) {
	s, gl, tax := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := taxedIn(inv.DocumentNumber)
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || !note.Net.Equal(amt("255.00")) || !note.Tax.Equal(amt("64.00")) {
		t.Fatalf("post: %+v %v", note, err)
	}
	entries := gl.Entries()
	if len(entries) != 1 || len(entries[0].Lines) != 5 {
		t.Fatalf("journal %+v", entries)
	}
	want := []struct {
		account string
		credit  bool
		amount  string
	}{{domain.AccountSalesReturns, false, "300.00"}, {"2310", false, "57.00"}, {"2320", false, "7.00"},
		{domain.AccountRestocking, true, "45.00"}, {domain.AccountReceivable, true, "319.00"}}
	debits, credits := decimal.Zero, decimal.Zero
	for i, l := range entries[0].Lines {
		if l.Account != want[i].account || l.Credit != want[i].credit || !l.Amount.Equal(amt(want[i].amount)) {
			t.Fatalf("GL line %d: %+v, want %+v", i, l, want[i])
		}
		if l.Credit {
			credits = credits.Add(l.Amount)
		} else {
			debits = debits.Add(l.Amount)
		}
	}
	if !debits.Equal(credits) {
		t.Fatalf("journal unbalanced: Dr %s Cr %s", debits, credits)
	}
	items := creditItems(t, s, ctx, inv.CustomerID)
	if len(items) != 1 || !items[0].Amount.Equal(amt("-319.00")) {
		t.Fatalf("the customer is credited net + tax: %+v", items)
	}
	recorded := tax.Recorded()
	sort.Slice(recorded, func(i, j int) bool { return recorded[i].Code < recorded[j].Code })
	if len(recorded) != 2 || recorded[0].Code != "DE19" || !recorded[0].Amount.Equal(amt("-57.00")) || recorded[1].Code != "DE7" || !recorded[1].Amount.Equal(amt("-7.00")) ||
		recorded[0].Date != "2027-03-01" || tax.LE[recorded[0].ID] != legalEntity {
		t.Fatalf("tax service transactions %+v", recorded)
	}

	replay, err := s.PostRMACredit(ctx, in)
	if err != nil || !replay.Tax.Equal(amt("64.00")) || replay.OpenItemID != note.OpenItemID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if len(gl.Entries()) != 1 || len(tax.Recorded()) != 2 || countOutbox(t, s, ctx, rcvevents.CreditNotePosted) != 1 {
		t.Fatalf("a replay posts and records nothing again: %d journals, %d tax transactions", len(gl.Entries()), len(tax.Recorded()))
	}
	changed := in
	changed.TaxLines = []RMATaxLine{{Code: "DE19", Amount: amt("58.00")}, {Code: "DE7", Amount: amt("7.00")}}
	if _, err := s.PostRMACredit(ctx, changed); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("other tax under the same RMA: %v", err)
	}
}

func TestPostRMACredit_UntaxedCreditNeedsNoTaxServiceOrAccounts(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	note, err := s.PostRMACredit(ctx, rmaIn(inv.DocumentNumber))
	if err != nil || !note.Tax.IsZero() || len(gl.Entries()[0].Lines) != 3 {
		t.Fatalf("untaxed: %+v %v %+v", note, err, gl.Entries())
	}
}

func TestPostRMACredit_TaxWithoutAccountOrTaxServiceIs503AndStoresNothing(t *testing.T) {
	ctx := tenantCtx(1)
	s, gl, _ := rmaSetup(t)
	inv := seedInvoice(t, s, ctx)
	if _, err := s.PostRMACredit(ctx, taxedIn(inv.DocumentNumber)); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("no tax service: %v", err)
	}
	s.WithRMATax(map[string]map[string]string{legalEntity: {"DE19": "2310"}}, taxclient.NewFake())
	if _, err := s.PostRMACredit(ctx, taxedIn(inv.DocumentNumber)); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("DE7 has no pinned account: %v", err)
	}
	pending, err := s.ListRMACredits(ctx, RMACreditFilter{Limit: 10})
	if err != nil || len(pending) != 0 || len(gl.Entries()) != 0 {
		t.Fatalf("nothing may be stored or posted: %+v %v", pending, err)
	}
}

func TestPostRMACredit_TaxServiceOutageKeepsCreditPendingAndRecordsOnceWhenBack(t *testing.T) {
	s, gl, tax := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := taxedIn(inv.DocumentNumber)
	tax.Err = &taxclient.StatusError{Status: http.StatusServiceUnavailable}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("tax outage: %v", err)
	}
	if items := creditItems(t, s, ctx, inv.CustomerID); len(items) != 0 {
		t.Fatalf("a credit whose tax is not recorded must not reach AR: %+v", items)
	}
	tax.Err = nil
	clearBackoff(t, s, ctx)
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || note.JournalPostingID == "" {
		t.Fatalf("recovered: %+v %v", note, err)
	}
	if len(gl.Entries()) != 1 || len(tax.Recorded()) != 2 {
		t.Fatalf("the ledger entry replays under its key and the tax is recorded once: %d journals, %d tax transactions", len(gl.Entries()), len(tax.Recorded()))
	}
}

func TestPostRMACredit_TaxServiceRefusalParksTheCredit(t *testing.T) {
	s, _, tax := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	tax.Err = &taxclient.StatusError{Status: http.StatusBadRequest}
	if _, err := s.PostRMACredit(ctx, taxedIn(inv.DocumentNumber)); problemCode(err) != CodeRMAParked {
		t.Fatalf("tax refusal: %v", err)
	}
	parked, err := s.ListRMACredits(ctx, RMACreditFilter{ParkedOnly: true, Limit: 10})
	if err != nil || len(parked) != 1 || !parked[0].Tax.Equal(amt("64.00")) {
		t.Fatalf("parked: %+v %v", parked, err)
	}
}

func TestPostRMACredit_InvoiceBoundIncludesTheReversedTax(t *testing.T) {
	s, _, _ := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	over := rmaIn(inv.DocumentNumber)
	over.InvoiceID = billingInvoiceID
	over.Gross, over.Restocking, over.Net = amt("700.00"), amt("0"), amt("700.00")
	over.TaxLines = []RMATaxLine{{Code: "DE19", Amount: amt("300.01")}}
	if _, err := s.PostRMACredit(ctx, over); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("700.00 + 300.01 of a 1000.00 invoice: %v", err)
	}
	over.TaxLines[0].Amount = amt("300.00")
	if _, err := s.PostRMACredit(ctx, over); err != nil {
		t.Fatalf("700.00 + 300.00 is the whole invoice: %v", err)
	}
	next := rmaIn(inv.DocumentNumber)
	next.RMANumber = "RMA-0000002"
	next.Gross, next.Restocking, next.Net = amt("0.01"), amt("0"), amt("0.01")
	if _, err := s.PostRMACredit(ctx, next); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("the earlier credit's tax counts against the invoice: %v", err)
	}
}

func TestValidateRMATax(t *testing.T) {
	long := "ABCDEFGHIJKLMNOPQRSTU"
	for name, lines := range map[string][]RMATaxLine{
		"empty code":    {{Code: "", Amount: amt("1")}},
		"long code":     {{Code: long, Amount: amt("1")}},
		"zero amount":   {{Code: "A", Amount: amt("0")}},
		"negative":      {{Code: "A", Amount: amt("-1")}},
		"duplicate":     {{Code: "A", Amount: amt("1")}, {Code: "A", Amount: amt("2")}},
		"too many code": make([]RMATaxLine, MaxRMATaxLines+1),
	} {
		if err := ValidateRMATax(lines); problemCode(err) != CodeRMACreditInvalid {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := ValidateRMATax([]RMATaxLine{{Code: "A", Amount: amt("1")}, {Code: "B", Amount: amt("2")}}); err != nil {
		t.Fatalf("valid lines: %v", err)
	}
}

func taxOnly(invoice, rma, code, amount string) RMACredit {
	in := rmaIn(invoice)
	in.RMAID, in.RMANumber, in.InvoiceID = ids.New(), rma, billingInvoiceID
	in.Gross, in.Restocking, in.Net = amt("100.00"), amt("0"), amt("100.00")
	in.TaxLines = []RMATaxLine{{Code: code, Amount: amt(amount)}}
	return in
}

func TestPostRMACredit_TaxCodeTheInvoiceNeverBilledIsRejected(t *testing.T) {
	s, gl, tax := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	s.WithRMATax(map[string]map[string]string{legalEntity: {"DE19": "2310", "DE7": "2320", "FR20": "2330"}}, tax)
	if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-0000001", "FR20", "1.00")); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("a code the invoice does not carry: %v", err)
	}
	if len(gl.Entries()) != 0 || len(tax.Recorded()) != 0 {
		t.Fatalf("nothing may be posted or recorded: %d journals, %d tax transactions", len(gl.Entries()), len(tax.Recorded()))
	}
}

func TestPostRMACredit_OverClaimingOneCodeIsRejectedEvenWhenTheTotalFits(t *testing.T) {
	s, gl, _ := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-0000001", "DE7", "20.01")); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("20.01 of 20.00 billed DE7 while 100.00 + 20.01 fits the 1000.00 invoice: %v", err)
	}
	if len(gl.Entries()) != 0 {
		t.Fatalf("a refused credit posts nothing: %d journals", len(gl.Entries()))
	}
}

func TestPostRMACredit_SuccessiveCreditsExhaustACodeExactly(t *testing.T) {
	s, _, tax := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	for i, amount := range []string{"12.00", "8.00"} {
		if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-000000"+string(rune('1'+i)), "DE7", amount)); err != nil {
			t.Fatalf("credit %d of %s: %v", i+1, amount, err)
		}
	}
	if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-0000003", "DE7", "0.01")); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("DE7 is used up after 12.00 + 8.00: %v", err)
	}
	if len(tax.Recorded()) != 2 {
		t.Fatalf("two credits recorded: %d", len(tax.Recorded()))
	}
}

func TestPostRMACredit_TaxBasisIsBillingsNotTheCallers(t *testing.T) {
	s, _, _ := rmaTaxSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := taxOnly(inv.DocumentNumber, "RMA-0000001", "DE7", "1.00")
	in.InvoiceID = "00000000-0000-0000-0000-0000000000b2"
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("an invoice id billing does not know: %v", err)
	}
	other := billingclient.NewFake()
	other.Set(billingInvoiceID, billingclient.Basis{Number: "INV-99999", LegalEntityID: legalEntity, Remaining: map[string]decimal.Decimal{"DE7": amt("20.00")}})
	s.WithBilling(other)
	if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-0000001", "DE7", "1.00")); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("an invoice id of another invoice number: %v", err)
	}
	other.Err = http.ErrHandlerTimeout
	if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-0000001", "DE7", "1.00")); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("billing outage: %v", err)
	}
	s.billing = nil
	if _, err := s.PostRMACredit(ctx, taxOnly(inv.DocumentNumber, "RMA-0000001", "DE7", "1.00")); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("billing not configured: %v", err)
	}
}
