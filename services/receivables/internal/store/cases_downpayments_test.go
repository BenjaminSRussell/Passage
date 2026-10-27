package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

func customer(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", 9000+n) }

// lastEvent reads the newest outbox payload data of eventType.
func lastEvent(t *testing.T, s *Postgres, ctx context.Context, eventType string, into any) {
	t.Helper()
	var raw []byte
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select payload->'data' from outbox where type = $1 order by created_at desc, id desc limit 1`, eventType).Scan(&raw)
	}); err != nil {
		t.Fatalf("read %s: %v", eventType, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode %s: %v", eventType, err)
	}
}

// AC-30.14: 20 customers selected as of 2027-03-31; 15 agree, 2 disagree and 3 do
// not reply; the difference list shows 2 rows with both balances and 2 DISPUTE
// cases exist.
func TestBalanceConfirmation_AC3014DifferencesAndDisputeCases(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	for i := 0; i < 20; i++ {
		seedItem(t, s, ctx, fmt.Sprintf("INV-BC-%02d", i), customer(i), "2027-04-30", fmt.Sprintf("%d.00", 10000+i*100), "USD")
	}
	seedItem(t, s, ctx, "INV-BC-SMALL", customer(50), "2027-04-30", "9999.99", "USD")
	in := ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2027-03-31", Threshold: amt("10000"), ReminderDays: 14}
	run, err := s.CreateConfirmationRun(ctx, in, "bc-run-1")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.Counts.Selected != 20 || len(run.Confirmations) != 20 {
		t.Fatalf("selected %d, want the 20 customers at or above 10,000.00", run.Counts.Selected)
	}
	replay, err := s.CreateConfirmationRun(ctx, in, "bc-run-1")
	if err != nil || replay.ID != run.ID {
		t.Fatalf("replay = %s (%v), want %s", replay.ID, err, run.ID)
	}
	changed := in
	changed.Threshold = amt("5000")
	if _, err := s.CreateConfirmationRun(ctx, changed, "bc-run-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("other body under the same key: %v", err)
	}

	for i, c := range run.Confirmations {
		var reply ConfirmationReply
		switch {
		case i < 15:
			reply = ConfirmationReply{ConfirmationID: c.ID, Reply: domain.ReplyAgreed}
		case i < 17:
			theirs := c.OurBalance.Sub(amt("250.00"))
			reply = ConfirmationReply{ConfirmationID: c.ID, Reply: domain.ReplyDisagreed, CustomerBalance: &theirs, Comment: ptr("credit note CN-7 missing")}
		default:
			continue
		}
		if _, err := s.RecordConfirmationReply(ctx, run.ID, reply); err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
	}

	diffs, err := s.ConfirmationDifferences(ctx, run.ID)
	if err != nil {
		t.Fatalf("differences: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("difference list has %d rows, want 2", len(diffs))
	}
	for _, d := range diffs {
		if d.CustomerBalance == nil || d.Difference == nil || !d.Difference.Equal(amt("-250")) || d.CaseID == nil {
			t.Fatalf("difference row %+v, want both balances, -250.00 and a case", d)
		}
		c, err := s.GetCase(ctx, *d.CaseID)
		if err != nil {
			t.Fatalf("case: %v", err)
		}
		if c.Category != domain.CaseCategoryDispute || c.Reason != domain.BalanceDifferenceReason || c.AttachedAmount == nil ||
			!c.AttachedAmount.Equal(*d.CustomerBalance) || c.CustomerID != d.CustomerID || c.SLAHours != 120 {
			t.Fatalf("case %+v, want a 120 h DISPUTE case carrying the customer's balance (BR-30.13)", c)
		}
	}
	cases, err := s.ListCases(ctx, CaseFilter{Category: domain.CaseCategoryDispute}, 50)
	if err != nil || len(cases) != 2 {
		t.Fatalf("DISPUTE cases = %d (%v), want 2", len(cases), err)
	}
	got, err := s.GetConfirmationRun(ctx, run.ID)
	if err != nil || got.Counts != (ConfirmationCounts{Selected: 20, Agreed: 15, Disagreed: 2, NoReply: 3}) {
		t.Fatalf("counts = %+v (%v)", got.Counts, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.ConfirmationReplied); n != 17 {
		t.Fatalf("replied events = %d, want 17", n)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CaseOpened); n != 2 {
		t.Fatalf("case.opened events = %d, want 2", n)
	}

	// Replies are immutable: the same reply again returns it, another is 409.
	first := run.Confirmations[0]
	if again, err := s.RecordConfirmationReply(ctx, run.ID, ConfirmationReply{ConfirmationID: first.ID, Reply: domain.ReplyAgreed}); err != nil || again.Status != domain.ReplyAgreed {
		t.Fatalf("repeat of the recorded reply: %+v %v", again, err)
	}
	other := amt("1.00")
	if _, err := s.RecordConfirmationReply(ctx, run.ID, ConfirmationReply{ConfirmationID: first.ID, Reply: domain.ReplyDisagreed, CustomerBalance: &other}); problemCode(err) != domain.CodeConfirmationReplied {
		t.Fatalf("changing a recorded reply: %v", err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.ConfirmationReplied); n != 17 {
		t.Fatalf("a repeated reply published again: %d events", n)
	}
	// AGREED with another balance is a contradiction.
	open := run.Confirmations[19]
	if _, err := s.RecordConfirmationReply(ctx, run.ID, ConfirmationReply{ConfirmationID: open.ID, Reply: domain.ReplyAgreed, CustomerBalance: &other}); problemCode(err) != domain.CodeConfirmationReply {
		t.Fatalf("AGREED with a different balance: %v", err)
	}
	// A confirmation is addressed through its own run.
	other2, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2027-03-31", Threshold: amt("10000"), SampleSize: ptr(3), ReminderDays: 14}, "bc-run-2")
	if err != nil || other2.Counts.Selected != 3 {
		t.Fatalf("sampled run: %+v %v", other2.Counts, err)
	}
	if _, err := s.RecordConfirmationReply(ctx, other2.ID, ConfirmationReply{ConfirmationID: open.ID, Reply: domain.ReplyAgreed}); problemCode(err) != CodeConfirmationNotFound {
		t.Fatalf("confirmation of another run: %v", err)
	}
	runs, err := s.ListConfirmationRuns(ctx, 10)
	if err != nil || len(runs) != 2 || runs[0].ID != other2.ID || len(runs[0].Confirmations) != 0 {
		t.Fatalf("runs = %+v (%v), want the sampled run first, without confirmations", runs, err)
	}
	// Another tenant sees nothing.
	if _, err := s.GetConfirmationRun(tenantCtx(2), run.ID); problemCode(err) != CodeConfirmationNotFound {
		t.Fatalf("other tenant read the run: %v", err)
	}
}

// The as-of balance adds back settlements made after the key date, so a run in
// the past sees the balance of that day; zero balances need include_zero_balances.
func TestBalanceConfirmation_AsOfBalanceAndZeroBalances(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedItem(t, s, ctx, "INV-ASOF", custA, "2027-03-31", "12000.00", "USD")
	pay := newPayment(t, s, ctx, &item.CustomerID, "12000.00", "USD", "asof-pay")
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("12000.00")}}}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	execTenant(t, s, ctx, `update settlement set settled_at = '2027-04-05T10:00:00Z' where open_item_id = $1`, item.ID)

	march, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2027-03-31", Threshold: amt("10000"), ReminderDays: 14}, "asof-1")
	if err != nil || len(march.Confirmations) != 1 || !march.Confirmations[0].OurBalance.Equal(amt("12000")) {
		t.Fatalf("March run = %+v (%v), want the 12,000.00 open on 31 March", march.Confirmations, err)
	}
	april, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2027-04-30", Threshold: amt("10000"), ReminderDays: 14}, "asof-2")
	if err != nil || len(april.Confirmations) != 0 {
		t.Fatalf("April run = %+v (%v), want no zero balance without include_zero_balances", april.Confirmations, err)
	}
	withZero, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2027-04-30", Threshold: amt("10000"), IncludeZero: true, ReminderDays: 14}, "asof-3")
	if err != nil || len(withZero.Confirmations) != 1 || !withZero.Confirmations[0].OurBalance.IsZero() {
		t.Fatalf("April run with zero balances = %+v (%v)", withZero.Confirmations, err)
	}
	// Items documented after the key date are not in the balance.
	seedItemOn(t, s, ctx, "INV-LATER", custB, "2027-04-01", "50000.00")
	if run, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2027-03-31", Threshold: amt("10000"), ReminderDays: 14}, "asof-4"); err != nil || len(run.Confirmations) != 1 {
		t.Fatalf("run before the later invoice = %+v (%v)", run.Confirmations, err)
	}
}

func seedItemOn(t *testing.T, s *Postgres, ctx context.Context, doc, cust, docDate, amount string) OpenItem {
	t.Helper()
	item, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: cust, SourceType: "INVOICE", DocumentNumber: doc,
		DocumentDate: docDate, DueDate: docDate, Currency: "USD", Amount: amt(amount), OpenAmount: amt(amount)})
	if err != nil {
		t.Fatalf("seed %s: %v", doc, err)
	}
	return item
}

func TestCase_OpenAssignCloseAndGuards(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedItem(t, s, ctx, "INV-CASE", custA, "2027-02-28", "800.00", "EUR")
	in := Case{LegalEntityID: legalEntity, CustomerID: custB, Category: domain.CaseCategoryCollection, Owner: "ana", Priority: "HIGH",
		Reason: "Overdue 60 days", OpenItemID: &item.ID}
	if _, err := s.CreateCase(ctx, in, "case-x"); problemCode(err) != domain.CodeCaseMismatch {
		t.Fatalf("item of another customer: %v", err)
	}
	in.CustomerID = custA
	c, err := s.CreateCase(ctx, in, "case-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if c.Currency != "EUR" || c.DocumentNumber == nil || *c.DocumentNumber != "INV-CASE" || c.SLAHours != 72 || c.Status != domain.CaseOpen || c.SLABreached {
		t.Fatalf("case = %+v, want EUR from the item, its document and a 72 h SLA", c)
	}
	if replay, err := s.CreateCase(ctx, in, "case-1"); err != nil || replay.ID != c.ID {
		t.Fatalf("replay: %v", err)
	}
	changed := in
	changed.Reason = "something else"
	if _, err := s.CreateCase(ctx, changed, "case-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("other body under the same key: %v", err)
	}
	if _, err := s.AssignCase(ctx, c.ID, c.Version+5, "ben"); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale assign: %v", err)
	}
	assigned, err := s.AssignCase(ctx, c.ID, c.Version, "ben")
	if err != nil || assigned.Owner != "ben" || assigned.Version != c.Version+1 {
		t.Fatalf("assign = %+v (%v)", assigned, err)
	}
	closed, err := s.CloseCase(ctx, c.ID, assigned.Version, ptr("customer paid"))
	if err != nil || closed.Status != domain.CaseClosed || closed.ClosedAt == nil || closed.CloseNote == nil {
		t.Fatalf("close = %+v (%v)", closed, err)
	}
	if _, err := s.CloseCase(ctx, c.ID, closed.Version, nil); problemCode(err) != domain.CodeCaseState {
		t.Fatalf("closing twice: %v", err)
	}
	var ev rcvevents.CaseClosedData
	lastEvent(t, s, ctx, rcvevents.CaseClosed, &ev)
	if ev.CaseID != c.ID || ev.CustomerID != custA || ev.SLABreached {
		t.Fatalf("case.closed = %+v", ev)
	}
	open, err := s.ListCases(ctx, CaseFilter{Status: domain.CaseOpen}, 10)
	if err != nil || len(open) != 0 {
		t.Fatalf("open cases = %+v (%v), want none", open, err)
	}
	if _, err := s.GetCase(tenantCtx(2), c.ID); problemCode(err) != CodeCaseNotFound {
		t.Fatalf("other tenant read the case: %v", err)
	}
	// An overdue SLA shows as breached on read (AR-0018).
	late, err := s.CreateCase(ctx, Case{LegalEntityID: legalEntity, CustomerID: custB, Category: domain.CaseCategoryDeduction, Owner: "ana",
		Priority: "NORMAL", Reason: "short pay", Currency: "USD"}, "case-2")
	if err != nil {
		t.Fatalf("second case: %v", err)
	}
	execTenant(t, s, ctx, `update collection_case set sla_due = now() - interval '1 hour' where id = $1`, late.ID)
	if got, err := s.GetCase(ctx, late.ID); err != nil || !got.SLABreached {
		t.Fatalf("overdue case = %+v (%v), want slaBreached", got, err)
	}
}

// AC-30.15: a 2,000.00 USD down payment on order SO-4000 against a final invoice
// of 5,000.00 clears Dr 2190 2,000.00 / Cr 1200 2,000.00 and leaves 3,000.00 open.
func TestDownPayment_AC3015ClearingAgainstTheFinalInvoice(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	order := "00000000-0000-0000-0000-00000000a400"
	in := DownPayment{LegalEntityID: legalEntity, CustomerID: custA, SalesOrderID: &order, Kind: domain.KindDownPayment, Currency: "USD", Amount: amt("2000.00")}
	dp, err := s.CreateDownPayment(ctx, in, "dp-1")
	if err != nil || dp.Status != domain.DownRequested || dp.ReceivedDate != nil {
		t.Fatalf("request = %+v (%v)", dp, err)
	}
	if replay, err := s.CreateDownPayment(ctx, in, "dp-1"); err != nil || replay.ID != dp.ID {
		t.Fatalf("replay: %v", err)
	}
	invoice := seedItem(t, s, ctx, "INV-SO-4000", custA, "2027-04-30", "5000.00", "USD")
	if _, err := s.ClearDownPayment(ctx, dp.ID, dp.Version, invoice.ID, nil); problemCode(err) != domain.CodeDownPaymentState {
		t.Fatalf("clearing an unpaid request: %v", err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.DownPaymentReceived); n != 0 {
		t.Fatalf("a request posted: %d events", n)
	}
	got, err := s.ReceiveDownPayment(ctx, dp.ID, dp.Version, "2027-03-02")
	if err != nil || got.Status != domain.DownReceived || got.ReceivedDate == nil || *got.ReceivedDate != "2027-03-02" {
		t.Fatalf("receive = %+v (%v)", got, err)
	}
	var received rcvevents.DownPaymentData
	lastEvent(t, s, ctx, rcvevents.DownPaymentReceived, &received)
	if received.Postings[0].Account != domain.AccountBank || received.Postings[1].Account != domain.AccountDownPayment ||
		!received.Postings[1].Credit.Equal(amt("2000")) || received.SalesOrderID == nil || *received.SalesOrderID != order {
		t.Fatalf("received event = %+v, want Dr bank / Cr 2190 2,000.00 on SO-4000", received)
	}
	if _, err := s.ClearDownPayment(ctx, dp.ID, dp.Version, invoice.ID, nil); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale clearing: %v", err)
	}
	cleared, err := s.ClearDownPayment(ctx, got.ID, got.Version, invoice.ID, nil)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Status != domain.DownCleared || !cleared.Remaining.IsZero() || len(cleared.Clearings) != 1 ||
		cleared.Clearings[0].DocumentNumber != "INV-SO-4000" || !cleared.Clearings[0].Amount.Equal(amt("2000")) {
		t.Fatalf("down payment after clearing = %+v", cleared)
	}
	items, err := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: custA, OpenOnly: true}, 10)
	if err != nil || len(items) != 1 || !items[0].OpenAmount.Equal(amt("3000")) || items[0].Status != "PARTIALLY_SETTLED" {
		t.Fatalf("invoice after clearing = %+v (%v), want 3,000.00 open", items, err)
	}
	var ev rcvevents.DownPaymentData
	lastEvent(t, s, ctx, rcvevents.DownPaymentCleared, &ev)
	want := []domain.Posting{{Account: "2190", Debit: amt("2000"), Credit: decimal.Zero}, {Account: "1200", Debit: decimal.Zero, Credit: amt("2000")}}
	if len(ev.Postings) != 2 || ev.OpenItemID == nil || *ev.OpenItemID != invoice.ID || !ev.Amount.Equal(amt("2000")) {
		t.Fatalf("cleared event = %+v", ev)
	}
	for i, p := range want {
		if ev.Postings[i].Account != p.Account || !ev.Postings[i].Debit.Equal(p.Debit) || !ev.Postings[i].Credit.Equal(p.Credit) {
			t.Fatalf("posting %d = %+v, want %+v", i, ev.Postings[i], p)
		}
	}
	if _, err := s.ClearDownPayment(ctx, cleared.ID, cleared.Version, invoice.ID, nil); problemCode(err) != domain.CodeDownPaymentState {
		t.Fatalf("clearing a cleared down payment: %v", err)
	}
	// The as-of balance sees the clearing: 3,000.00 remains for the customer.
	run, err := s.CreateConfirmationRun(ctx, ConfirmationRun{LegalEntityID: legalEntity, AsOfDate: "2099-12-31", Threshold: amt("0"), ReminderDays: 14}, "dp-bc")
	if err != nil || len(run.Confirmations) != 1 || !run.Confirmations[0].OurBalance.Equal(amt("3000")) {
		t.Fatalf("balance after clearing = %+v (%v), want 3,000.00", run.Confirmations, err)
	}
}

func TestSecurityDeposit_OffsetReleaseAndGuards(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	dep, err := s.CreateDownPayment(ctx, DownPayment{LegalEntityID: legalEntity, CustomerID: custA, Kind: domain.KindSecurityDeposit, Currency: "USD", Amount: amt("1000.00")}, "dep-1")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, err := s.ReleaseDownPayment(ctx, dep.ID, dep.Version); problemCode(err) != domain.CodeDownPaymentState {
		t.Fatalf("releasing an unpaid deposit: %v", err)
	}
	dep, err = s.ReceiveDownPayment(ctx, dep.ID, dep.Version, "")
	if err != nil || dep.ReceivedDate == nil {
		t.Fatalf("receive without a date: %+v %v", dep, err)
	}
	fee, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: custA, SourceType: "FEE", DocumentNumber: "FEE-1",
		DocumentDate: "2027-03-01", DueDate: "2027-03-15", Currency: "USD", Amount: amt("300.00"), OpenAmount: amt("300.00")})
	if err != nil {
		t.Fatalf("fee: %v", err)
	}
	other := seedItem(t, s, ctx, "INV-OTHER", custB, "2027-03-31", "500.00", "USD")
	if _, err := s.ClearDownPayment(ctx, dep.ID, dep.Version, other.ID, nil); problemCode(err) != domain.CodeDownPaymentMismatch {
		t.Fatalf("offset against another customer's item: %v", err)
	}
	if _, err := s.ClearDownPayment(ctx, dep.ID, dep.Version, fee.ID, ptr(amt("300.01"))); problemCode(err) != domain.CodeClearingExceeded {
		t.Fatalf("offset above the fee: %v", err)
	}
	dep, err = s.ClearDownPayment(ctx, dep.ID, dep.Version, fee.ID, nil)
	if err != nil || dep.Status != domain.DownReceived || !dep.Remaining.Equal(amt("700")) {
		t.Fatalf("offset = %+v (%v), want 700.00 remaining", dep, err)
	}
	var offset rcvevents.DownPaymentData
	lastEvent(t, s, ctx, rcvevents.DownPaymentCleared, &offset)
	if offset.Postings[0].Account != domain.AccountSecurityDeposit || offset.Postings[1].Account != domain.AccountReceivable {
		t.Fatalf("offset postings = %+v, want Dr 2195 / Cr 1200", offset.Postings)
	}
	released, err := s.ReleaseDownPayment(ctx, dep.ID, dep.Version)
	if err != nil || released.Status != domain.DownReleased || !released.Remaining.IsZero() {
		t.Fatalf("release = %+v (%v)", released, err)
	}
	var ev rcvevents.DownPaymentData
	lastEvent(t, s, ctx, rcvevents.DownPaymentReleased, &ev)
	if !ev.Amount.Equal(amt("700")) || !ev.Remaining.IsZero() || ev.Postings[0].Account != domain.AccountSecurityDeposit || ev.Postings[1].Account != domain.AccountBank ||
		!ev.Postings[1].Credit.Equal(amt("700")) {
		t.Fatalf("released event = %+v, want Dr 2195 / Cr bank 700.00", ev)
	}

	// A down payment clears invoices only, and an unpaid request can be cancelled.
	dp, err := s.CreateDownPayment(ctx, DownPayment{LegalEntityID: legalEntity, CustomerID: custA, Kind: domain.KindDownPayment, Currency: "USD", Amount: amt("50.00")}, "dp-2")
	if err != nil {
		t.Fatalf("down payment: %v", err)
	}
	cancelled, err := s.CancelDownPayment(ctx, dp.ID, dp.Version)
	if err != nil || cancelled.Status != domain.DownCancelled || !cancelled.Remaining.IsZero() {
		t.Fatalf("cancel = %+v (%v)", cancelled, err)
	}
	if _, err := s.ReceiveDownPayment(ctx, cancelled.ID, cancelled.Version, ""); problemCode(err) != domain.CodeDownPaymentState {
		t.Fatalf("receiving a cancelled request: %v", err)
	}
	dp3, _ := s.CreateDownPayment(ctx, DownPayment{LegalEntityID: legalEntity, CustomerID: custA, Kind: domain.KindDownPayment, Currency: "USD", Amount: amt("50.00")}, "dp-3")
	dp3, _ = s.ReceiveDownPayment(ctx, dp3.ID, dp3.Version, "")
	fee2, _ := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: custA, SourceType: "FEE", DocumentNumber: "FEE-2",
		DocumentDate: "2027-03-01", DueDate: "2027-03-15", Currency: "USD", Amount: amt("10.00"), OpenAmount: amt("10.00")})
	if _, err := s.ClearDownPayment(ctx, dp3.ID, dp3.Version, fee2.ID, nil); problemCode(err) != domain.CodeDownPaymentMismatch {
		t.Fatalf("down payment against a fee: %v", err)
	}
	list, err := s.ListDownPayments(ctx, DownPaymentFilter{Kind: domain.KindSecurityDeposit}, 10)
	if err != nil || len(list) != 1 || list[0].ID != dep.ID {
		t.Fatalf("deposits = %+v (%v)", list, err)
	}
	if _, err := s.GetDownPayment(tenantCtx(2), dep.ID); problemCode(err) != CodeDownPaymentNotFound {
		t.Fatalf("other tenant read the deposit: %v", err)
	}
}
