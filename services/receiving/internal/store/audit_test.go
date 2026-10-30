package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

var auditProfile = domain.PostingProfile{LedgerID: ledgerUUID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000"}

func seedOpenGrir(t *testing.T, db settlementDB) {
	t.Helper()
	if err := db.run(db.one, `insert into receiving_grir_open_item (tenant_id, id, legal_entity_id, receipt_id, line_no, received_qty, received_amount, currency, vendor_id, po_line_id)
		values ($1, gen_random_uuid(), $2, $3, 1, 5, 10, 'USD', $4, $5)`, db.one.TenantID, db.one.LegalEntityID, receiptID, vendorID, poID); err != nil {
		t.Fatal(err)
	}
}

func fullInvoice(db settlementDB, id, number, source string) store.Invoice {
	return store.Invoice{
		ID: id, LegalEntityID: db.one.LegalEntityID, VendorID: vendorID, InvoiceNumber: number, InvoiceDate: postDay(), PostingDate: postDay(), Currency: "USD",
		LedgerID: ledgerUUID, FiscalPeriodID: ledgerUUID, Source: source, PostedBy: "alice",
		Lines: []store.InvoiceLine{{ReceiptID: receiptID, ReceiptLineNo: 1, Qty: decimal.NewFromInt(5), UnitPrice: decimal.NewFromInt(2), Amount: decimal.NewFromInt(10)}},
	}
}

func holdFunc(t *testing.T, db settlementDB, in store.Invoice, blocked bool) store.InvoiceFunc {
	return func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		doc := domain.VendorInvoice{
			ID: in.ID, LegalEntityID: in.LegalEntityID, LedgerID: in.LedgerID, FiscalPeriodID: in.FiscalPeriodID, PostingDate: in.PostingDate, InvoiceDate: in.InvoiceDate,
			Currency: in.Currency, Lines: []domain.PostLine{{Item: items[0], Qty: in.Lines[0].Qty, UnitPrice: in.Lines[0].UnitPrice}},
		}
		entry, err := domain.HoldInvoice(context.Background(), doc, domain.FakePeriods{Status: domain.PeriodOpen}, auditProfile)
		if err != nil {
			return store.InvoiceOutcome{}, err
		}
		out := store.InvoiceOutcome{Total: decimal.NewFromInt(10), Blocked: blocked, Clearings: entry.GrirCleared, Event: event(t, db.one, "vendorinvoice", "posted")}
		if !blocked {
			out.LedgerEntryID = "e-" + in.ID
		}
		return out, nil
	}
}

