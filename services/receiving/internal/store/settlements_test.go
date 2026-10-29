package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/migrations"
)

const (
	receiptID  = "00000000-0000-0000-0000-0000000000c1"
	invoiceID  = "00000000-0000-0000-0000-0000000000c2"
	adjustID   = "00000000-0000-0000-0000-0000000000c3"
	settleID   = "00000000-0000-0000-0000-0000000000c4"
	ledgerUUID = "00000000-0000-0000-0000-0000000000c5"
)

type settlementDB struct {
	pool *pgxpool.Pool
	one  tenant.Principal
	run  func(p tenant.Principal, sql string, args ...any) error
	rows func(p tenant.Principal, table string) int
}

func seedInvoice(t *testing.T) settlementDB {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	ctx := context.Background()
	one := principal(1)
	if err := dbx.Migrate(tenant.With(ctx, one), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	db := settlementDB{pool: pool, one: one}
	db.run = func(p tenant.Principal, sql string, args ...any) error {
		return dbx.WithTenantTx(tenant.With(ctx, p), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	db.rows = func(p tenant.Principal, table string) (n int) {
		err := dbx.WithTenantTx(tenant.With(ctx, p), pool, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from `+table).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, step := range []struct {
		sql  string
		args []any
	}{
		{`insert into receiving_receipt (tenant_id, id, legal_entity_id, vendor_id, purchase_order_id, packing_slip, state, receipt_date)
			values ($1, $2, $3, $4, $5, 'PS-1', 'POSTED', current_date)`, []any{one.TenantID, receiptID, one.LegalEntityID, vendorID, poID}},
		{`insert into receiving_receipt_line (tenant_id, receipt_id, line_no, legal_entity_id, product_id, qty, unit_price, currency)
			values ($1, $2, 1, $3, $4, 5, 2, 'USD')`, []any{one.TenantID, receiptID, one.LegalEntityID, poID}},
		{`insert into receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total)
			values ($1, $2, $3, $4, 'INV-1', current_date, current_date, 'USD', $5, $5, 'e1', 10)`, []any{one.TenantID, invoiceID, one.LegalEntityID, vendorID, ledgerUUID}},
		{`insert into receiving_vendor_invoice_line (tenant_id, invoice_id, line_no, legal_entity_id, receipt_id, receipt_line_no, qty, unit_price, amount)
			values ($1, $2, 1, $3, $4, 1, 5, 2, 10)`, []any{one.TenantID, invoiceID, one.LegalEntityID, receiptID}},
	} {
		if err := db.run(one, step.sql, step.args...); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestMigration0006_AdjustmentAndSettlementTablesIsolationConstraintsAndGrants(t *testing.T) {
	db := seedInvoice(t)
	one := db.one
	insertAdjustment := func(id, number, kind string) error {
		return db.run(one, `insert into receiving_invoice_adjustment (tenant_id, id, legal_entity_id, original_invoice_id, vendor_id, kind, document_number, document_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total)
			values ($1, $2, $3, $4, $5, $6, $7, current_date, current_date, 'USD', $8, $8, 'e2', 3)`, one.TenantID, id, one.LegalEntityID, invoiceID, vendorID, kind, number, ledgerUUID)
	}
	insertAdjustmentLine := func(amount string) error {
		return db.run(one, `insert into receiving_invoice_adjustment_line (tenant_id, adjustment_id, line_no, legal_entity_id, invoice_id, invoice_line_no, amount)
			values ($1, $2, 1, $3, $4, 1, $5::numeric)`, one.TenantID, adjustID, one.LegalEntityID, invoiceID, amount)
	}
	insertSettlement := func(id, kind, source string, amount string) error {
		return db.run(one, `insert into receiving_settlement (tenant_id, id, legal_entity_id, kind, source_ref, vendor_id, currency, amount, posting_date, ledger_id, fiscal_period_id, ledger_entry_id)
			values ($1, $2, $3, $4, $5, 'V-1', 'USD', $6::numeric, current_date, $7, $7, 'e3')`, one.TenantID, id, one.LegalEntityID, kind, source, amount, ledgerUUID)
	}

	if err := insertAdjustment(adjustID, "CN-1", "CREDIT"); err != nil {
		t.Fatal(err)
	}
	if err := insertAdjustmentLine("3"); err != nil {
		t.Fatal(err)
	}
	if err := insertSettlement(settleID, "CONSIGNMENT", "journal-1", "10"); err != nil {
		t.Fatal(err)
	}

	for name, err := range map[string]error{
		"the same document number in another case": insertAdjustment("00000000-0000-0000-0000-0000000000d1", "cn-1", "CREDIT"),
		"an unknown adjustment kind":               insertAdjustment("00000000-0000-0000-0000-0000000000d2", "CN-2", "REFUND"),
		"the same settlement source again":         insertSettlement("00000000-0000-0000-0000-0000000000d3", "CONSIGNMENT", "journal-1", "10"),
		"an unknown settlement kind":               insertSettlement("00000000-0000-0000-0000-0000000000d4", "OTHER", "x", "10"),
		"a zero settlement amount":                 insertSettlement("00000000-0000-0000-0000-0000000000d5", "CONSIGNMENT", "journal-2", "0"),
	} {
		if err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if err := insertSettlement("00000000-0000-0000-0000-0000000000d6", "PLAN_ROW", "journal-1", "10"); err != nil {
		t.Errorf("the same source under another kind must be allowed: %v", err)
	}
	if err := db.run(one, `insert into receiving_invoice_adjustment_line (tenant_id, adjustment_id, line_no, legal_entity_id, invoice_id, invoice_line_no, amount)
		values ($1, $2, 2, $3, $4, 9, 1)`, one.TenantID, adjustID, one.LegalEntityID, invoiceID); err == nil {
		t.Error("an adjustment line must reference a line of the invoice")
	}
	if err := db.run(one, `insert into receiving_invoice_adjustment_line (tenant_id, adjustment_id, line_no, legal_entity_id, invoice_id, invoice_line_no, amount)
		values ($1, $2, 3, $3, $4, 1, 0)`, one.TenantID, adjustID, one.LegalEntityID, invoiceID); err == nil {
		t.Error("an adjustment line amount must be positive")
	}

	otherEntity := tenant.Principal{TenantID: one.TenantID, LegalEntityID: testkit.TenantID(8)}
	for _, table := range []string{"receiving_invoice_adjustment", "receiving_invoice_adjustment_line", "receiving_settlement"} {
		if got := db.rows(one, table); got < 1 {
			t.Errorf("%s: owner sees %d rows", table, got)
		}
		if got := db.rows(principal(2), table); got != 0 {
			t.Errorf("%s: other tenant sees %d rows, want 0", table, got)
		}
		if got := db.rows(otherEntity, table); got != 0 {
			t.Errorf("%s: other legal entity sees %d rows, want 0", table, got)
		}
		if err := db.run(one, `delete from `+table+` where tenant_id = $1`, one.TenantID); err == nil {
			t.Errorf("%s: passage_app must not delete", table)
		}
	}
	if err := db.run(one, `update receiving_settlement set amount = 1 where tenant_id = $1`, one.TenantID); err == nil {
		t.Error("passage_app must not update settlements")
	}
	if err := db.run(principal(2), `insert into receiving_settlement (tenant_id, id, legal_entity_id, kind, source_ref, vendor_id, currency, amount, posting_date, ledger_id, fiscal_period_id, ledger_entry_id)
		values ($1, gen_random_uuid(), $2, 'CONSIGNMENT', 'x', 'V', 'USD', 1, current_date, $3, $3, 'e')`, one.TenantID, one.LegalEntityID, ledgerUUID); err == nil {
		t.Error("another tenant must not write a settlement for the first tenant")
	}
}

func event(t *testing.T, p tenant.Principal, entity, verb string) events.Event {
	t.Helper()
	evt, err := events.New(events.Type("payables", entity, verb, 1), "/passage/receiving/receiving", p.TenantID, p.TenantID, map[string]string{"k": "v"})
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestPostInvoiceAdjustment_ConcurrentCreditsCannotExceedTheInvoicedAmount(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	const racers = 4
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := []string{"00000000-0000-0000-0000-0000000000e1", "00000000-0000-0000-0000-0000000000e2", "00000000-0000-0000-0000-0000000000e3", "00000000-0000-0000-0000-0000000000e4"}[i]
			in := store.InvoiceAdjustment{
				ID: id, OriginalInvoiceID: invoiceID, LegalEntityID: db.one.LegalEntityID, Kind: domain.AdjustmentCredit, DocumentNumber: "CN-" + id[len(id)-1:],
				DocumentDate: postDay(), PostingDate: postDay(), LedgerID: ledgerUUID, FiscalPeriodID: ledgerUUID,
				Lines: []domain.AdjustmentLine{{InvoiceLineNo: 1, Amount: decimal.NewFromInt(6)}},
			}
			_, results[i] = st.PostInvoiceAdjustment(ctx, in, func(base store.InvoiceBase) (store.AdjustmentOutcome, error) {
				l := base.Lines[0]
				if l.Credited.Add(decimal.NewFromInt(6)).GreaterThan(l.Amount.Add(l.Debited)) {
					return store.AdjustmentOutcome{}, &domain.Error{Code: domain.CodeOverCredit, Detail: "over"}
				}
				return store.AdjustmentOutcome{LedgerEntryID: "e-" + id, Total: decimal.NewFromInt(6), Event: event(t, db.one, "invoice", "adjusted")}, nil
			})
		}()
	}
	wg.Wait()

	won, refused := 0, 0
	for _, err := range results {
		var de *domain.Error
		switch {
		case err == nil:
			won++
		case errors.As(err, &de) && de.Code == domain.CodeOverCredit:
			refused++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if won != 1 || refused != racers-1 || db.rows(db.one, "receiving_invoice_adjustment") != 1 {
		t.Fatalf("%d credits of 6.00 against 10.00 succeeded (%d refused, %d stored), want exactly 1", won, refused, db.rows(db.one, "receiving_invoice_adjustment"))
	}
}

func TestPostSettlement_ConcurrentRunsSettleASourceOnce(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	const racers = 4
	var wg sync.WaitGroup
	var mu sync.Mutex
	ran, replayed := 0, 0
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := store.Settlement{
				ID:   []string{"00000000-0000-0000-0000-0000000000f1", "00000000-0000-0000-0000-0000000000f2", "00000000-0000-0000-0000-0000000000f3", "00000000-0000-0000-0000-0000000000f4"}[i],
				Kind: domain.SettlementPlanRow, SourceRef: "po/1/1", LegalEntityID: db.one.LegalEntityID, VendorID: vendorID, Currency: "USD", Amount: decimal.NewFromInt(7),
				PostingDate: postDay(), LedgerID: ledgerUUID, FiscalPeriodID: ledgerUUID,
			}
			_, again, err := st.PostSettlement(ctx, in, func() (store.SettlementOutcome, error) {
				mu.Lock()
				ran++
				mu.Unlock()
				return store.SettlementOutcome{LedgerEntryID: "e", Event: event(t, db.one, "settlement", "posted")}, nil
			})
			if err != nil {
				t.Error(err)
			}
			if again {
				mu.Lock()
				replayed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if ran != 1 || replayed != racers-1 || db.rows(db.one, "receiving_settlement") != 1 {
		t.Fatalf("function ran %d times, %d replays, %d rows; want 1, %d, 1", ran, replayed, db.rows(db.one, "receiving_settlement"), racers-1)
	}
}

func postDay() time.Time { return time.Date(2026, 3, 6, 0, 0, 0, 0, time.UTC) }
