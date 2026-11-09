package ebics

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"fmt"
	"regexp"
	"time"
)

type State string

const (
	StateNew         State = "NEW"
	StateINISent     State = "INI_SENT"
	StateHIASent     State = "HIA_SENT"
	StateKeysPending State = "BANK_KEYS_PENDING"
	StateReady       State = "READY"
)

type KeyUsage = string

const (
	UsageA006 KeyUsage = "A006"
	UsageX002 KeyUsage = "X002"
	UsageE002 KeyUsage = "E002"
)

// Keys is the subscriber's private-key custody. No method returns private key
// material: A006 and X002 are signed and E002 transaction keys are unwrapped
// inside the key store.
type Keys interface {
	Public(ctx context.Context, usage KeyUsage) (*rsa.PublicKey, error)
	SignA006(ctx context.Context, orderHash []byte) ([]byte, error)
	SignX002(ctx context.Context, signedInfo []byte) ([]byte, error)
	DecryptE002(ctx context.Context, wrappedKey []byte) ([]byte, error)
}

var (
	ErrVersionConflict = errors.New("ebics subscriber was changed by someone else")
	ErrBadState        = errors.New("ebics subscriber is not in a state that allows this step")
	ErrKeyHashMismatch = errors.New("bank key hash does not match the hash the bank confirmed")
)

// Subscriber is one EBICS user at one bank, bound to one bank account. Only key
// names and the bank's public keys are stored; private keys live in Vault.
type Subscriber struct {
	ID            string
	LegalEntityID string
	BankAccountID string
	HostID        string
	PartnerID     string
	UserID        string
	URL           string
	A006Key       string
	X002Key       string
	E002KVPath    string
	State         State
	BankX002      []byte
	BankE002      []byte
	KeysConfirmed bool
	ConfirmedBy   string
	ConfirmedAt   *time.Time
	ParkedCode    string
	ParkedText    string
	Version       int
}

type SubscriberStore interface {
	GetSubscriber(ctx context.Context, id string) (Subscriber, error)
	UpdateSubscriber(ctx context.Context, s Subscriber) (Subscriber, error)
}

var ebicsID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,35}$`)

func (s Subscriber) Validate() error {
	for name, v := range map[string]string{"HostID": s.HostID, "PartnerID": s.PartnerID, "UserID": s.UserID} {
		if !ebicsID.MatchString(v) {
			return fmt.Errorf("%s %q must be 1 to 35 letters, digits, '.', '_' or '-'", name, v)
		}
	}
	return nil
}

func (s Subscriber) bankKeys() (x002, e002 *rsa.PublicKey, err error) {
	if x002, err = parsePKIX(s.BankX002); err != nil {
		return nil, nil, fmt.Errorf("bank X002 key: %w", err)
	}
	if e002, err = parsePKIX(s.BankE002); err != nil {
		return nil, nil, fmt.Errorf("bank E002 key: %w", err)
	}
	return x002, e002, nil
}

// BankHashes are the hashes of the bank keys stored by HPB, in the form letters
// print; false while no keys were downloaded.
func (s Subscriber) BankHashes() (BankKeyHashes, bool) {
	x002, e002, err := s.bankKeys()
	if err != nil {
		return BankKeyHashes{}, false
	}
	return BankKeyHashes{X002: FormatDigest(KeyDigest(x002)), E002: FormatDigest(KeyDigest(e002))}, true
}

func parsePKIX(der []byte) (*rsa.PublicKey, error) {
	if len(der) == 0 {
		return nil, errors.New("not downloaded yet")
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("not an RSA key")
	}
	return pub, nil
}
