package store

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// oneTimePartyField is the fieldcrypt field name the one-time party is sealed
// under; it is bound into the envelope with the tenant id, so a value copied
// to another tenant or column does not open.
const oneTimePartyField = "sales_order.one_time_party"

// keyedHashPrefix marks a request hash that is an HMAC under a fieldcrypt key
// ("hmac:<key id>:<hex>") rather than a plain SHA-256.
const keyedHashPrefix = "hmac:"

// sealedParty is the one-time party as loadOrder reads it: the envelope and
// the clear country. It marks the order as one-time without opening it.
type sealedParty struct {
	envelope []byte
	country  string
}

// errNoFieldCrypt refuses to store or read a one-time party when the service
// has no field encryption keys: the buyer's personal data is never kept in clear.
func errNoFieldCrypt() error {
	return problem(http.StatusServiceUnavailable, CodeFieldCryptMissing,
		"field encryption is not configured; one-time customer orders cannot be stored or read")
}

// WithFieldCrypt sets the keys that seal the one-time party (FIELDCRYPT_KEYS).
// Without them one-time customer orders are refused with 503 SLS-0054.
func (s *Postgres) WithFieldCrypt(k *fieldcrypt.Keys) *Postgres {
	s.crypt = k
	return s
}

// isOneTime reports whether o is a one-time customer order.
func (o Order) isOneTime() bool { return o.oneTime != nil }

// sealOneTime seals in.OneTime for tenantID and replaces the plain request
// hash with a keyed one, so neither the column nor the idempotency record lets
// anyone confirm a guessed buyer name or tax id.
func (s *Postgres) sealOneTime(tenantID string, in *NewOrder) error {
	if in.OneTime == nil {
		return nil
	}
	if s.crypt == nil {
		return errNoFieldCrypt()
	}
	plain, err := json.Marshal(in.OneTime)
	if err != nil {
		return err
	}
	if in.oneTimeSealed, err = s.crypt.Seal(tenantID, oneTimePartyField, plain); err != nil {
		return err
	}
	digest := in.RequestHash
	if in.RequestHash, err = s.keyedHash(s.crypt.CurrentID(), tenantID, digest); err != nil {
		return err
	}
	// A replay may arrive after a key rotation: the stored hash names its key.
	in.sameRequest = func(stored string) bool {
		keyID, _, ok := strings.Cut(strings.TrimPrefix(stored, keyedHashPrefix), ":")
		if !ok || !strings.HasPrefix(stored, keyedHashPrefix) {
			return false
		}
		want, err := s.keyedHash(keyID, tenantID, digest)
		return err == nil && hmac.Equal([]byte(want), []byte(stored))
	}
	return nil
}

func (s *Postgres) keyedHash(keyID, tenantID, digest string) (string, error) {
	mac, err := s.crypt.MACWithKey(keyID, tenantID, []byte("sales.order.intake\x00"+digest))
	if err != nil {
		return "", err
	}
	return keyedHashPrefix + keyID + ":" + hex.EncodeToString(mac), nil
}

// openOneTime opens the one-time party of o into o.OneTime.
func (s *Postgres) openOneTime(tenantID string, o *Order) error {
	if o == nil || o.oneTime == nil || o.OneTime != nil {
		return nil
	}
	if s.crypt == nil {
		return errNoFieldCrypt()
	}
	plain, err := s.crypt.Open(tenantID, oneTimePartyField, o.oneTime.envelope)
	if err != nil {
		return err
	}
	var party domain.OneTimeParty
	if err := json.Unmarshal(plain, &party); err != nil {
		return err
	}
	o.OneTime = &party
	return nil
}

// orderTx runs fn in a tenant transaction and opens the one-time party of the
// order it returns before the transaction commits, so a write never commits
// with a response that cannot show its buyer.
func (s *Postgres) orderTx(ctx context.Context, tenantID string, o *Order, fn func(tx pgx.Tx) error) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return s.openOneTime(tenantID, o)
	})
}

// checkOneTimeLimitTx refuses a one-time order whose net total is over the
// legal entity's one_time_order_limit (FR-27.22, SLS-0016). Callers hold the
// order row locked (or are inserting it), so two edits of one order cannot
// both pass on the same old total.
func checkOneTimeLimitTx(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, net decimal.Decimal, currency string) error {
	params, err := loadParameters(ctx, tx, tenantID, legalEntityID)
	if err != nil {
		return err
	}
	err = params.Parameters.CheckOneTimeLimit(net, currency)
	var limit *domain.OneTimeLimitError
	if errors.As(err, &limit) {
		return problem(http.StatusUnprocessableEntity, CodeOneTimeLimit, limit.Detail)
	}
	return err
}