func TestMigration0008_AuditColumnsPolicyTablesIsolationAndGrants(t *testing.T) {
	db := seedInvoice(t)
	one := db.one
	other := tenant.Principal{TenantID: one.TenantID, LegalEntityID: testkit.TenantID(8)}

	for name, sql := range map[string]string{
		"an unknown invoice status": `update receiving_vendor_invoice set status = 'HELD'`,
		"an unknown invoice source": `update receiving_vendor_invoice set source = 'EDI'`,
		"a change of the total":     `update receiving_vendor_invoice set total = 1`,
		"a change of the number":    `update receiving_vendor_invoice set invoice_number = 'X'`,
		"a deleted invoice":         `delete from receiving_vendor_invoice`,
	} {
		if err := db.run(one, sql); err == nil {
			t.Errorf("passage_app must not run %s", name)
		}
	}
	if err := db.run(one, `update receiving_vendor_invoice set status = 'AUDIT_BLOCKED'`); err == nil {
		t.Error("a POSTED invoice must not go back to AUDIT_BLOCKED")
	}
	if err := db.run(one, `update receiving_vendor_invoice set status = 'AUDIT_REJECTED'`); err == nil {
		t.Error("a POSTED invoice must not become AUDIT_REJECTED")
	}
	if err := db.run(one, `insert into receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total, status)
		values ($1, '00000000-0000-0000-0000-00000000c9a1', $2, $3, 'HELD-1', current_date, current_date, 'USD', $4, $4, '', 10, 'AUDIT_BLOCKED')`, one.TenantID, one.LegalEntityID, vendorID, ledgerUUID); err != nil {
		t.Fatal(err)
	}
	if err := db.run(one, `update receiving_vendor_invoice set status = 'AUDIT_REJECTED', rejected_by = 'bob', rejected_at = now(), reject_reason = 'dup' where invoice_number = 'HELD-1'`); err != nil {
		t.Errorf("AUDIT_BLOCKED to AUDIT_REJECTED must be allowed: %v", err)
	}
	for _, target := range []string{"AUDIT_BLOCKED", "POSTED"} {
		if err := db.run(one, `update receiving_vendor_invoice set status = '`+target+`' where invoice_number = 'HELD-1'`); err == nil {
			t.Errorf("a rejected invoice must not become %s", target)
		}
	}
	if err := db.run(one, `insert into receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total, status)
		values ($1, '00000000-0000-0000-0000-00000000c9a2', $2, $3, 'HELD-2', current_date, current_date, 'USD', $4, $4, '', 10, 'AUDIT_BLOCKED')`, one.TenantID, one.LegalEntityID, vendorID, ledgerUUID); err != nil {
		t.Fatal(err)
	}
	if err := db.run(one, `update receiving_vendor_invoice set status = 'POSTED', released_by = 'bob', released_at = now(), ledger_entry_id = 'e9' where invoice_number = 'HELD-2'`); err != nil {
		t.Errorf("AUDIT_BLOCKED to POSTED must be allowed: %v", err)
	}
	if got := scalar(t, db, `select status || '/' || released_by || '/' || ledger_entry_id from receiving_vendor_invoice where invoice_number = 'HELD-2'`); got != "POSTED/bob/e9" {
		t.Errorf("released invoice columns: %q", got)
	}
	for _, table := range []string{"receiving_ers_vendor_history", "receiving_audit_policy_history"} {
		cols := "(tenant_id, legal_entity_id, vendor_id, old_enabled, new_enabled, changed_by) values ($1, $2, $3, false, true, 'a')"
		if table == "receiving_audit_policy_history" {
			cols = "(tenant_id, legal_entity_id, old_rate_pct, new_rate_pct, changed_by) values ($1, $2, 0, 5, 'a')"
		}
		args := []any{one.TenantID, one.LegalEntityID}
		if table == "receiving_ers_vendor_history" {
			args = append(args, vendorID)
		}
		if err := db.run(one, `insert into `+table+` `+cols, args...); err != nil {
			t.Fatalf("%s insert: %v", table, err)
		}
		if err := db.run(one, `update `+table+` set changed_by = 'x'`); err == nil {
			t.Errorf("%s must be append-only: update allowed", table)
		}
		if err := db.run(one, `delete from `+table); err == nil {
			t.Errorf("%s must be append-only: delete allowed", table)
		}
		if got := db.rows(one, table); got != 1 {
			t.Errorf("%s: owner sees %d rows", table, got)
		}
		if got := db.rows(principal(2), table); got != 0 {
			t.Errorf("%s: other tenant sees %d rows", table, got)
		}
		if got := db.rows(other, table); got != 0 {
			t.Errorf("%s: other legal entity sees %d rows", table, got)
		}
	}

	if err := db.run(one, `insert into receiving_ers_vendor (tenant_id, legal_entity_id, vendor_id, enabled, updated_by) values ($1, $2, $3, true, 'alice')`, one.TenantID, one.LegalEntityID, vendorID); err != nil {
		t.Fatal(err)
	}
	if err := db.run(one, `insert into receiving_audit_policy (tenant_id, legal_entity_id, sample_rate_pct, updated_by) values ($1, $2, 12.5, 'alice')`, one.TenantID, one.LegalEntityID); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"a rate above 100":  db.run(one, `insert into receiving_audit_policy (tenant_id, legal_entity_id, sample_rate_pct, updated_by) values ($1, $2, 100.01, 'a')`, one.TenantID, testkit.TenantID(7)),
		"a negative rate":   db.run(one, `insert into receiving_audit_policy (tenant_id, legal_entity_id, sample_rate_pct, updated_by) values ($1, $2, -1, 'a')`, one.TenantID, testkit.TenantID(7)),
		"a second policy":   db.run(one, `insert into receiving_audit_policy (tenant_id, legal_entity_id, sample_rate_pct, updated_by) values ($1, $2, 1, 'a')`, one.TenantID, one.LegalEntityID),
		"a second ERS flag": db.run(one, `insert into receiving_ers_vendor (tenant_id, legal_entity_id, vendor_id, enabled, updated_by) values ($1, $2, $3, false, 'a')`, one.TenantID, one.LegalEntityID, vendorID),
	} {
		if err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	for _, table := range []string{"receiving_ers_vendor", "receiving_audit_policy"} {
		if got := db.rows(one, table); got != 1 {
			t.Errorf("%s: owner sees %d rows, want 1", table, got)
		}
		if got := db.rows(principal(2), table); got != 0 {
			t.Errorf("%s: other tenant sees %d rows", table, got)
		}
		if got := db.rows(other, table); got != 0 {
			t.Errorf("%s: other legal entity sees %d rows", table, got)
		}
		if err := db.run(one, `delete from `+table); err == nil {
			t.Errorf("%s: passage_app must not delete", table)
		}
	}
	if err := db.run(principal(2), `insert into receiving_audit_policy (tenant_id, legal_entity_id, sample_rate_pct, updated_by) values ($1, $2, 1, 'a')`, one.TenantID, one.LegalEntityID); err == nil {
		t.Error("another tenant must not write a policy for the first tenant")
	}
}

