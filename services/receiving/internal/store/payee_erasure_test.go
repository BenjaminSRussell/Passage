package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

var erasureNow = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func payeeID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", 900+n) }

func seedPayee(t *testing.T, db settlementDB, p tenant.Principal, n int, subject string, createdAt time.Time) string {
	t.Helper()
	id := payeeID(n)
	var subjectArg any
	if subject != "" {
		subjectArg = subject
	}
	if err := db.run(p, `insert into receiving_one_time_payee (tenant_id, id, legal_entity_id, country, name_sealed, address_sealed, tax_id_sealed, email_sealed, bank_sealed, created_by, created_at, subject_ref)
		values ($1, $2, $3, 'DE', '\x0102', '\x0304', '\x05', '\x06', '\x07', 'alice', $4, $5)`, p.TenantID, id, p.LegalEntityID, createdAt, subjectArg); err != nil {
		t.Fatal(err)
	}
	return id
}

type payeeRow struct {
	nameLen, addrLen  int
	taxNull, bankNull bool
	erasedAt          *time.Time
	erasureID         *string
	subject           *string
}

func readPayee(t *testing.T, db settlementDB, p tenant.Principal, id string) payeeRow {
	t.Helper()
	var r payeeRow
	err := dbx.WithTenantTx(tenant.With(context.Background(), p), db.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select length(name_sealed), length(address_sealed), tax_id_sealed is null, bank_sealed is null, erased_at, erasure_id::text, subject_ref
			from receiving_one_time_payee where id = $1`, id).Scan(&r.nameLen, &r.addrLen, &r.taxNull, &r.bankNull, &r.erasedAt, &r.erasureID, &r.subject)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestErasePayees_SubjectRunKeepsRetainedAndHeldAndErasesTheRest(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	old, recent := erasureNow.AddDate(-8, 0, 0), erasureNow.AddDate(-1, 0, 0)
	expired := seedPayee(t, db, db.one, 1, "party:42", old)
	retained := seedPayee(t, db, db.one, 2, "party:42", recent)
	held := seedPayee(t, db, db.one, 3, "party:42", old)
	otherSubject := seedPayee(t, db, db.one, 4, "party:43", old)
	holds := &domain.FakeLegalHolds{ByRecord: map[string][]string{held: {"hold-1"}}}
	erasureID := "00000000-0000-0000-0000-0000000000f1"

	res, err := store.ErasePayees(ctx, st, holds, store.ErasePayeesInput{LegalEntityID: db.one.LegalEntityID, SubjectRef: "party:42", ErasureID: erasureID, Now: erasureNow})
	if err != nil || res != (store.ErasePayeesResult{Erased: 1, Retained: 1, Held: 1}) {
		t.Fatalf("result %+v %v", res, err)
	}
	if r := readPayee(t, db, db.one, expired); r.erasedAt == nil || r.nameLen != 0 || r.addrLen != 0 || !r.taxNull || !r.bankNull || r.erasureID == nil || *r.erasureID != erasureID || *r.subject != "party:42" {
		t.Fatalf("expired payee not pseudonymized: %+v", r)
	}
	for name, id := range map[string]string{"retained": retained, "held": held, "other subject": otherSubject} {
		if r := readPayee(t, db, db.one, id); r.erasedAt != nil || r.nameLen != 2 {
			t.Errorf("%s payee was changed: %+v", name, r)
		}
	}
	for _, q := range holds.Queries {
		if q.DataClass != domain.PayeeDataClass || q.SubjectRef != "party:42" || q.LegalEntityID != db.one.LegalEntityID || q.RecordRef == retained {
			t.Errorf("hold query %+v", q)
		}
	}
	again, err := store.ErasePayees(ctx, st, holds, store.ErasePayeesInput{LegalEntityID: db.one.LegalEntityID, SubjectRef: "party:42", Now: erasureNow})
	if err != nil || again.Erased != 0 || again.Retained != 1 || again.Held != 1 {
		t.Fatalf("a repeated run must not erase again: %+v %v", again, err)
	}
}

func TestErasePayees_RetentionSweepAndLastActivityOfInvoices(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	old := erasureNow.AddDate(-8, 0, 0)
	idle := seedPayee(t, db, db.one, 1, "", old)
	active := seedPayee(t, db, db.one, 2, "", old)
	if err := db.run(db.one, `insert into receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total, one_time_payee_id, created_at)
		values ($1, '00000000-0000-0000-0000-0000000000c9', $2, $3, 'INV-OTP', current_date, current_date, 'USD', $4, $4, 'e9', 10, $5, $6)`,
		db.one.TenantID, db.one.LegalEntityID, vendorID, ledgerUUID, active, erasureNow.AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	res, err := store.ErasePayees(ctx, st, &domain.FakeLegalHolds{}, store.ErasePayeesInput{LegalEntityID: db.one.LegalEntityID, Now: erasureNow})
	if err != nil || res != (store.ErasePayeesResult{Erased: 1, Retained: 1}) {
		t.Fatalf("sweep %+v %v", res, err)
	}
	if readPayee(t, db, db.one, idle).erasedAt == nil || readPayee(t, db, db.one, active).erasedAt != nil {
		t.Fatal("a payee with a recent invoice is still inside retention; the idle one is not")
	}
}

func TestErasePayees_HoldCheckFailureChangesNothingAndOtherEntitiesAreUntouched(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	old := erasureNow.AddDate(-8, 0, 0)
	id := seedPayee(t, db, db.one, 1, "party:42", old)
	boom := errors.New("compliance unreachable")
	if _, err := store.ErasePayees(ctx, st, &domain.FakeLegalHolds{Err: boom}, store.ErasePayeesInput{LegalEntityID: db.one.LegalEntityID, SubjectRef: "party:42", Now: erasureNow}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if readPayee(t, db, db.one, id).erasedAt != nil {
		t.Fatal("an unanswered hold check must fail closed")
	}

	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: "00000000-0000-0000-0000-000000000008"}
	foreign := seedPayee(t, db, otherEntity, 2, "party:42", old)
	res, err := store.ErasePayees(ctx, st, &domain.FakeLegalHolds{}, store.ErasePayeesInput{LegalEntityID: otherEntity.LegalEntityID, SubjectRef: "party:42", Now: erasureNow})
	if err != nil || res.Erased != 0 {
		t.Fatalf("a caller must not erase another legal entity's payees: %+v %v", res, err)
	}
	if readPayee(t, db, otherEntity, foreign).erasedAt != nil {
		t.Fatal("foreign payee erased")
	}
	if err := db.run(db.one, `update receiving_one_time_payee set country = 'FR'`); err == nil {
		t.Fatal("erasure must not make the other payee columns writable")
	}
}
