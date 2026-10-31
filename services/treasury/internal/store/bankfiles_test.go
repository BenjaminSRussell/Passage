package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

type staticSigner struct{}

func (staticSigner) Sign(context.Context, string, []byte) (bankconnect.Signature, error) {
	return bankconnect.Signature{Bytes: []byte{1, 2, 3}, KeyVersion: 2}, nil
}

type staticTransport struct{ err error }

func (staticTransport) Protocol() string { return bankconnect.ProtocolEBICS }
func (s staticTransport) Send(context.Context, bankconnect.File, bankconnect.Signature) (bankconnect.Receipt, error) {
	return bankconnect.Receipt{Reference: "TX-9"}, s.err
}

func TestBankFileAudit_EveryFileLeavesPreparedAndOutcomeRows(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	ctx := ctxFor(1, le)
	acct := activeAccount(t, s, ctx, le)
	file := bankconnect.File{LegalEntityID: le, BankAccountID: acct.ID, Name: "a.xml", OrderType: "CCT", KeyName: "bank-a", Data: []byte("x")}

	if _, err := (bankconnect.Sender{Signer: staticSigner{}, Transport: staticTransport{}, Audit: s}).Send(ctx, file); err != nil {
		t.Fatal(err)
	}
	failing := bankconnect.Sender{Signer: staticSigner{}, Transport: staticTransport{err: errors.New("bank down")}, Audit: s}
	if _, err := failing.Send(ctx, file); err == nil {
		t.Fatal("expected the transport error")
	}

	var events []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select event from bank_file_audit order by created_at, event desc`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			events = append(events, e)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, e := range events {
		counts[e]++
	}
	if counts["prepared"] != 2 || counts["sent"] != 1 || counts["failed"] != 1 {
		t.Fatalf("audit events = %v", events)
	}

	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update bank_file_audit set event = 'sent'`)
		return err
	})
	requireInsufficientPrivilege(t, err, "the audit must be append-only for the application role")
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from bank_file_audit`)
		return err
	})
	requireInsufficientPrivilege(t, err, "the audit must not be deletable by the application role")
}

func sqlState(t *testing.T, err error) string {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want a database error, got %v", err)
	}
	return pgErr.Code
}

func TestBankFileAudit_RejectsForeignAccountsAndEntities(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	acct := activeAccount(t, s, ctxFor(1, le), le)
	entry := func(legalEntity string) bankconnect.AuditEntry {
		return bankconnect.AuditEntry{FileID: ids.New(), Event: "prepared", LegalEntityID: legalEntity, BankAccountID: acct.ID,
			Protocol: "ebics", FileName: "a.xml", OrderType: "CCT", SHA256: strings.Repeat("0", 64), KeyName: "k"}
	}
	if err := s.RecordBankFile(ctxFor(1, ""), entry(le)); err != nil {
		t.Fatalf("own account: %v", err)
	}
	if code := sqlState(t, s.RecordBankFile(ctxFor(2, ""), entry(le))); code != "23503" {
		t.Fatalf("another tenant naming this account: SQLSTATE %s; want 23503", code)
	}
	if code := sqlState(t, s.RecordBankFile(ctxFor(1, ""), entry(ids.New()))); code != "23503" {
		t.Fatalf("same tenant, account of another legal entity: SQLSTATE %s; want 23503", code)
	}
	if code := sqlState(t, s.RecordBankFile(ctxFor(1, ids.New()), entry(le))); code != "42501" {
		t.Fatalf("caller scoped to a different legal entity: SQLSTATE %s; want 42501", code)
	}
}
