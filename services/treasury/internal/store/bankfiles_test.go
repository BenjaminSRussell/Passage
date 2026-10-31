package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

type staticSigner struct{}

func (staticSigner) Sign(context.Context, string, []byte) (bankconnect.Signature, error) {
	return bankconnect.Signature{Value: "vault:v2:abc", KeyVersion: 2}, nil
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

func TestBankFileAudit_TenantIsolatedAndRequiresOwnAccount(t *testing.T) {
	s, _ := newStore(t)
	le := ids.New()
	acct := activeAccount(t, s, ctxFor(1, le), le)
	other := ctxFor(2, ids.New())
	err := s.RecordBankFile(other, bankconnect.AuditEntry{FileID: ids.New(), Event: "prepared", LegalEntityID: le, BankAccountID: acct.ID,
		Protocol: "ebics", FileName: "a.xml", OrderType: "CCT", SHA256: "0000000000000000000000000000000000000000000000000000000000000000", KeyName: "k"})
	if err == nil {
		t.Fatal("another tenant must not be able to audit a file against this tenant's account")
	}
}
