package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const (
	bundleOrder    = "018f0000-0000-7000-8000-00000000000f"
	bundleHardware = "018f0000-0000-7000-8000-0000000000a1"
	bundleInstall  = "018f0000-0000-7000-8000-0000000000a2"
	bundleSupport  = "018f0000-0000-7000-8000-0000000000a3"
)

// creditLine is one line of a credit note as billing books it: revenue is the allocated amount debited to 4000, deferred the
// part debited to 2400 (creditnote.booked lines[].deferred).
type creditLine struct {
	product, revenue, deferred string
}

// creditNote books a credit note the way billing does, Cr 1200 / Dr 4000 / Dr 2400 into the ledger fake on its posting date, and
// returns its creditnote.booked event. Billing is a separate module; its own tests check that it books these amounts and publishes
// the same deferred amounts on the event (TestGLPostings_BookedCreditNotePublishesTheDeferredItReversed).
func (g *ledgerRig) creditNote(t *testing.T, number, date string, lines ...creditLine) events.Event {
	t.Helper()
	id := ids.New()
	gross, revenue, deferred := decimal.Zero, decimal.Zero, decimal.Zero
	type line struct {
		LineNo         int    `json:"line_no"`
		OriginalLineNo int    `json:"original_line_no"`
		OrderID        string `json:"order_id"`
		Product        string `json:"product"`
		Net            string `json:"net"`
		Allocated      string `json:"allocated"`
		Deferred       string `json:"deferred"`
	}
	data := map[string]any{"credit_note_id": id, "number": number, "original_invoice_id": ids.New(), "original_number": "INV-2027-0000001",
		"legal_entity_id": entityID, "currency": "USD", "posting_date": date}
	var out []line
	for i, l := range lines {
		r, d := decimal.RequireFromString(l.revenue), decimal.RequireFromString(l.deferred)
		gross, revenue, deferred = gross.Add(r).Add(d), revenue.Add(r), deferred.Add(d)
		out = append(out, line{LineNo: i + 1, OriginalLineNo: i + 1, OrderID: bundleOrder, Product: l.product, Net: r.Add(d).StringFixed(2),
			Allocated: r.Add(d).StringFixed(2), Deferred: d.StringFixed(2)})
	}
	entryLines := []ledgerclient.Line{{Account: "1200", Credit: true, Amount: gross}}
	if revenue.IsPositive() {
		entryLines = append(entryLines, ledgerclient.Line{Account: "4000", Amount: revenue})
	}
	if deferred.IsPositive() {
		entryLines = append(entryLines, ledgerclient.Line{Account: store.DeferredAccount, Amount: deferred})
	}
	entryID, err := g.ledger.Post(context.Background(), ledgerclient.Entry{LegalEntityID: entityID, LedgerID: g.ledgerID, DocumentID: id,
		PostingDate: date, Currency: "USD", IdempotencyKey: "billing-credit-note-" + id, SourceType: "billing_credit_note", Lines: entryLines})
	if err != nil {
		t.Fatal(err)
	}
	data["journal_entry_id"], data["lines"] = entryID, out
	evt, err := events.New(CreditNoteBookedType, BillingSource, id, testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

// consume delivers an event to the credit-note consumer's handler as the consumer runs it: under its service principal, for the
// event's tenant, with no legal entity.
func (g *ledgerRig) consume(t *testing.T, evt events.Event) error {
	t.Helper()
	actor, err := tenant.ServicePrincipal(CreditNoteConsumerName, evt.TenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	return CreditNoteConsumerHandlers(g.st)[evt.Type].Handle(tenant.With(context.Background(), actor), nil, evt)
}

func (g *ledgerRig) rollForward(t *testing.T, period string) rollForwardOut {
	t.Helper()
	rec := call(t, g.handler, http.MethodGet, "/v1/revrec/roll-forward?legalEntityId="+entityID+"&currency=USD&period="+period, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("roll-forward %s: %d %s", period, rec.Code, rec.Body)
	}
	var out rollForwardOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type postAllOut struct {
	Items           []postingOut  `json:"items"`
	Reversals       []reversalOut `json:"reversals"`
	FailedReversals []reversalOut `json:"failedReversals"`
}

func (g *ledgerRig) postAll(t *testing.T, body string) postAllOut {
	t.Helper()
	rec := call(t, g.handler, http.MethodPost, "/v1/revrec/journal-postings", body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("journal-postings %d %s", rec.Code, rec.Body)
	}
	var out postAllOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (g *ledgerRig) contract(t *testing.T, id string) contractOut {
	t.Helper()
	return decodeContract(t, call(t, g.handler, http.MethodGet, "/v1/revrec/contracts/"+id, "", nil))
}

func scheduledTotal(o obligationOut) decimal.Decimal {
	total := decimal.Zero
	for _, s := range o.Schedule {
		if s.Status == "SCHEDULED" {
			total = total.Add(decimal.RequireFromString(s.Amount))
		}
	}
	return total
}

func requireTied(t *testing.T, what string, rec glReconciliationOut) {
	t.Helper()
	if !rec.Reconciled || rec.Difference != "0.00" || rec.ClosingDiff != "0.00" || rec.GLClosing != rec.Deferred {
		t.Fatalf("%s: %+v, want the roll-forward tied to GL 2400 (difference and closing difference 0.00)", what, rec)
	}
}

// creditNotesKeepTheRollForwardTied continues TestGLReconciliation_ClosingBalanceTiesInvoiceDeferralsToRecognition (#29 P3):
// after the invoices booked the allocated deferred revenue to 2400 and recognition debited it for March and April, a partial
// credit note (half the support) and a full credit note of the rest (the satisfied installation and the support left) debit
// 2400 for their deferred part as billing books them. The consumer reduces the unrecognized schedule first and reverses
// recognized revenue for the excess, the posting sweep books that reversal (Dr 4000 / Cr 2400), and after every step the
// roll-forward closing equals GL 2400 to the cent; at the end both are 0.00 and nothing is left to recognize.
func (g *ledgerRig) creditNotesKeepTheRollForwardTied(t *testing.T, created contractOut) {
	t.Helper()
	// Partial credit note: half the support (1,125.00 of its 2,250.00), posted in May while 1,125.00+ is still unrecognized.
	support := created.Obligations[2]
	before := scheduledTotal(g.contract(t, created.ID).Obligations[2])
	*g.clock = time.Date(2027, 5, 11, 9, 0, 0, 0, time.UTC)
	partial := g.creditNote(t, "CN-2027-0000001", "2027-05-10", creditLine{bundleSupport, "0.00", "1125.00"})
	if err := g.consume(t, partial); err != nil {
		t.Fatalf("partial credit note: %v", err)
	}
	if err := g.consume(t, partial); err != nil {
		t.Fatalf("partial credit note redelivered: %v", err)
	}
	after := g.contract(t, created.ID).Obligations[2]
	if got := before.Sub(scheduledTotal(after)); got.StringFixed(2) != "1125.00" || after.ID != support.ID {
		t.Fatalf("partial credit took %s off the support's unrecognized schedule (redelivered once), want exactly 1125.00", got)
	}
	reduced := 0
	for _, s := range after.Schedule {
		if s.Status == "REVERSED" {
			t.Errorf("partial credit reversed line %d; the remaining lines should each be reduced pro rata", s.Seq)
		}
		if s.Status == "SCHEDULED" {
			reduced++
		}
	}
	if reduced == 0 {
		t.Fatal("no scheduled support line left after the partial credit")
	}
	// The installation is satisfied in May: its 1,350.00 is recognized and the full credit note below must reverse it.
	if rec := call(t, g.handler, http.MethodPost, "/v1/revrec/milestones/"+created.Obligations[1].ID+":satisfy", `{"satisfiedOn":"2027-05-20"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("satisfy: %d %s", rec.Code, rec.Body)
	}
	*g.clock = time.Date(2027, 6, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-05-31")
	if out := g.postAll(t, `{"legalEntityId":"`+entityID+`"}`); len(out.Reversals) != 0 {
		t.Fatalf("partial credit posted reversals %+v, want none (it only reduced unrecognized revenue)", out.Reversals)
	}
	may := g.reconcile(t, "2027-05")
	requireTied(t, "May after the partial credit note", may)
	if may.CreditNotes != "1125.00" || may.Reversed != "0.00" {
		t.Errorf("May reconciliation %+v, want 1125.00 of credit-note debits and nothing reversed", may)
	}
	if rf := g.rollForward(t, "2027-05"); rf.Adjustments != "-1125.00" || rf.Reversed != "0.00" {
		t.Errorf("May roll-forward %+v, want adjustments -1125.00 and reversed 0.00", rf)
	}

	// Full credit note of the rest in June: hardware (no deferral), the installation's 1,350.00 (all recognized) and the
	// support's other 1,125.00, more than the support still has unrecognized.
	unrecognized := scheduledTotal(g.contract(t, created.ID).Obligations[2])
	*g.clock = time.Date(2027, 6, 16, 9, 0, 0, 0, time.UTC)
	full := g.creditNote(t, "CN-2027-0000002", "2027-06-15",
		creditLine{bundleHardware, "7200.00", "0.00"}, creditLine{bundleInstall, "0.00", "1350.00"}, creditLine{bundleSupport, "0.00", "1125.00"})
	if err := g.consume(t, full); err != nil {
		t.Fatalf("full credit note: %v", err)
	}
	replayed := full
	replayed.ID = ids.New()
	if err := g.consume(t, replayed); err != nil {
		t.Fatalf("full credit note replayed under another event id: %v", err)
	}
	wantReversed := decimal.RequireFromString("1350.00").Add(decimal.RequireFromString("1125.00").Sub(unrecognized))
	if !unrecognized.IsPositive() || !wantReversed.GreaterThan(decimal.RequireFromString("1350.00")) {
		t.Fatalf("support has %s unrecognized before the full credit, want some left and less than its 1125.00 credit", unrecognized)
	}
	final := g.contract(t, created.ID)
	if final.Status != "COMPLETE" || !scheduledTotal(final.Obligations[2]).IsZero() {
		t.Errorf("after the full credit the contract is %s with %s scheduled, want COMPLETE and nothing left to recognize", final.Status, scheduledTotal(final.Obligations[2]))
	}

	// Before the reversal is posted the roll-forward already shows it and the ledger does not: the closing differs by it.
	if rec := g.reconcile(t, "2027-06"); rec.Reconciled || rec.Difference != "0.00" || rec.ClosingDiff != fixed(wantReversed, "USD") {
		t.Fatalf("June before posting the reversal %+v, want debits tied and a closing difference of %s", rec, wantReversed.StringFixed(2))
	}
	*g.clock = time.Date(2027, 7, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-06-30")
	out := g.postAll(t, `{"legalEntityId":"`+entityID+`"}`)
	posted := decimal.Zero
	for _, r := range out.Reversals {
		posted = posted.Add(decimal.RequireFromString(r.Amount))
		if r.PostingDate != "2027-06-15" || r.CreditNoteNumber != "CN-2027-0000002" || r.JournalEntryID == "" {
			t.Errorf("reversal %+v, want CN-2027-0000002's posting dated 2027-06-15", r)
		}
	}
	if !posted.Equal(wantReversed) || len(out.FailedReversals) != 0 {
		t.Fatalf("reversals posted %s (%+v), want %s: the installation's 1350.00 and the support's credit above its unrecognized %s",
			posted, out, wantReversed.StringFixed(2), unrecognized.StringFixed(2))
	}
	reversalEntries := decimal.Zero
	for _, e := range g.ledger.Entries() {
		if e.SourceType != reversalSourceType {
			continue
		}
		if e.LedgerID != g.ledgerID || len(e.Lines) != 2 || e.Lines[0].Account != "4000" || e.Lines[0].Credit || e.Lines[1].Account != "2400" || !e.Lines[1].Credit {
			t.Errorf("reversal entry %+v, want Dr 4000 / Cr 2400 on the configured ledger", e)
		}
		reversalEntries = reversalEntries.Add(e.Lines[1].Amount)
	}
	if !reversalEntries.Equal(wantReversed) {
		t.Errorf("ledger holds %s of reversals, want %s", reversalEntries, wantReversed)
	}
	june := g.reconcile(t, "2027-06")
	requireTied(t, "June after the full credit note", june)
	if june.Deferred != "0.00" || june.GLClosing != "0.00" || june.CreditNotes != "3600.00" || june.Reversed != fixed(wantReversed, "USD") {
		t.Errorf("June %+v, want 2400 closed at 0.00 on both sides, 3600.00 of credit-note debits and %s reversed", june, wantReversed.StringFixed(2))
	}
	if rf := g.rollForward(t, "2027-06"); rf.Adjustments != "-2475.00" || rf.Reversed != fixed(wantReversed, "USD") || rf.Closing != "0.00" {
		t.Errorf("June roll-forward %+v, want adjustments -2475.00, reversed %s, closing 0.00", rf, wantReversed.StringFixed(2))
	}
	if again := g.postAll(t, `{"legalEntityId":"`+entityID+`"}`); len(again.Reversals) != 0 || len(again.Items) != 0 {
		t.Errorf("second posting call %+v, want nothing new", again)
	}

	// Nothing is left: later months recognize nothing and stay tied at 0.00.
	*g.clock = time.Date(2027, 8, 2, 9, 0, 0, 0, time.UTC)
	g.run(t, "2027-07-31")
	g.postAll(t, `{"legalEntityId":"`+entityID+`"}`)
	if july := g.reconcile(t, "2027-07"); !july.Reconciled || july.Deferred != "0.00" || july.GLClosing != "0.00" {
		t.Errorf("July %+v, want 0.00 tied", july)
	}
}

// TestCreditNoteConsumer_ParksWhatItCannotApplyAndRetriesTheRest: a rule failure (no contract for the order, a credit above
// what the obligations hold, an event from another source) is permanent and parks the event; a line already applied is not
// applied again.
func TestCreditNoteConsumer_ParksWhatItCannotApplyAndRetriesTheRest(t *testing.T) {
	g := newLedgerRig(t)
	g.bill(t, "3600.00", "2027-03-10")
	call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey})

	unknown := g.creditNote(t, "CN-2027-0000001", "2027-03-20", creditLine{bundleSupport, "0.00", "100.00"})
	var d map[string]any
	if err := json.Unmarshal(unknown.Data, &d); err != nil {
		t.Fatal(err)
	}
	d["lines"].([]any)[0].(map[string]any)["order_id"] = ids.New()
	unknown.Data, _ = json.Marshal(d)
	if err := g.consume(t, unknown); !eventing.IsPermanent(err) {
		t.Errorf("credit note for an order without a contract: %v, want a permanent failure", err)
	}

	tooMuch := g.creditNote(t, "CN-2027-0000002", "2027-03-20", creditLine{bundleSupport, "0.00", "2250.01"})
	if err := g.consume(t, tooMuch); !eventing.IsPermanent(err) {
		t.Errorf("credit above the support's 2,250.00: %v, want a permanent failure", err)
	}

	forged := g.creditNote(t, "CN-2027-0000003", "2027-03-20", creditLine{bundleSupport, "0.00", "10.00"})
	forged.Source = "sales"
	if err := g.consume(t, forged); !eventing.IsPermanent(err) {
		t.Errorf("creditnote.booked from another source: %v, want a permanent failure", err)
	}

	// One good line and one without a contract: the good line is applied once, the event still parks for the other.
	mixed := g.creditNote(t, "CN-2027-0000004", "2027-03-20", creditLine{bundleSupport, "0.00", "250.00"}, creditLine{bundleInstall, "0.00", "50.00"})
	if err := json.Unmarshal(mixed.Data, &d); err != nil {
		t.Fatal(err)
	}
	d["lines"].([]any)[1].(map[string]any)["order_id"] = ids.New()
	mixed.Data, _ = json.Marshal(d)
	for range 2 {
		if err := g.consume(t, mixed); !eventing.IsPermanent(err) {
			t.Errorf("mixed credit note: %v, want a permanent failure for the line without a contract", err)
		}
	}
	if rf := g.rollForward(t, "2027-03"); rf.Adjustments != "-250.00" {
		t.Errorf("roll-forward adjustments %s, want only the good line's -250.00, once", rf.Adjustments)
	}
}

type failingCreditStore struct{ err error }

func (f failingCreditStore) ApplyCreditLine(context.Context, store.CreditNote, store.CreditLine) (store.Reversal, bool, error) {
	return store.Reversal{}, false, f.err
}

// TestCreditNoteConsumer_TransientFailureIsRetried: a store failure that is not a rule violation (the database, a concurrent
// apply of the same line) is retried by the consumer, not parked; a rule problem is parked.
func TestCreditNoteConsumer_TransientFailureIsRetried(t *testing.T) {
	g := &ledgerRig{ledgerID: ids.New(), ledger: ledgerclient.NewFake()}
	evt := g.creditNote(t, "CN-2027-0000001", "2027-03-20", creditLine{bundleSupport, "0.00", "10.00"})
	actor, err := tenant.ServicePrincipal(CreditNoteConsumerName, evt.TenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), actor)
	for _, tc := range []struct {
		err       error
		permanent bool
	}{
		{pgx.ErrTxClosed, false},
		{httpx.NewProblem(http.StatusConflict, store.CodeKeyInFlight, "in flight"), false},
		{httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCreditExceeds, "too much"), true},
	} {
		err := CreditNoteConsumerHandlers(failingCreditStore{tc.err})[CreditNoteBookedType].Handle(ctx, nil, evt)
		if err == nil || eventing.IsPermanent(err) != tc.permanent {
			t.Errorf("store error %v: handler returned %v, want permanent=%v", tc.err, err, tc.permanent)
		}
	}
	unscoped := tenant.With(context.Background(), tenant.Principal{TenantID: evt.TenantID})
	if err := CreditNoteConsumerHandlers(failingCreditStore{})[CreditNoteBookedType].Handle(unscoped, nil, evt); !eventing.IsPermanent(err) || errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("principal without %s: %v, want a permanent failure", dutyCreditNoteReverse, err)
	}
}

// TestReversalPostings_RefusalBacksOffAndParks: a reversal the ledger refuses is backed off and parked after
// store.MaxReversalRefusals refusals, never blocks the rows behind it, and is offered again with retryRejected.
func TestReversalPostings_RefusalBacksOffAndParks(t *testing.T) {
	g := newLedgerRig(t)
	g.bill(t, "3600.00", "2027-03-10")
	created := decodeContract(t, call(t, g.handler, http.MethodPost, "/v1/revrec/contracts", bundleContract, map[string]string{idempotencyKey: idemKey}))
	if rec := call(t, g.handler, http.MethodPost, "/v1/revrec/milestones/"+created.Obligations[1].ID+":satisfy", `{"satisfiedOn":"2027-03-25"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("satisfy: %d %s", rec.Code, rec.Body)
	}
	g.post(t, http.StatusOK)
	if err := g.consume(t, g.creditNote(t, "CN-2027-0000001", "2027-03-28", creditLine{bundleInstall, "0.00", "1350.00"})); err != nil {
		t.Fatal(err)
	}
	g.ledger.RefuseClosedThrough("2027-03-31")
	for i := 1; i <= store.MaxReversalRefusals; i++ {
		out := g.postAll(t, `{"legalEntityId":"`+entityID+`"}`)
		if len(out.FailedReversals) != 1 || out.FailedReversals[0].Parked != (i == store.MaxReversalRefusals) || out.FailedReversals[0].LedgerCode != ledgerclient.CodePeriodClosed {
			t.Fatalf("attempt %d: %+v, want one refusal, parked on the last", i, out)
		}
		*g.clock = g.clock.Add(25 * time.Hour)
	}
	if out := g.postAll(t, `{"legalEntityId":"`+entityID+`"}`); len(out.FailedReversals) != 0 || len(out.Reversals) != 0 {
		t.Fatalf("parked reversal offered again without retryRejected: %+v", out)
	}
	if rec := g.reconcile(t, "2027-03"); rec.Reconciled || rec.ClosingDiff != "1350.00" {
		t.Errorf("parked reversal %+v, want the unposted 1350.00 shown as a closing difference", rec)
	}
	g.ledger.RefuseClosedThrough("")
	out := g.postAll(t, `{"legalEntityId":"`+entityID+`","retryRejected":true}`)
	if len(out.Reversals) != 1 || out.Reversals[0].Amount != "1350.00" {
		t.Fatalf("retried %+v, want the 1350.00 reversal posted", out)
	}
	requireTied(t, "after the retried reversal", g.reconcile(t, "2027-03"))
}