func TestInvoiceTakersOfOneGrirLineExcludeEachOther(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	const racers = 4
	var wg sync.WaitGroup
	manual := make([]error, racers)
	selfBilled := make([]error, racers)
	settled := make([]error, racers)
	for i := range racers {
		wg.Add(3)
		go func() {
			defer wg.Done()
			in := fullInvoice(db, fmt.Sprintf("00000000-0000-0000-0000-00000000c0%02d", i), fmt.Sprintf("MAN-%d", i), domain.InvoiceSourceManual)
			_, manual[i] = st.PostInvoice(ctx, in, holdFunc(t, db, in, false))
		}()
		go func() {
			defer wg.Done()
			in := fullInvoice(db, fmt.Sprintf("00000000-0000-0000-0000-00000000c1%02d", i), fmt.Sprintf("ERS-RCV-%d", i), domain.InvoiceSourceERS)
			_, selfBilled[i] = st.PostInvoice(ctx, in, holdFunc(t, db, in, i%2 == 0))
		}()
		go func() {
			defer wg.Done()
			in := store.Settlement{
				ID: fmt.Sprintf("00000000-0000-0000-0000-00000000c2%02d", i), Kind: domain.SettlementPlanRow, SourceRef: fmt.Sprintf("po/1/%d", i), LegalEntityID: db.one.LegalEntityID,
				VendorID: vendorID, Currency: "USD", Amount: decimal.NewFromInt(7), PostingDate: postDay(), LedgerID: ledgerUUID, FiscalPeriodID: ledgerUUID,
				PurchaseOrderID: poID, POLineNo: 1, ProductID: poID,
			}
			_, _, settled[i] = st.PostSettlement(ctx, in, func() (store.SettlementOutcome, error) {
				return store.SettlementOutcome{LedgerEntryID: "e", Event: event(t, db.one, "settlement", "posted")}, nil
			})
		}()
	}
	wg.Wait()

	invoices, settlements := 0, 0
	for i := range racers {
		for _, err := range []error{manual[i], selfBilled[i]} {
			var de *domain.Error
			switch {
			case err == nil:
				invoices++
			case errors.As(err, &de) && (de.Code == domain.CodeOverInvoice || de.Code == domain.CodePlanSettled):
			default:
				t.Fatalf("invoice %d: %v", i, err)
			}
		}
		switch {
		case settled[i] == nil:
			settlements++
		case errors.Is(settled[i], store.ErrLineInvoiced):
		default:
			t.Fatalf("settlement %d: %v", i, settled[i])
		}
	}
	if invoices > 1 || (invoices == 1) == (settlements > 0) {
		t.Fatalf("%d invoices (manual and ERS) and %d plan settlements committed for one line of 5 units, want exactly one invoice or only settlements", invoices, settlements)
	}
	if got := db.rows(db.one, "receiving_vendor_invoice") - 1; got != invoices {
		t.Fatalf("%d invoice rows for %d reported invoices", got, invoices)
	}
}

