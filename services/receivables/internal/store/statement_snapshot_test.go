package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

func seedDoc(t *testing.T, s *Postgres, ctx context.Context, doc, cust, docDate, due, amount, currency string) OpenItem {
	t.Helper()
	item, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: cust, SourceType: "INVOICE", DocumentNumber: doc,
		DocumentDate: docDate, DueDate: due, Currency: currency, Amount: amt(amount), OpenAmount: amt(amount)})
	if err != nil {
		t.Fatalf("seed %s: %v", doc, err)
	}
	return item
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type statementRow struct{ source, doc, open, running string }

func statementRows(sec domain.StatementSection) []statementRow {
	var out []statementRow
	for _, l := range sec.Lines {
		out = append(out, statementRow{l.SourceType, l.DocumentNumber, l.OpenAmount.StringFixed(2), l.RunningBalance.StringFixed(2)})
	}
	return out
}

// FR-30.13 and BR-30.10: the statement lists what was open at the end of the
// statement date. Settlements, deductions and payments booked later are added
// back, a deduction being worked is its own line, unapplied cash is a credit,
// and reading the statement changes nothing.
func TestStatement_AsOfDatesDeductionsAndUnappliedCash(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(21)
	now := time.Now().UTC()
	todayText := now.Format(dateLayout)
	a := seedDoc(t, s, ctx, "INV-S1", custA, "2026-01-05", "2026-02-04", "1000.00", "USD")
	seedDoc(t, s, ctx, "INV-S2", custA, now.AddDate(0, 0, -1).Format(dateLayout), now.AddDate(0, 0, 30).Format(dateLayout), "500.00", "USD")
	seedDoc(t, s, ctx, "INV-S3", custA, "2026-01-10", "2026-02-09", "250.00", "EUR")
	seedDoc(t, s, ctx, "INV-OTHER", custB, "2026-01-05", "2026-02-04", "999.00", "USD")

	// Today the customer pays 700.00: 600.00 to INV-S1, 100.00 left on account.
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: ptr(custA), PaymentMode: "WIRE", Source: "MANUAL",
		SourceRef: ptr("WIRE-77"), Amount: amt("700.00"), Currency: "USD", ReceivedDate: todayText, ValueDate: todayText}, "stmt-pay")
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{
		Allocations: []domain.Allocation{{OpenItemID: a.ID, Amount: amt("600.00")}}, Residual: amt("100.00")}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// And takes 300.00 off INV-S1 as a deduction still being worked.
	if _, err := s.CreateDeduction(ctx, Deduction{LegalEntityID: legalEntity, OpenItemID: a.ID, PaymentID: &pay.ID, Type: "PROMO",
		ReasonCode: "promo", Amount: amt("300.00")}, "stmt-ded"); err != nil {
		t.Fatalf("deduction: %v", err)
	}
	outboxBefore := countOutbox(t, s, ctx, rcvevents.PaymentApplied)

	current, err := s.Statement(ctx, legalEntity, custA, now)
	if err != nil {
		t.Fatalf("statement today: %v", err)
	}
	if len(current) != 2 || current[0].Currency != "EUR" || current[1].Currency != "USD" {
		t.Fatalf("sections = %+v, want EUR and USD", current)
	}
	got := statementRows(current[1])
	want := []statementRow{
		{"INVOICE", "INV-S1", "100.00", "100.00"},
		{"INVOICE", "INV-S2", "500.00", "600.00"},
		{"DEDUCTION", "INV-S1", "300.00", "900.00"},
		{"PAYMENT", "WIRE-77", "-100.00", "800.00"},
	}
	if len(got) != len(want) {
		t.Fatalf("USD lines = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	usd := current[1]
	if !usd.ClosingBalance.Equal(amt("800")) || usd.Lines[2].DueDate != nil || usd.Lines[3].DueDate != nil {
		t.Errorf("closing %s, due dates %v / %v; want 800 and no due date on the deduction and the payment",
			usd.ClosingBalance, usd.Lines[2].DueDate, usd.Lines[3].DueDate)
	}
	// The aging footer covers the debits only: INV-S1 is long overdue, INV-S2 is current.
	if len(usd.Aging) != 2 || usd.Aging[0].Bucket != domain.BucketCurrent || !usd.Aging[0].Amount.Equal(amt("500")) ||
		usd.Aging[1].Bucket != domain.BucketOver180 || !usd.Aging[1].Amount.Equal(amt("100")) {
		t.Errorf("aging = %+v", usd.Aging)
	}
	// Today's debit lines agree with the open items.
	if it := openItem(t, s, ctx, a.ID); !it.OpenAmount.Equal(usd.Lines[0].OpenAmount) {
		t.Errorf("INV-S1 open %s, statement %s", it.OpenAmount, usd.Lines[0].OpenAmount)
	}

	// At the end of 2026-01-31 only INV-S1 and INV-S3 existed and nothing was paid yet.
	past, err := s.Statement(ctx, legalEntity, custA, day(t, "2026-01-31"))
	if err != nil {
		t.Fatalf("statement in the past: %v", err)
	}
	if len(past) != 2 || len(past[1].Lines) != 1 || past[1].Lines[0].DocumentNumber != "INV-S1" ||
		!past[1].Lines[0].OpenAmount.Equal(amt("1000")) || !past[1].ClosingBalance.Equal(amt("1000")) {
		t.Fatalf("statement 2026-01-31 = %+v, want INV-S1 fully open", past)
	}
	if len(past[1].Aging) != 1 || past[1].Aging[0].Bucket != domain.BucketCurrent {
		t.Errorf("aging 2026-01-31 = %+v, want INV-S1 current (due 2026-02-04)", past[1].Aging)
	}
	// Before any document: nothing.
	if none, err := s.Statement(ctx, legalEntity, custA, day(t, "2025-12-31")); err != nil || len(none) != 0 {
		t.Fatalf("statement before the first document = %+v (%v)", none, err)
	}
	// Read-only: no event and no change to the items.
	if n := countOutbox(t, s, ctx, rcvevents.PaymentApplied); n != outboxBefore {
		t.Errorf("statement emitted events: %d -> %d", outboxBefore, n)
	}
	// Another tenant sees nothing; another legal entity is empty.
	if other, err := s.Statement(tenantCtx(22), legalEntity, custA, now); err != nil || len(other) != 0 {
		t.Errorf("another tenant's statement = %+v (%v)", other, err)
	}
	if other, err := s.Statement(ctx, "00000000-0000-0000-0000-0000000000a2", custA, now); err != nil || len(other) != 0 {
		t.Errorf("another legal entity's statement = %+v (%v)", other, err)
	}
}

// §5.30.16: the snapshot stores one balance per customer and currency for the
// date, emits customerbalance.changed.v1 only when a balance moved, writes a
// zero row (and one event) for a customer whose items all settled, and lists
// the latest date's rows riskiest first.
func TestAgingSnapshot_ChangesOnlyAndZeroRows(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(23)
	now := time.Now().UTC()
	asOf := day(t, now.Format(dateLayout))
	a1 := seedDoc(t, s, ctx, "INV-N1", custA, "2026-01-05", "2026-02-04", "400.00", "USD")
	seedDoc(t, s, ctx, "INV-N2", custA, "2026-01-05", now.AddDate(0, 0, 20).Format(dateLayout), "100.00", "USD")
	b1 := seedDoc(t, s, ctx, "INV-N3", custB, "2026-01-05", "2026-02-04", "50.00", "EUR")

	first, err := s.RunAgingSnapshot(ctx, legalEntity, asOf)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Rows != 2 || first.Changed != 2 || first.AsOf != asOf.Format(dateLayout) {
		t.Fatalf("first run = %+v, want 2 rows and 2 changes", first)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CustomerBalanceChanged); n != 2 {
		t.Fatalf("events after the first run = %d, want 2", n)
	}
	data := latestBalanceEvent(t, s, ctx, custA)
	if data.Currency != "USD" || !data.Balance.Equal(amt("500")) || !data.Overdue.Equal(amt("400")) || data.OldestItemDays <= 0 ||
		data.AsOf != asOf.Format(dateLayout) || data.LegalEntityID != legalEntity {
		t.Fatalf("event payload = %+v, want 500 open with 400 overdue", data)
	}

	// The same date again with nothing moved: rows rewritten, no event.
	again, err := s.RunAgingSnapshot(ctx, legalEntity, asOf)
	if err != nil || again.Rows != 2 || again.Changed != 0 {
		t.Fatalf("re-run = %+v (%v), want 2 rows and no change", again, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CustomerBalanceChanged); n != 2 {
		t.Fatalf("events after the re-run = %d, want still 2", n)
	}

	// Customer A pays INV-N1; customer B's only item is written off in full.
	settle(t, s, ctx, a1, custA, "400.00", "USD", "snap-a")
	settle(t, s, ctx, b1, custB, "50.00", "EUR", "snap-b")
	moved, err := s.RunAgingSnapshot(ctx, legalEntity, asOf)
	if err != nil || moved.Rows != 2 || moved.Changed != 2 {
		t.Fatalf("after payments = %+v (%v), want A's new balance and B's zero row", moved, err)
	}
	if d := latestBalanceEvent(t, s, ctx, custA); !d.Balance.Equal(amt("100")) || !d.Overdue.IsZero() {
		t.Errorf("A's event = %+v, want 100 open, nothing overdue", d)
	}
	if d := latestBalanceEvent(t, s, ctx, custB); !d.Balance.IsZero() || d.Currency != "EUR" || d.RiskScore != 0 {
		t.Errorf("B's event = %+v, want a zero EUR balance", d)
	}
	// B's zero row is not re-announced.
	if quiet, err := s.RunAgingSnapshot(ctx, legalEntity, asOf); err != nil || quiet.Changed != 0 || quiet.Rows != 1 {
		t.Fatalf("after the zero row = %+v (%v), want A's row only and no change", quiet, err)
	}

	rows, err := s.ListBalanceSnapshots(ctx, SnapshotFilter{LegalEntityID: legalEntity}, 50)
	if err != nil || len(rows) != 2 {
		t.Fatalf("latest snapshots = %+v (%v), want A and B", rows, err)
	}
	if rows[0].RiskScore < rows[1].RiskScore || rows[0].AsOf != asOf.Format(dateLayout) {
		t.Errorf("rows not riskiest first: %+v", rows)
	}
	onlyB, err := s.ListBalanceSnapshots(ctx, SnapshotFilter{LegalEntityID: legalEntity, AsOf: asOf.Format(dateLayout), CustomerID: custB}, 50)
	if err != nil || len(onlyB) != 1 || !onlyB[0].TotalOpen.IsZero() {
		t.Errorf("B's snapshot = %+v (%v), want the zero row", onlyB, err)
	}
	if none, err := s.ListBalanceSnapshots(ctx, SnapshotFilter{LegalEntityID: legalEntity, AsOf: "2020-01-01"}, 50); err != nil || len(none) != 0 {
		t.Errorf("snapshots of a date never run = %+v (%v)", none, err)
	}
	if other, err := s.ListBalanceSnapshots(tenantCtx(24), SnapshotFilter{LegalEntityID: legalEntity}, 50); err != nil || len(other) != 0 {
		t.Errorf("another tenant sees %d snapshots (%v)", len(other), err)
	}
}

// settle pays an item in full with a manual payment of its customer.
func settle(t *testing.T, s *Postgres, ctx context.Context, item OpenItem, cust, amount, currency, key string) {
	t.Helper()
	pay := newPayment(t, s, ctx, ptr(cust), amount, currency, key)
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt(amount)}}}, ""); err != nil {
		t.Fatalf("settle %s: %v", item.DocumentNumber, err)
	}
}

