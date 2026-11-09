package ebics

import (
	"context"
	"fmt"

	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
)

// AccountLookup finds the EBICS subscriber bound to a bank account.
type AccountLookup interface {
	SubscriberForAccount(ctx context.Context, bankAccountID string) (Subscriber, error)
}

// selfSigned stands in for the Sender's detached signature: an EBICS upload is
// signed by the subscriber's A006 key inside the order, so there is nothing for
// a second signature to cover.
type selfSigned struct{}

func (selfSigned) Sign(context.Context, string, []byte) (bankconnect.Signature, error) {
	return bankconnect.Signature{}, nil
}

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
	f.KeyName = sub.A006Key
	return bankconnect.Sender{Signer: selfSigned{}, Transport: Transport{Client: s.Client, SubscriberID: sub.ID}, Audit: s.Audit}.Send(ctx, f)
}