func TestSelfBilledThenManualAndManualThenSelfBilledAreRefused(t *testing.T) {
	for _, first := range []string{domain.InvoiceSourceERS, domain.InvoiceSourceManual} {
		second := domain.InvoiceSourceManual
		if first == domain.InvoiceSourceManual {
			second = domain.InvoiceSourceERS
		}
		t.Run(first+" first", func(t *testing.T) {
			db := seedInvoice(t)
			seedOpenGrir(t, db)
			st := store.New(db.pool)
			ctx := tenant.With(context.Background(), db.one)
			one := fullInvoice(db, "00000000-0000-0000-0000-00000000c300", "FIRST-1", first)
			if _, err := st.PostInvoice(ctx, one, holdFunc(t, db, one, false)); err != nil {
				t.Fatal(err)
			}
			two := fullInvoice(db, "00000000-0000-0000-0000-00000000c301", "SECOND-1", second)
			_, err := st.PostInvoice(ctx, two, holdFunc(t, db, two, false))
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != domain.CodeOverInvoice {
				t.Fatalf("second err %v, want %s", err, domain.CodeOverInvoice)
			}
		})
	}
}

func TestReleaseInvoice_ConcurrentReleasesPostOnce(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	held := fullInvoice(db, "00000000-0000-0000-0000-00000000c400", "ERS-RCV-9", domain.InvoiceSourceERS)
	stored, err := st.PostInvoice(ctx, held, holdFunc(t, db, held, true))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.InvoiceStatusAuditBlocked || stored.LedgerEntryID != "" {
		t.Fatalf("stored %+v", stored)
	}
	const racers = 4
	var wg sync.WaitGroup
	var mu sync.Mutex
	ran := 0
	results := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = st.ReleaseInvoice(ctx, held.ID, db.one.LegalEntityID, "bob", func(inv store.Invoice) (store.InvoiceOutcome, error) {
				mu.Lock()
				ran++
				mu.Unlock()
				return store.InvoiceOutcome{LedgerEntryID: "released", Total: inv.Total, Event: event(t, db.one, "vendorinvoice", "posted")}, nil
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
		case errors.As(err, &de) && de.Code == domain.CodeNotAuditBlocked:
			refused++
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if won != 1 || refused != racers-1 || ran != 1 {
		t.Fatalf("%d releases won, %d refused, function ran %d times; want 1, %d, 1", won, refused, ran, racers-1)
	}
	if got := scalar(t, db, `select status || '/' || ledger_entry_id || '/' || released_by from receiving_vendor_invoice where invoice_number = 'ERS-RCV-9'`); got != "POSTED/released/bob" {
		t.Fatalf("stored invoice %q", got)
	}
}

func scalar(t *testing.T, db settlementDB, sql string) (out string) {
	t.Helper()
	err := dbx.WithTenantTx(tenant.With(context.Background(), db.one), db.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql).Scan(&out)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRejectAndReleaseRacingForOneBlockedInvoiceDecideOnce(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	held := fullInvoice(db, "00000000-0000-0000-0000-00000000c500", "ERS-RCV-7", domain.InvoiceSourceERS)
	if _, err := st.PostInvoice(ctx, held, holdFunc(t, db, held, true)); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, db, `select invoiced_qty::text || '/' || status || '/' || cleared_amount::text from receiving_grir_open_item`); got != "5.000000/CLEARED/10.000000" {
		t.Fatalf("reserved GR/IR %q", got)
	}
	const racers = 4
	var wg sync.WaitGroup
	results := make([]error, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, results[i] = st.ReleaseInvoice(ctx, held.ID, db.one.LegalEntityID, "bob", func(inv store.Invoice) (store.InvoiceOutcome, error) {
					return store.InvoiceOutcome{LedgerEntryID: "released", Total: inv.Total, Event: event(t, db.one, "vendorinvoice", "posted")}, nil
				})
				return
			}
			_, results[i] = st.RejectInvoice(ctx, held.ID, db.one.LegalEntityID, "carol", "duplicate", func(store.Invoice) (events.Event, error) {
				return event(t, db.one, "invoice", "rejected"), nil
			})
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range results {
		var de *domain.Error
		switch {
		case err == nil:
			won++
		case errors.As(err, &de) && de.Code == domain.CodeNotAuditBlocked:
		default:
			t.Fatalf("unexpected error %v", err)
		}
	}
	status := scalar(t, db, `select status from receiving_vendor_invoice where invoice_number = 'ERS-RCV-7'`)
	grir := scalar(t, db, `select invoiced_qty::text || '/' || status from receiving_grir_open_item`)
	line := scalar(t, db, `select invoiced_qty::text from receiving_receipt_line`)
	switch status {
	case "POSTED":
		if grir != "5.000000/CLEARED" {
			t.Fatalf("released invoice left GR/IR %q", grir)
		}
	case "AUDIT_REJECTED":
		if grir != "0.000000/OPEN" || line != "0.000000" {
			t.Fatalf("rejected invoice left GR/IR %q and receipt line %q, want the quantity freed", grir, line)
		}
		if got := scalar(t, db, `select rejected_by || '/' || reject_reason from receiving_vendor_invoice where invoice_number = 'ERS-RCV-7'`); got != "carol/duplicate" {
			t.Fatalf("audit columns %q", got)
		}
	default:
		t.Fatalf("status %s", status)
	}
	if won != 1 {
		t.Fatalf("%d decisions won, want 1", won)
	}
}

func TestPolicyChangesWriteAppendOnlyHistoryWithTheOldValue(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	var olds []string
	for _, rate := range []string{"5", "7.5"} {
		err := st.SetAuditRate(ctx, db.one.LegalEntityID, decimal.RequireFromString(rate), "alice", func(old decimal.Decimal) (events.Event, error) {
			olds = append(olds, old.String())
			return event(t, db.one, "invoice", "sampled"), nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, enabled := range []bool{true, false} {
		if err := st.SetErsVendor(ctx, db.one.LegalEntityID, vendorID, enabled, "alice", func(bool) (events.Event, error) {
			return event(t, db.one, "invoice", "sampled"), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	boom := errors.New("event failed")
	if err := st.SetAuditRate(ctx, db.one.LegalEntityID, decimal.NewFromInt(50), "mallory", func(decimal.Decimal) (events.Event, error) { return events.Event{}, boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}

	if got := scalar(t, db, `select string_agg(old_rate_pct::text || '>' || new_rate_pct::text || ':' || changed_by, ',' order by changed_at) from receiving_audit_policy_history`); got != "0.00>5.00:alice,5.00>7.50:alice" {
		t.Errorf("audit history %q", got)
	}
	if got := scalar(t, db, `select string_agg(old_enabled::text || '>' || new_enabled::text, ',' order by changed_at) from receiving_ers_vendor_history`); got != "false>true,true>false" {
		t.Errorf("ERS history %q", got)
	}
	if rate, _ := st.AuditRate(ctx, db.one.LegalEntityID); !rate.Equal(decimal.RequireFromString("7.5")) {
		t.Errorf("a failed change was applied: %s", rate)
	}
	if olds[0] != "0" || olds[1] != "5" {
		t.Errorf("old values seen by the event builder %v", olds)
	}
}
