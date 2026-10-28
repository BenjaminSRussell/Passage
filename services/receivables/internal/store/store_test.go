package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/migrations"
)

const legalEntity = "00000000-0000-0000-0000-0000000000a1"

func tenantCtx(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), Duties: map[string]struct{}{}})
}

func setup(t *testing.T) *Postgres {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	adminPools.Store(app, admin)
	return New(app)
}

var adminPools sync.Map

func execAdmin(t *testing.T, s *Postgres, sql string, args ...any) {
	t.Helper()
	admin, _ := adminPools.Load(s.pool)
	if _, err := admin.(*pgxpool.Pool).Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func ptr[T any](v T) *T { return &v }

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func amt(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// seedInvoice posts a 1,000.00 USD invoice with 2% 10 net 30 terms.
func seedInvoice(t *testing.T, s *Postgres, ctx context.Context) OpenItem {
	t.Helper()
	item, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: "00000000-0000-0000-0000-0000000000c1",
		SourceType: "INVOICE", DocumentNumber: "INV-30001", DocumentDate: "2027-03-01", DueDate: "2027-03-31", Currency: "USD",
		Amount: amt("1000.00"), OpenAmount: amt("1000.00"), CashDiscountDate: ptr("2027-03-11"), CashDiscountAmount: amt("20.00")})
	if err != nil {
		t.Fatalf("create open item: %v", err)
	}
	return item
}

func TestPayment_IdempotentCreateAndAutoMatch(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	in := Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL", SourceRef: ptr("ACH-7"),
		Amount: amt("980.00"), UnappliedAmount: amt("980.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08",
		Remittance: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("980.00")}}}

	first, err := s.CreatePayment(ctx, in, "key-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	replay, err := s.CreatePayment(ctx, in, "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay returned %v (%v), want %s", replay.ID, err, first.ID)
	}
	changed := in
	changed.Amount, changed.UnappliedAmount = amt("990.00"), amt("990.00")
	if _, err := s.CreatePayment(ctx, changed, "key-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("different body under the same key: got %v", err)
	}

	res, err := s.MatchPayment(ctx, first.ID)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Decision != domain.DecisionAutoApply || res.Top[0].ItemIDs[0] != item.ID {
		t.Fatalf("match = %+v, want auto-apply to the invoice", res)
	}
}

func TestApply_SettlesWithDiscountAndGuardsVersion(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "ACH", Source: "MANUAL",
		Amount: amt("980.00"), UnappliedAmount: amt("980.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "key-2")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	plan := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("980.00"), CashDiscount: amt("20.00")}}, Residual: decimal.Zero}

	if _, err := s.ApplyPayment(ctx, pay.ID, 99, plan, ""); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: got %v, want %s", err, CodeVersionConflict)
	}
	out, err := s.ApplyPayment(ctx, pay.ID, pay.Version, plan, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Payment.Status != "APPLIED" || out.Payment.Version != pay.Version+1 || !out.Payment.UnappliedAmount.IsZero() {
		t.Fatalf("payment after apply = %+v", out.Payment)
	}
	if len(out.Settlements) != 1 || out.Settlements[0].ItemStatus != "SETTLED" || !out.Settlements[0].OpenAfter.IsZero() {
		t.Fatalf("settlements = %+v, want the item SETTLED", out.Settlements)
	}
	open, err := s.ListOpenItems(ctx, OpenItemFilter{OpenOnly: true}, 10)
	if err != nil || len(open) != 0 {
		t.Fatalf("open items after settlement = %+v (%v), want none", open, err)
	}
	if _, err := s.ApplyPayment(ctx, pay.ID, out.Payment.Version, plan, ""); problemCode(err) != CodePaymentState {
		t.Fatalf("re-apply: got %v, want %s", err, CodePaymentState)
	}
}

func TestApply_ResidualGoesOnAccount(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("1500.00"), UnappliedAmount: amt("1500.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "key-3")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{
		Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("1000.00")}}, Residual: amt("500.00")}, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Payment.Status != "ON_ACCOUNT" || !out.Payment.UnappliedAmount.Equal(amt("500.00")) {
		t.Fatalf("payment = %+v, want ON_ACCOUNT with 500.00 unapplied", out.Payment)
	}
}

