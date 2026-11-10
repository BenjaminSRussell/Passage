package ebics

import (
	"context"
	"errors"
	"fmt"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

// AccountLookup finds the EBICS subscriber bound to a bank account.
type AccountLookup interface {
	SubscriberForAccount(ctx context.Context, bankAccountID string) (Subscriber, error)
}

// selfSigned stands in for the Sender's detached signature: an EBICS upload is
// signed by the subscriber's A006 key inside the order, so there is nothing for
// a second signature to cover. It reports the pinned A006 key version so the
// audit row names the key the order is signed with.
type selfSigned struct{ keyVersion int }

func (s selfSigned) Sign(context.Context, string, []byte) (bankconnect.Signature, error) {
	return bankconnect.Signature{KeyVersion: s.keyVersion}, nil
}

// ErrLegalEntityMismatch is returned when a file names a legal entity other than
// the one that owns the subscriber bound to its bank account.
var ErrLegalEntityMismatch = errors.New("ebics: the file's legal entity is not the legal entity of the subscriber bound to its bank account")

// AccountSender sends bank files for an account through the EBICS subscriber
// bound to it, with the same bank_file_audit rows every channel writes. This is
// what the "ebics" entry of TREASURY_BANK_CHANNELS enables.
type AccountSender struct {
	Client   *Client
	Accounts AccountLookup
	Audit    bankconnect.AuditSink
}

// Send audits the file as prepared, uploads it as the order f.OrderType (CCT,
// CDD, CDB or CIP) and audits the outcome. f.KeyName is set to the subscriber's
// A006 key name, the key the order is signed with.
func (s AccountSender) Send(ctx context.Context, f bankconnect.File) (bankconnect.Receipt, error) {
	sub, err := s.Accounts.SubscriberForAccount(ctx, f.BankAccountID)
	if err != nil {
		return bankconnect.Receipt{}, fmt.Errorf("ebics subscriber for bank account: %w", err)
	}
	if f.LegalEntityID != sub.LegalEntityID {
		return bankconnect.Receipt{}, ErrLegalEntityMismatch
	}
	f.KeyName = sub.A006Key
	return bankconnect.Sender{Signer: selfSigned{keyVersion: sub.A006Version}, Transport: Transport{Client: s.Client, SubscriberID: sub.ID}, Audit: s.Audit}.Send(ctx, f)
}
