package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/bankconnect"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/ebics"
)

const (
	CodeSubscriberNotFound  = "TRS-0495"
	CodeSubscriberDuplicate = "TRS-0496"
	CodeSubscriberInvalid   = "TRS-0497"
)

// NewEBICSSubscriber registers an EBICS user at a bank for one bank account. The
// key names point into Vault; the keys themselves are never given to the service.
type NewEBICSSubscriber struct {
	LegalEntityID string
	BankAccountID string
	HostID        string
	PartnerID     string
	UserID        string
	URL           string
	A006Key       string
	X002Key       string
	E002KVPath    string
}

const subscriberColumns = `id, legal_entity_id, bank_account_id, host_id, partner_id, user_id, url, a006_key_name, x002_key_name, e002_kv_path,
	state, bank_x002, bank_e002, keys_confirmed, coalesce(confirmed_by, ''), confirmed_at, coalesce(parked_code, ''), coalesce(parked_text, ''), version`

func scanSubscriber(row pgx.Row) (ebics.Subscriber, error) {
	var s ebics.Subscriber
	var state string
	err := row.Scan(&s.ID, &s.LegalEntityID, &s.BankAccountID, &s.HostID, &s.PartnerID, &s.UserID, &s.URL, &s.A006Key, &s.X002Key, &s.E002KVPath,
		&state, &s.BankX002, &s.BankE002, &s.KeysConfirmed, &s.ConfirmedBy, &s.ConfirmedAt, &s.ParkedCode, &s.ParkedText, &s.Version)
	s.State = ebics.State(state)
	return s, err
}

func (s *Postgres) CreateEBICSSubscriber(ctx context.Context, in NewEBICSSubscriber) (ebics.Subscriber, error) {
	p, _ := tenant.FromContext(ctx)
	probe := ebics.Subscriber{HostID: in.HostID, PartnerID: in.PartnerID, UserID: in.UserID}
	if err := probe.Validate(); err != nil {
		return ebics.Subscriber{}, unprocessable(CodeSubscriberInvalid, err.Error())
	}
	if err := bankconnect.CheckURL(in.URL); err != nil {
		return ebics.Subscriber{}, unprocessable(CodeSubscriberInvalid, err.Error())
	}
	var out ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSubscriber(tx.QueryRow(ctx, `insert into ebics_subscriber
			(tenant_id, id, legal_entity_id, bank_account_id, host_id, partner_id, user_id, url, a006_key_name, x002_key_name, e002_kv_path)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning `+subscriberColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.BankAccountID, in.HostID, in.PartnerID, in.UserID, in.URL, in.A006Key, in.X002Key, in.E002KVPath))
		return err
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == uniqueViolation:
		return ebics.Subscriber{}, httpx.NewProblem(http.StatusConflict, CodeSubscriberDuplicate, "an EBICS subscriber with this host, partner and user already exists")
	case errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation:
		return ebics.Subscriber{}, notFound(CodeAccountNotFound, "bank account not found for this legal entity")
	}
	return out, err
}

func (s *Postgres) GetSubscriber(ctx context.Context, id string) (ebics.Subscriber, error) {
	p, _ := tenant.FromContext(ctx)
	var out ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSubscriber(tx.QueryRow(ctx, `select `+subscriberColumns+` from ebics_subscriber where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ebics.Subscriber{}, notFound(CodeSubscriberNotFound, "EBICS subscriber not found")
	}
	return out, err
}

// UpdateSubscriber writes the mutable state of a subscriber (state, the bank's
// keys, their confirmation, parking) when its version is still the stored one.
func (s *Postgres) UpdateSubscriber(ctx context.Context, sub ebics.Subscriber) (ebics.Subscriber, error) {
	p, _ := tenant.FromContext(ctx)
	var out ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSubscriber(tx.QueryRow(ctx, `update ebics_subscriber set state = $3, bank_x002 = $4, bank_e002 = $5, keys_confirmed = $6,
			confirmed_by = nullif($7, ''), confirmed_at = $8, parked_code = nullif($9, ''), parked_text = nullif($10, ''), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $11 returning `+subscriberColumns,
			p.TenantID, sub.ID, string(sub.State), sub.BankX002, sub.BankE002, sub.KeysConfirmed, sub.ConfirmedBy, sub.ConfirmedAt, sub.ParkedCode, sub.ParkedText, sub.Version))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := s.GetSubscriber(ctx, sub.ID); gerr != nil {
			return ebics.Subscriber{}, gerr
		}
		return ebics.Subscriber{}, ebics.ErrVersionConflict
	}
	return out, err
}

// ImportStatementFile feeds one downloaded statement file (camt.053, BAI2 or
// MT940) into the same import the upload endpoint uses. A file already imported
// is a replay, not an error, so a bank that redelivers does no harm.
func (s *Postgres) ImportStatementFile(ctx context.Context, bankAccountID, _ string, data []byte) error {
	sum := sha256.Sum256(data)
	parsed, err := domain.ParseStatements(data)
	if err == nil && len(parsed) != 1 {
		err = &domain.Error{Code: domain.CodeStatementFormat, Detail: "a file must hold exactly one statement; split multi-statement files"}
	}
	if err != nil {
		return err
	}
	_, err = s.ImportStatement(ctx, StatementImport{BankAccountID: bankAccountID, FileHash: hex.EncodeToString(sum[:]), Parsed: parsed[0]})
	return err
}