func TestRLS_OtherTenantSeesNothing(t *testing.T) {
	s := setup(t)
	item := seedInvoice(t, s, tenantCtx(1))
	cust := item.CustomerID
	pay, err := s.CreatePayment(tenantCtx(1), Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "CHECK", Source: "MANUAL",
		Amount: amt("10.00"), Currency: "USD", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, "rls-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.GetPayment(tenantCtx(2), pay.ID); problemCode(err) != CodePaymentNotFound {
		t.Fatalf("cross-tenant read: got %v, want %s", err, CodePaymentNotFound)
	}
	if _, err := s.ApplyPayment(tenantCtx(2), pay.ID, pay.Version, ApplyRequest{Residual: amt("10.00")}, ""); problemCode(err) != CodePaymentNotFound {
		t.Fatalf("cross-tenant apply: got %v, want %s", err, CodePaymentNotFound)
	}
	open, err := s.ListOpenItems(tenantCtx(2), OpenItemFilter{OpenOnly: true}, 10)
	if err != nil || len(open) != 0 {
		t.Fatalf("tenant 2 open items = %+v (%v), want none", open, err)
	}
	pays, err := s.ListPayments(tenantCtx(2), PaymentFilter{}, 10)
	if err != nil || len(pays) != 0 {
		t.Fatalf("tenant 2 payments = %+v (%v), want none", pays, err)
	}
	rows, err := s.AgingRows(tenantCtx(2))
	if err != nil || len(rows) != 0 {
		t.Fatalf("tenant 2 aging rows = %+v (%v), want none", rows, err)
	}
}

func TestRLS_LegalEntityScopeHidesOtherEntities(t *testing.T) {
	s := setup(t)
	seedInvoice(t, s, tenantCtx(1))
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: "00000000-0000-0000-0000-0000000000b2"})
	open, err := s.ListOpenItems(scoped, OpenItemFilter{}, 10)
	if err != nil || len(open) != 0 {
		t.Fatalf("other legal entity sees %+v (%v), want none", open, err)
	}
}

func newPayment(t *testing.T, s *Postgres, ctx context.Context, customer *string, amount, currency, key string) Payment {
	t.Helper()
	pay, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: customer, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt(amount), Currency: currency, ReceivedDate: "2027-03-08", ValueDate: "2027-03-08"}, key)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	return pay
}

func TestApply_RejectsOtherCurrencyCustomerAndExcessDiscount(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	other := "00000000-0000-0000-0000-0000000000c2"

	eur := newPayment(t, s, ctx, &cust, "1000.00", "EUR", "cur-1")
	if _, err := s.ApplyPayment(ctx, eur.ID, eur.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("1000.00")}}}, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Fatalf("EUR payment on a USD item: got %v, want %s", err, domain.CodeApplyMismatch)
	}
	foreign := newPayment(t, s, ctx, &other, "1000.00", "USD", "cust-1")
	if _, err := s.ApplyPayment(ctx, foreign.ID, foreign.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("1000.00")}}}, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Fatalf("another customer's payment: got %v, want %s", err, domain.CodeApplyMismatch)
	}
	greedy := newPayment(t, s, ctx, &cust, "950.00", "USD", "disc-1")
	plan := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("950.00"), CashDiscount: amt("50.00")}}}
	if _, err := s.ApplyPayment(ctx, greedy.ID, greedy.Version, plan, ""); problemCode(err) != domain.CodeApplyMismatch {
		t.Fatalf("discount above terms: got %v, want %s", err, domain.CodeApplyMismatch)
	}
	open, err := s.ListOpenItems(ctx, OpenItemFilter{OpenOnly: true}, 10)
	if err != nil || len(open) != 1 || !open[0].OpenAmount.Equal(amt("1000.00")) {
		t.Fatalf("rejected applies must leave the item untouched, got %+v (%v)", open, err)
	}
}

func TestApply_UnidentifiedPaymentTakesTheCustomerOfItsItems(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	pay := newPayment(t, s, ctx, nil, "400.00", "USD", "anon-1")
	out, err := s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("400.00")}}}, "")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Payment.CustomerID == nil || *out.Payment.CustomerID != item.CustomerID {
		t.Fatalf("payment customer = %v, want %s", out.Payment.CustomerID, item.CustomerID)
	}
	if out.Settlements[0].ItemStatus != "PARTIALLY_SETTLED" || !out.Settlements[0].OpenAfter.Equal(amt("600.00")) {
		t.Fatalf("settlement = %+v, want PARTIALLY_SETTLED with 600.00 open", out.Settlements[0])
	}
}