func latestBalanceEvent(t *testing.T, s *Postgres, ctx context.Context, cust string) rcvevents.CustomerBalanceChangedData {
	t.Helper()
	var raw []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select payload from outbox where type = $1 and aggregate_id = $2 order by created_at desc, id desc limit 1`,
			rcvevents.CustomerBalanceChanged, cust).Scan(&raw)
	})
	if err != nil {
		t.Fatalf("read balance event: %v", err)
	}
	var evt struct {
		Data rcvevents.CustomerBalanceChangedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatalf("decode balance event: %v", err)
	}
	return evt.Data
}

// BR-30.1 R4 and R5: applying a payment teaches the payer's account hash and
// name for the customer; the next payment from that account, naming the invoice,
// then scores at the auto-apply threshold although it is unidentified. Its amount
// differs from the invoice, so it stays a suggestion (reference-only never auto-applies).
func TestPayerIdentity_LearnedOnApplyScoresTheNextMatchButStaysSuggested(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(25)
	hash := domain.AccountHash("DE89 3704 0044 0532 0130 00")
	first := seedDoc(t, s, ctx, "INV-P1", custA, "2027-01-05", "2027-02-04", "300.00", "USD")
	second := seedDoc(t, s, ctx, "INV-P2", custA, "2027-01-05", "2027-02-04", "1000.00", "USD")
	seedDoc(t, s, ctx, "INV-P3", custB, "2027-01-05", "2027-02-04", "1000.00", "USD")

	base := Payment{LegalEntityID: legalEntity, PaymentMode: "WIRE", Source: "MANUAL", Currency: "USD", ReceivedDate: "2027-03-20",
		ValueDate: "2027-03-20", PayerName: ptr("Acme Inc."), PayerAccountHash: &hash}
	known := base
	known.CustomerID, known.Amount = ptr(custA), amt("300.00")
	paid, err := s.CreatePayment(ctx, known, "payer-1")
	if err != nil {
		t.Fatalf("first payment: %v", err)
	}
	if paid.PayerAccountHash == nil || *paid.PayerAccountHash != hash {
		t.Fatalf("stored hash = %v", paid.PayerAccountHash)
	}
	// Before anything is learned, a reference alone is only a suggestion.
	next := base
	next.Amount, next.PayerName = amt("400.00"), ptr("ACME, INC")
	next.Remittance = []domain.RemittanceLine{{Reference: "INV-P2", Amount: amt("400.00")}}
	unknown, err := s.CreatePayment(ctx, next, "payer-2")
	if err != nil {
		t.Fatalf("second payment: %v", err)
	}
	if res, err := s.MatchPayment(ctx, unknown.ID); err != nil || res.Decision != domain.DecisionSuggested {
		t.Fatalf("match before learning = %+v (%v), want SUGGESTED", res, err)
	}

	if _, err := s.ApplyPayment(ctx, paid.ID, paid.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: first.ID, Amount: amt("300.00")}}}, ""); err != nil {
		t.Fatalf("apply: %v", err)
	}
	res, err := s.MatchPayment(ctx, unknown.ID)
	if err != nil {
		t.Fatalf("match after learning: %v", err)
	}
	if res.Decision != domain.DecisionSuggested || res.Top[0].ItemIDs[0] != second.ID {
		t.Fatalf("match after learning = %+v, want SUGGESTED for INV-P2", res)
	}
	rules := map[string]bool{}
	for _, r := range res.Top[0].Rules {
		rules[r] = true
	}
	if !rules["R1"] || !rules["R4"] || !rules["R5"] || res.Top[0].Score != 85 {
		t.Fatalf("top candidate = %+v, want R1 + R4 + R5 = 85", res.Top[0])
	}
	// The bindings are the tenant's own.
	var n int
	if err := dbx.WithTenantTx(tenantCtx(26), s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from payer_account_binding`).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("another tenant reads %d bindings (%v)", n, err)
	}

	// Idempotent replay compares the payer identity too.
	changed := known
	changed.ShortPayReason = ptr("promo")
	if _, err := s.CreatePayment(ctx, changed, "payer-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("same key with another short-pay reason: %v, want %s", err, CodeIdempotencyReuse)
	}
}

