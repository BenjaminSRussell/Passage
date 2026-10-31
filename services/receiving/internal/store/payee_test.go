package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	payeeOne   = "00000000-0000-0000-0000-0000000000e1"
	payeeOther = "00000000-0000-0000-0000-0000000000e2"
	payeeTable = "receiving_one_time_payee"
)

func payeeKeys(t *testing.T) *fieldcrypt.Keys {
	t.Helper()
	keys, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{5}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func samplePayee(id, legalEntityID string) store.OneTimePayee {
	return store.OneTimePayee{
		ID: id, LegalEntityID: legalEntityID, Country: "DE", Name: "Erika Mustermann", Address: "Hauptstrasse 1, 10115 Berlin",
		TaxID: "DE123456789", Email: "erika@example.org", CreatedBy: "alice",
		Bank: &domain.PayeeBank{IBAN: "DE89370400440532013000", BIC: "COBADEFF"},
	}
}

func sealedColumn(t *testing.T, db settlementDB, column, id string) []byte {
	t.Helper()
	var sealed []byte
	err := dbx.WithTenantTx(tenant.With(context.Background(), db.one), db.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select `+column+` from `+payeeTable+` where id = $1`, id).Scan(&sealed)
	})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func TestOneTimePayee_FieldsAreSealedBoundToTenantAndFieldAndIsolated(t *testing.T) {
	db := seedInvoice(t)
	keys := payeeKeys(t)
	st := store.New(db.pool).WithFieldCrypt(keys)
	ctx := tenant.With(context.Background(), db.one)
	if _, err := st.CreateOneTimePayee(ctx, samplePayee(payeeOne, db.one.LegalEntityID)); err != nil {
		t.Fatal(err)
	}

	columns := map[string]struct{ field, plain string }{
		"name_sealed":    {"receiving_one_time_payee.name", "Erika Mustermann"},
		"address_sealed": {"receiving_one_time_payee.address", "Hauptstrasse 1, 10115 Berlin"},
		"tax_id_sealed":  {"receiving_one_time_payee.tax_id", "DE123456789"},
		"email_sealed":   {"receiving_one_time_payee.email", "erika@example.org"},
	}
	for column, want := range columns {
		sealed := sealedColumn(t, db, column, payeeOne)
		if bytes.Contains(sealed, []byte(want.plain)) {
			t.Errorf("%s holds plaintext", column)
		}
		got, err := keys.Open(db.one.TenantID, want.field, sealed)
		if err != nil || string(got) != want.plain {
			t.Errorf("%s opens to %q, %v", column, got, err)
		}
		if _, err := keys.Open(testkit.TenantID(2), want.field, sealed); !errors.Is(err, fieldcrypt.ErrOpen) {
			t.Errorf("%s opened for another tenant", column)
		}
		if _, err := keys.Open(db.one.TenantID, "receiving_one_time_payee.other", sealed); !errors.Is(err, fieldcrypt.ErrOpen) {
			t.Errorf("%s opened under another field", column)
		}
	}
	bankSealed := sealedColumn(t, db, "bank_sealed", payeeOne)
	if bytes.Contains(bankSealed, []byte("DE89370400440532013000")) {
		t.Error("bank_sealed holds the IBAN in the clear")
	}
	bankPlain, err := keys.Open(db.one.TenantID, "receiving_one_time_payee.bank", bankSealed)
	var bank domain.PayeeBank
	if err != nil || json.Unmarshal(bankPlain, &bank) != nil || bank.IBAN != "DE89370400440532013000" || bank.BIC != "COBADEFF" {
		t.Fatalf("bank opens to %s, %v", bankPlain, err)
	}

	other := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if got := db.rows(db.one, payeeTable); got != 1 {
		t.Errorf("owner sees %d payees", got)
	}
	if got := db.rows(principal(2), payeeTable); got != 0 {
		t.Errorf("another tenant sees %d payees", got)
	}
	if got := db.rows(other, payeeTable); got != 0 {
		t.Errorf("another legal entity sees %d payees", got)
	}
	for name, sql := range map[string]string{
		"an update": `update ` + payeeTable + ` set country = 'FR'`,
		"a delete":  `delete from ` + payeeTable,
	} {
		if err := db.run(db.one, sql); err == nil {
			t.Errorf("passage_app must not run %s: the payee is immutable", name)
		}
	}
	var forced bool
	if err := db.pool.QueryRow(context.Background(), `select relforcerowsecurity from pg_class where relname = $1 and relnamespace = current_schema()::regnamespace`, payeeTable).Scan(&forced); err != nil || !forced {
		t.Errorf("rls forced=%v err=%v", forced, err)
	}
}

func TestOneTimePayee_RefusedWithoutKeys(t *testing.T) {
	db := seedInvoice(t)
	_, err := store.New(db.pool).CreateOneTimePayee(tenant.With(context.Background(), db.one), samplePayee(payeeOne, db.one.LegalEntityID))
	if !errors.Is(err, store.ErrNoFieldCrypt) {
		t.Fatalf("err = %v, want ErrNoFieldCrypt", err)
	}
	if got := db.rows(db.one, payeeTable); got != 0 {
		t.Fatalf("%d payees stored without keys", got)
	}
}

func TestPostInvoice_ReferencesAPayeeOfTheSameTenantAndEntity(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool).WithFieldCrypt(payeeKeys(t))
	ctx := tenant.With(context.Background(), db.one)
	if _, err := st.CreateOneTimePayee(ctx, samplePayee(payeeOne, db.one.LegalEntityID)); err != nil {
		t.Fatal(err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if _, err := st.CreateOneTimePayee(tenant.With(context.Background(), otherEntity), samplePayee(payeeOther, otherEntity.LegalEntityID)); err != nil {
		t.Fatal(err)
	}

	missing := fullInvoice(db, "00000000-0000-0000-0000-00000000c901", "OTP-MISSING", domain.InvoiceSourceManual)
	missing.OneTimePayeeID = "00000000-0000-0000-0000-0000000000ff"
	if _, err := st.PostInvoice(ctx, missing, holdFunc(t, db, missing, false)); !errors.Is(err, store.ErrPayeeNotFound) {
		t.Fatalf("unknown payee: %v", err)
	}
	hidden := fullInvoice(db, "00000000-0000-0000-0000-00000000c902", "OTP-HIDDEN", domain.InvoiceSourceManual)
	hidden.OneTimePayeeID = payeeOther
	if _, err := st.PostInvoice(ctx, hidden, holdFunc(t, db, hidden, false)); !errors.Is(err, store.ErrPayeeNotFound) {
		t.Fatalf("payee of another legal entity: %v", err)
	}

	in := fullInvoice(db, "00000000-0000-0000-0000-00000000c903", "OTP-1", domain.InvoiceSourceManual)
	in.OneTimePayeeID = payeeOne
	if _, err := st.PostInvoice(ctx, in, holdFunc(t, db, in, false)); err != nil {
		t.Fatal(err)
	}
	if got := scalar(t, db, `select one_time_payee_id::text from receiving_vendor_invoice where invoice_number = 'OTP-1'`); got != payeeOne {
		t.Fatalf("stored payee %q", got)
	}
	if got := scalar(t, db, `select count(*)::text from receiving_vendor_invoice where invoice_number in ('OTP-MISSING', 'OTP-HIDDEN')`); got != "0" {
		t.Fatalf("%s refused invoices were stored", got)
	}
}