func TestApply_IdempotencyKeyReplaysAndRejectsReuse(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	pay := newPayment(t, s, ctx, &cust, "1000.00", "USD", "idem-pay")
	plan := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("1000.00")}}}
	first, err := s.ApplyPayment(ctx, pay.ID, pay.Version, plan, "apply-1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	again, err := s.ApplyPayment(ctx, pay.ID, pay.Version, plan, "apply-1")
	if err != nil {
		t.Fatalf("retry with the same key must replay, got %v", err)
	}
	if again.Payment.Version != first.Payment.Version || again.Payment.Status != "APPLIED" || len(again.Settlements) != 1 {
		t.Fatalf("replay = %+v, want the first response %+v", again, first)
	}
	changed := ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("900.00")}}, Residual: amt("100.00")}
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, changed, "apply-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("same key, different plan: got %v, want %s", err, CodeIdempotencyReuse)
	}
	if n := countOutbox(t, s, ctx, "dev.passage.receivables.payment.applied.v1"); n != 1 {
		t.Fatalf("applied events = %d, want exactly 1 after a replay", n)
	}
}

func TestApply_LockedItemIsAR0002(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	pay := newPayment(t, s, ctx, &cust, "1000.00", "USD", "lock-pay")

	holder, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `select 1 from open_item where id = $1 for update`, item.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyPayment(ctx, pay.ID, pay.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: item.ID, Amount: amt("1000.00")}}}, "")
	if problemCode(err) != domain.CodeItemLocked {
		t.Fatalf("apply on a locked item: got %v, want %s", err, domain.CodeItemLocked)
	}
}

func TestMatch_IgnoresItemsInOtherCurrencies(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	cust := item.CustomerID
	eur, err := s.CreatePayment(ctx, Payment{LegalEntityID: legalEntity, CustomerID: &cust, PaymentMode: "WIRE", Source: "MANUAL",
		Amount: amt("1000.00"), Currency: "EUR", ReceivedDate: "2027-03-08", ValueDate: "2027-03-08",
		Remittance: []domain.RemittanceLine{{Reference: "INV-30001", Amount: amt("1000.00")}}}, "eur-match")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err := s.MatchPayment(ctx, eur.ID)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	if res.Decision != domain.DecisionOnAccount || len(res.Top) != 0 {
		t.Fatalf("EUR payment matched a USD item: %+v", res)
	}
}

func TestAgingRowsAndPaymentList(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	item := seedInvoice(t, s, ctx)
	if _, err := s.CreateOpenItem(ctx, OpenItem{LegalEntityID: legalEntity, CustomerID: item.CustomerID, SourceType: "INVOICE",
		DocumentNumber: "INV-30002", DocumentDate: "2027-03-01", DueDate: "2027-03-31", Currency: "USD",
		Amount: amt("250.50"), OpenAmount: amt("250.50")}); err != nil {
		t.Fatalf("second item: %v", err)
	}
	rows, err := s.AgingRows(ctx)
	if err != nil {
		t.Fatalf("aging rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Currency != "USD" || !rows[0].OpenAmount.Equal(amt("1250.50")) {
		t.Fatalf("aging rows = %+v, want one USD row of 1250.50", rows)
	}
	cust := item.CustomerID
	newPayment(t, s, ctx, &cust, "10.00", "USD", "list-1")
	all, err := s.ListPayments(ctx, PaymentFilter{}, 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("payments = %+v (%v), want 1", all, err)
	}
	applied, err := s.ListPayments(ctx, PaymentFilter{Status: "APPLIED"}, 10)
	if err != nil || len(applied) != 0 {
		t.Fatalf("APPLIED payments = %+v (%v), want none", applied, err)
	}
	if n := countOutbox(t, s, ctx, "dev.passage.receivables.payment.received.v1"); n != 1 {
		t.Fatalf("received events = %d, want 1", n)
	}
}

func countOutbox(t *testing.T, s *Postgres, ctx context.Context, eventType string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from outbox where type = $1`, eventType).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}