// R7: a short payment with a reason scores the item it falls just short of.
func TestPayerIdentity_ShortPayWithReason(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(27)
	item := seedDoc(t, s, ctx, "INV-R7", custA, "2027-01-05", "2027-03-31", "1000.00", "USD")
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: ptr(custA), PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("995.00"), Currency: "USD", ReceivedDate: "2027-03-28", ValueDate: "2027-03-28", ShortPayReason: ptr("damaged pallet"),
		Remittance: []domain.RemittanceLine{{Reference: "INV-R7", Amount: amt("995.00")}}}, "r7")
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	res, err := s.MatchPayment(ctx, pay.ID)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if len(res.Top) == 0 || res.Top[0].ItemIDs[0] != item.ID || res.Top[0].Score != 60+20+5 || res.Decision != domain.DecisionAutoApply {
		t.Fatalf("match = %+v, want R1 + R7 + R6 = 85 on INV-R7", res)
	}
}

// camt.053: the debtor account hash travels to the payment for R4.
func TestImportBankStatement_KeepsTheDebtorAccountHash(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(28)
	credit := domain.CAMTCredit{Line: 8, StatementID: "STMT-1", Reference: "E-1", Currency: "EUR", Amount: amt("10.00"), ValueDate: "2027-03-20",
		DebtorName: "Acme GmbH", DebtorAccountHash: domain.AccountHash("FR1420041010050500013M02606")}
	batch, err := s.ImportBankStatement(ctx, BankStatementImport{LegalEntityID: legalEntity, FileSHA256: strings.Repeat("e", 64),
		StatementCount: 1, Credits: []domain.CAMTCredit{credit}})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	got := batch.Payments[0].PayerAccountHash
	if got == nil || *got != credit.DebtorAccountHash {
		t.Fatalf("payer account hash = %v, want the debtor's", got)
	}
	if !batch.Payments[0].Amount.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("payment = %+v", batch.Payments[0])
	}
}
