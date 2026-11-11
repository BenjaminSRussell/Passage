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
	idspkg "github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/fxclient"
)

// fakeRates answers by pair and date; a pair or date it does not know is "no rate".
type fakeRates map[string]string

func (f fakeRates) Rate(_ context.Context, _, from, to string, date time.Time) (decimal.Decimal, error) {
	if v, ok := f[from+">"+to+"@"+date.Format(dateLayout)]; ok {
		return decimal.RequireFromString(v), nil
	}
	return decimal.Decimal{}, fxclient.ErrNoRate
}

func fxJournalLines(t *testing.T, s *Postgres, sourceType string) []journalLine {
	t.Helper()
	ctx := tenantCtx(1)
	var lines []journalLine
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `select lines from gl_journal where source_type = $1`, sourceType).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &lines)
	})
	if err != nil {
		t.Fatalf("journal %s: %v", sourceType, err)
	}
	return lines
}

func eurPayment(t *testing.T, s *Postgres, cust, key string) Payment {
	t.Helper()
	pay, err := s.CreatePayment(tenantCtx(1), Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("90.00"), UnappliedAmount: amt("90.00"), Currency: "EUR", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pay
}

// BR-30.4: an EUR payment settles a USD invoice; the realized difference is its own USD journal at 7100/7200.
func TestApplyPayment_CrossCurrencyBooksTheRealizedFXAsItsOwnJournal(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx) // USD 1000.00 dated 2027-03-01
	s.WithRates(fakeRates{})
	pay := eurPayment(t, s, item.CustomerID, "fx-1")
	req := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00"), ItemCurrencyAmount: amt("100.00")}}}

	// Not enabled: refused as before.
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, req, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Fatalf("disabled entity: %v", err)
	}
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, CrossCurrencyEnabled: true, FunctionalCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	// No itemAmount: the caller must say what comes off the item.
	noItem := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00")}}}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, noItem, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Fatalf("missing itemAmount: %v", err)
	}
	// A rate the fx service does not have: 422 and nothing changes.
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, req, ""); problemCode(err) != CodeFXUnavailable {
		t.Fatalf("no rate: %v", err)
	}
	s.WithRates(fakeRates{"EUR>USD@2027-03-08": "1.12"})
	out, err := s.ApplyPayment(ctx, pay.ID, pay.Version, req, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	// 90.00 EUR * 1.12 = 100.80 USD against 100.00 USD carried: a 0.80 gain.
	got := out.Settlements[0]
	if !got.RealizedFX.Equal(amt("0.80")) || got.ItemCurrency != "USD" || !got.OpenAfter.Equal(amt("900.00")) || out.Payment.Status != "APPLIED" {
		t.Fatalf("settlement = %+v payment %s", got, out.Payment.Status)
	}
	var itemAmount, itemCurrency string
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select payload->'data'->'settlements'->0->>'item_amount', payload->'data'->'settlements'->0->>'item_currency' from outbox
			where type = 'dev.passage.receivables.payment.applied.v1'`).Scan(&itemAmount, &itemCurrency)
	}); err != nil || itemAmount != "100" || itemCurrency != "USD" {
		t.Errorf("payment.applied carries item amount %q %q (%v)", itemAmount, itemCurrency, err)
	}
	fxLines := fxJournalLines(t, s, JournalRealizedFX)
	if len(fxLines) != 2 || fxLines[0].Account != "1200" || fxLines[0].Credit || !fxLines[0].Amount.Equal(amt("0.80")) ||
		fxLines[1].Account != "7100" || !fxLines[1].Credit {
		t.Fatalf("fx journal = %+v", fxLines)
	}
	list, err := s.ListGLJournals(ctx, GLJournalFilter{SourceType: JournalRealizedFX})
	if err != nil || len(list) != 1 || list[0].Currency != "USD" {
		t.Fatalf("the fx journal is not in the functional currency: %+v %v", list, err)
	}
	// The application journal stays in the payment currency, and a loss uses 7200.
	if apply, err := s.ListGLJournals(ctx, GLJournalFilter{SourceType: JournalPaymentApply}); err != nil || len(apply) != 1 || apply[0].Currency != "EUR" {
		t.Fatalf("application journal: %+v %v", apply, err)
	}
	pay2 := eurPayment(t, s, item.CustomerID, "fx-2")
	s.WithRates(fakeRates{"EUR>USD@2027-03-08": "1.10"})
	if _, err := s.ApplyPayment(ctx, pay2.ID, pay2.Version, req, ""); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListGLJournals(ctx, GLJournalFilter{SourceType: JournalRealizedFX})
	if err != nil || len(list) != 2 {
		t.Fatalf("second fx journal: %+v %v", list, err)
	}
	var loss GLJournalView
	for _, j := range list {
		if j.Lines[0].Account == "7200" {
			loss = j
		}
	}
	// 90.00 * 1.10 = 99.00 against 100.00: a 1.00 loss, Dr 7200 / Cr 1200.
	if loss.ID == "" || !loss.Lines[0].Amount.Equal(amt("1.00")) || loss.Lines[1].Account != "1200" || !loss.Lines[1].Amount.Equal(amt("1.00")) {
		t.Fatalf("loss journal = %+v", loss)
	}
}

func TestApplyPayment_CrossCurrencyAllocationTakesNoDiscountAndSameCurrencyTakesNoItemAmount(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	s.WithRates(fakeRates{"EUR>USD@2027-03-08": "1.12"})
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, CrossCurrencyEnabled: true, FunctionalCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	pay := eurPayment(t, s, item.CustomerID, "fx-3")
	disc := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00"), CashDiscount: amt("1.00"), ItemCurrencyAmount: amt("100.00")}}}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, disc, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Errorf("discount on a cross-currency allocation: %v", err)
	}
	cust := item.CustomerID
	usd, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("100.00"), UnappliedAmount: amt("100.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "fx-4")
	if err != nil {
		t.Fatal(err)
	}
	same := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("100.00"), ItemCurrencyAmount: amt("100.00")}}}
	if _, err := s.ApplyPayment(ctx, usd.ID, usd.Version, same, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Errorf("itemAmount on a same-currency allocation: %v", err)
	}
}

func TestFXSetting_DefaultsValidationAndRoundTrip(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	got, err := s.GetFXSetting(ctx, legalEntity)
	if err != nil || got.CrossCurrencyEnabled || got.GainAccount != "7100" || got.LossAccount != "7200" {
		t.Fatalf("default = %+v %v", got, err)
	}
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, CrossCurrencyEnabled: true}); err == nil {
		t.Error("enabled without a functional currency was accepted")
	}
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, FunctionalCurrency: "EUR", GainAccount: "7105", LossAccount: "7205"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetFXSetting(ctx, legalEntity)
	if err != nil || got.FunctionalCurrency != "EUR" || got.GainAccount != "7105" || got.LossAccount != "7205" {
		t.Fatalf("stored = %+v %v", got, err)
	}
}

// A receipt reversal credits back the bank account the receipt was booked to, not the one mapped today.
func TestReversePayment_UsesTheOriginalReceiptMapping(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	if err := s.PutBankAccount(ctx, BankAccount{LegalEntityID: legalEntity, Currency: "USD", GLAccount: "1010"}); err != nil {
		t.Fatal(err)
	}
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("50.00"), UnappliedAmount: amt("50.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "rev-1")
	if err != nil {
		t.Fatal(err)
	}
	// The mapping changes after the receipt was booked.
	if err := s.PutBankAccount(ctx, BankAccount{LegalEntityID: legalEntity, Currency: "USD", GLAccount: "1099"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReversePayment(ctx, pay.ID, pay.Version+5); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	out, err := s.ReversePayment(ctx, pay.ID, pay.Version)
	if err != nil || out.Status != "REVERSED" || !out.UnappliedAmount.IsZero() {
		t.Fatalf("reverse: %+v %v", out, err)
	}
	lines := fxJournalLines(t, s, JournalReceiptReversal)
	if len(lines) != 2 || lines[0].Account != "1010" {
		t.Fatalf("reversal lines = %+v", lines)
	}
	var bankLine journalLine
	for _, l := range lines {
		if l.Account != "1205" {
			bankLine = l
		}
	}
	if bankLine.Account != "1010" || !bankLine.Credit || !bankLine.Amount.Equal(amt("50.00")) {
		t.Fatalf("the reversal credits %+v, want 1010 (the original mapping)", bankLine)
	}
	if _, err := s.ReversePayment(ctx, pay.ID, out.Version); problemCode(err) != CodePaymentState {
		t.Errorf("reversing twice: %v", err)
	}
	// An applied payment cannot be reversed.
	cust2 := item.CustomerID
	applied, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust2, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("100.00"), UnappliedAmount: amt("100.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "rev-2")
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.ApplyPayment(ctx, applied.ID, applied.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("100.00")}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReversePayment(ctx, applied.ID, done.Payment.Version); problemCode(err) != CodePaymentState {
		t.Errorf("reversing an applied payment: %v", err)
	}
}

func TestRealizedFXRounding(t *testing.T) {
	fx := domain.ComputeRealizedFX("JPY", amt("100.00"), amt("0.0075"), amt("1"), amt("1"))
	if !fx.PaymentValue.Equal(amt("1")) || !fx.ItemValue.Equal(amt("1")) || !fx.Difference.IsZero() {
		t.Errorf("JPY rounds to whole units: %+v", fx)
	}
	if lines := domain.FXLines(fx, "7100", "7200"); lines != nil {
		t.Errorf("a zero difference books nothing: %+v", lines)
	}
}

// Backfill of the collection-side journals: what dunning, interest, deposits and deductions posted before 30-2 gets its journal under the
// key the live path uses, and a repeat queues nothing.
func TestBackfill_CollectionPostingsGetTheirJournalsOnce(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(15)
	item := seedItem(t, s, ctx, "INV-B1", custA, "2027-01-31", "10000.00", "USD")
	if _, err := s.PostInterest(ctx, InterestRequest{LegalEntityID: legalEntity, CalculationDate: "2027-03-31", AnnualRate: amt("0.12"),
		Basis: domain.BasisAct365, GraceDays: 5}, "bf-int"); err != nil {
		t.Fatal(err)
	}
	dep, err := s.CreateDownPayment(ctx, DownPayment{LegalEntityID: legalEntity, CustomerID: custA, Kind: domain.KindSecurityDeposit, Currency: "USD", Amount: amt("1000.00")}, "bf-dep")
	if err != nil {
		t.Fatal(err)
	}
	if dep, err = s.ReceiveDownPayment(ctx, dep.ID, dep.Version, "2027-03-02"); err != nil {
		t.Fatal(err)
	}
	fee := seedItem(t, s, ctx, "FEE-B1", custA, "2027-03-15", "300.00", "USD")
	if dep, err = s.ClearDownPayment(ctx, dep.ID, dep.Version, fee.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReleaseDownPayment(ctx, dep.ID, dep.Version); err != nil {
		t.Fatal(err)
	}
	run, letter, ded := idspkg.New(), idspkg.New(), idspkg.New()
	execAdmin(t, s, `insert into dunning_run (tenant_id, id, legal_entity_id, run_date, grace_days, status, idempotency_key, issued_at)
		values ($1, $2, $3, '2027-03-31', 5, 'ISSUED', 'bf-run', '2027-03-31T10:00:00Z')`, testkit.TenantID(15), run, legalEntity)
	execAdmin(t, s, `insert into dunning_letter (tenant_id, id, run_id, legal_entity_id, customer_id, currency, level, prior_level, amount, fee_amount, items, status, issued_at)
		values ($1, $2, $3, $4, $5, 'USD', 2, 1, 500, 25, '[]', 'ISSUED', '2027-03-31T10:00:00Z')`, testkit.TenantID(15), letter, run, legalEntity, custA)
	execAdmin(t, s, `insert into deduction (tenant_id, id, legal_entity_id, customer_id, open_item_id, deduction_type, reason_code, currency, amount, status, idempotency_key, resolved_at)
		values ($1, $2, $3, $4, $5, 'PRICE', 'R1', 'USD', 42.50, 'WRITTEN_OFF', 'bf-ded', '2027-04-02T10:00:00Z')`, testkit.TenantID(15), ded, legalEntity, custA, item.ID)
	// Everything above posted its journal live except the rows inserted directly; drop the live ones to look like history.
	execAdmin(t, s, `delete from gl_journal where source_type in ('interest', 'down_payment', 'dunning_fee', 'deduction')`)

	execAdmin(t, s, `update down_payment set released_at = '2027-05-05T10:00:00Z' where id = $1`, dep.ID)
	first, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity})
	if err != nil {
		t.Fatal(err)
	}
	if first.DunningFees != 1 || first.Interest != 1 || first.DownPayments != 3 || first.Deductions != 1 {
		t.Fatalf("backfill = %+v", first)
	}
	by := map[string][]journalRow{}
	for _, j := range journals(t, s, ctx) {
		by[j.sourceType] = append(by[j.sourceType], j)
	}
	if f := by[JournalDunningFee]; len(f) != 1 || f[0].debits["1200"] != "25" || f[0].credits["4300"] != "25" || f[0].date != "2027-03-31" {
		t.Errorf("fee journal = %+v", f)
	}
	if d := by[JournalDeduction]; len(d) != 1 || d[0].debits["6110"] != "42.5" || d[0].credits["1200"] != "42.5" || d[0].date != "2027-04-02" {
		t.Errorf("deduction journal = %+v", d)
	}
	if n := len(by[JournalInterest]); n != 1 {
		t.Errorf("interest journals = %d", n)
	}
	for _, j := range by[JournalDownPayment] {
		if strings.HasSuffix(j.key, ":release") && j.date != "2027-05-05" {
			t.Errorf("the release journal is dated %s, want the release date 2027-05-05", j.date)
		}
	}
	if n := len(by[JournalDownPayment]); n != 3 {
		t.Errorf("down payment journals = %d, want receipt, clearing and release", n)
	}
	second, err := s.BackfillGLJournals(ctx, BackfillRequest{LegalEntityID: legalEntity})
	if err != nil || second.DunningFees+second.Interest+second.DownPayments+second.Deductions != 0 {
		t.Errorf("a repeat queued more: %+v %v", second, err)
	}
}

// A statement and a balance confirmation count what a cross-currency settlement took off the item (item_amount, in the item's currency),
// not the cash in the payment currency.
func TestCrossCurrencySettlement_StatementAndConfirmationUseTheItemAmount(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx) // USD 1000.00
	s.WithRates(fakeRates{"EUR>USD@2027-03-08": "1.12"})
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, CrossCurrencyEnabled: true, FunctionalCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	pay := eurPayment(t, s, item.CustomerID, "st-1") // EUR 90.00
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00"), ItemCurrencyAmount: amt("100.00")}}}, ""); err != nil {
		t.Fatal(err)
	}
	asOf := parseDay("2027-12-31")
	sections, err := s.Statement(ctx, legalEntity, item.CustomerID, asOf)
	if err != nil {
		t.Fatal(err)
	}
	var open decimal.Decimal
	for _, sec := range sections {
		for _, l := range sec.Lines {
			if l.DocumentNumber == "INV-30001" {
				open = l.OpenAmount
			}
		}
	}
	if !open.Equal(amt("900.00")) {
		t.Errorf("the statement shows %s open, want 900.00 (not 910.00)", open)
	}
	got, err := asOfBalancesFor(t, s, ctx, "2027-12-31")
	if err != nil || !got.Equal(amt("900.00")) {
		t.Errorf("balance confirmation basis = %s (%v), want 900.00", got, err)
	}
	// Settle the rest in full: the item is gone from both.
	pay2 := eurPayment(t, s, item.CustomerID, "st-2")
	execAdmin(t, s, `update payment set amount = 800, unapplied_amount = 800 where id = $1`, pay2.ID)
	pay2, _ = s.GetPayment(ctx, pay2.ID)
	if _, err := s.ApplyPayment(ctx, pay2.ID, pay2.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("800.00"), ItemCurrencyAmount: amt("900.00")}}}, ""); err != nil {
		t.Fatal(err)
	}
	if got, err := asOfBalancesFor(t, s, ctx, "2027-12-31"); err != nil || !got.IsZero() {
		t.Errorf("after the full settlement the balance is %s (%v), want 0", got, err)
	}
}

func asOfBalancesFor(t *testing.T, s *Postgres, ctx context.Context, asOf string) (decimal.Decimal, error) {
	t.Helper()
	total := decimal.Zero
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		bs, err := asOfBalances(ctx, tx, legalEntity, asOf)
		for _, b := range bs {
			if b.Currency == "USD" {
				total = total.Add(b.Balance)
			}
		}
		return err
	})
	return total, err
}

// The item amount must be what the cash is worth: GBP 1 cannot close a EUR 10,000 invoice into a realized loss, and the item amount has no
// more decimals than the item's currency.
func TestApplyPayment_CrossCurrencyItemAmountMustMatchTheCash(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	s.WithRates(fakeRates{"EUR>USD@2027-03-08": "1.12"})
	if _, err := s.PutFXSetting(ctx, FXSetting{LegalEntityID: legalEntity, CrossCurrencyEnabled: true, FunctionalCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	pay := eurPayment(t, s, item.CustomerID, "tol-1")
	big := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00"), ItemCurrencyAmount: amt("1000.00")}}}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, big, ""); problemCode(err) != CodeFXTolerance {
		t.Fatalf("a 1000.00 item amount for 90.00 EUR: %v", err)
	}
	fine := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00"), ItemCurrencyAmount: amt("100.005")}}}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, fine, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Fatalf("too many decimals: %v", err)
	}
	// Within the 5.00 tolerance (cash worth 100.80): accepted.
	ok := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("90.00"), ItemCurrencyAmount: amt("104.00")}}}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ok, ""); err != nil {
		t.Fatalf("within tolerance: %v", err)
	}
}

func TestReversePayment_RefusedWhenItConfirmedARemittanceAdvice(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	mk := func(key string) Payment {
		p, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
			Amount: amt("50.00"), UnappliedAmount: amt("50.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, key)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	bank, advised := mk("adv-1"), mk("adv-2")
	execAdmin(t, s, `update payment set status = 'CONFIRMED', confirmed_by_payment_id = $2 where id = $1`, advised.ID, bank.ID)
	if _, err := s.ReversePayment(ctx, bank.ID, bank.Version); problemCode(err) != CodePaymentState {
		t.Fatalf("reversing the confirming payment: %v", err)
	}
}
