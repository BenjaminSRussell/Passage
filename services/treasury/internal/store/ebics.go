package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

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
// Vault key names are not an input: they are derived from the principal's tenant,
// the legal entity and the new subscriber's id (ebics.KeyNames), and the table
// refuses any other value. The keys themselves are never given to the service.
type NewEBICSSubscriber struct {
	LegalEntityID string
	BankAccountID string
	HostID        string
	PartnerID     string
	UserID        string
	URL           string
}

const subscriberColumns = `id, legal_entity_id, bank_account_id, host_id, partner_id, user_id, url, a006_key_name, x002_key_name, e002_kv_path,
	a006_key_version, x002_key_version, state, bank_x002, bank_e002, coalesce(fetched_by, ''), keys_confirmed, coalesce(confirmed_by, ''), confirmed_at,
	coalesce(parked_code, ''), coalesce(parked_text, ''), version`

func scanSubscriber(row pgx.Row) (ebics.Subscriber, error) {
	var s ebics.Subscriber
	var state string
	err := row.Scan(&s.ID, &s.LegalEntityID, &s.BankAccountID, &s.HostID, &s.PartnerID, &s.UserID, &s.URL, &s.A006Key, &s.X002Key, &s.E002KVPath,
		&s.A006Version, &s.X002Version, &state, &s.BankX002, &s.BankE002, &s.FetchedBy, &s.KeysConfirmed, &s.ConfirmedBy, &s.ConfirmedAt, &s.ParkedCode, &s.ParkedText, &s.Version)
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
	id := ids.New()
	a006, x002, e002 := ebics.KeyNames(p.TenantID, in.LegalEntityID, id)
	var out ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSubscriber(tx.QueryRow(ctx, `insert into ebics_subscriber
			(tenant_id, id, legal_entity_id, bank_account_id, host_id, partner_id, user_id, url, a006_key_name, x002_key_name, e002_kv_path)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) returning `+subscriberColumns,
			p.TenantID, id, in.LegalEntityID, in.BankAccountID, in.HostID, in.PartnerID, in.UserID, in.URL, a006, x002, e002))
		return err
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == uniqueViolation:
		return ebics.Subscriber{}, httpx.NewProblem(http.StatusConflict, CodeSubscriberDuplicate, "an EBICS subscriber with this host, partner and user, or for this bank account, already exists")
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

// UpdateSubscriber writes the mutable state of a subscriber (state, pinned key
// versions, the bank's keys, who fetched and who confirmed them, parking) when its version is still the stored one.
func (s *Postgres) UpdateSubscriber(ctx context.Context, sub ebics.Subscriber) (ebics.Subscriber, error) {
	p, _ := tenant.FromContext(ctx)
	var out ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSubscriber(tx.QueryRow(ctx, `update ebics_subscriber set state = $3, bank_x002 = $4, bank_e002 = $5, keys_confirmed = $6,
			confirmed_by = nullif($7, ''), confirmed_at = $8, parked_code = nullif($9, ''), parked_text = nullif($10, ''),
			a006_key_version = $12, x002_key_version = $13, fetched_by = nullif($14, ''), version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $11 returning `+subscriberColumns,
			p.TenantID, sub.ID, string(sub.State), sub.BankX002, sub.BankE002, sub.KeysConfirmed, sub.ConfirmedBy, sub.ConfirmedAt, sub.ParkedCode, sub.ParkedText, sub.Version,
			sub.A006Version, sub.X002Version, sub.FetchedBy))
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
// MT940) into the same import the upload endpoint uses. The file may hold
// several statements, for several accounts: each is routed to the bank account
// of the subscriber's legal entity that its own account number names, and a
// statement for an account that is not registered fails the file. A statement
// already imported is a replay, not an error, so a bank that redelivers does no
// harm. Every statement is tried; the failures are returned together.
func (s *Postgres) ImportStatementFile(ctx context.Context, subscriberAccountID, _ string, data []byte) error {
	parsed, err := domain.ParseStatements(data)
	if err != nil {
		return err
	}
	fileSum := sha256.Sum256(data)
	var errs []error
	for i, st := range parsed {
		hash := fileSum
		if len(parsed) > 1 {
			hash = sha256.Sum256(append(append([]byte{}, data...), []byte("#"+strconv.Itoa(i))...))
		}
		accountID, err := s.accountForStatement(ctx, subscriberAccountID, st.Account)
		if err == nil {
			_, err = s.ImportStatement(ctx, StatementImport{BankAccountID: accountID, FileHash: hex.EncodeToString(hash[:]), Parsed: st})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("statement %d: %w", i+1, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Postgres) accountForStatement(ctx context.Context, subscriberAccountID, account string) (string, error) {
	p, _ := tenant.FromContext(ctx)
	var found []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select b.id::text from bank_account b
			join bank_account s on s.tenant_id = b.tenant_id and s.legal_entity_id = b.legal_entity_id
			where b.tenant_id = $1 and s.id = $2 and (b.iban_hash = $3 or b.statement_account_hash = $3) limit 2`,
			p.TenantID, subscriberAccountID, ibanHash(account))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			found = append(found, id)
		}
		return rows.Err()
	})
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeStatementAccount, "the statement names no single bank account of this legal entity")
	}
	return found[0], nil
}

// PollableSubscribers lists the subscribers of the principal's tenant that may
// be polled: READY, bank keys confirmed, not parked.
func (s *Postgres) PollableSubscribers(ctx context.Context) ([]ebics.Subscriber, error) {
	p, _ := tenant.FromContext(ctx)
	var out []ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+subscriberColumns+` from ebics_subscriber
			where tenant_id = $1 and state = 'READY' and keys_confirmed and parked_code is null order by id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sub, err := scanSubscriber(rows)
			if err != nil {
				return err
			}
			out = append(out, sub)
		}
		return rows.Err()
	})
	return out, err
}

// SubscriberForAccount finds the subscriber bound to a bank account. More than
// one is refused rather than picked from: a payment file must not leave through
// a subscriber chosen by row order.
func (s *Postgres) SubscriberForAccount(ctx context.Context, bankAccountID string) (ebics.Subscriber, error) {
	p, _ := tenant.FromContext(ctx)
	var found []ebics.Subscriber
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+subscriberColumns+` from ebics_subscriber where tenant_id = $1 and bank_account_id = $2 limit 2`, p.TenantID, bankAccountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sub, err := scanSubscriber(rows)
			if err != nil {
				return err
			}
			found = append(found, sub)
		}
		return rows.Err()
	})
	switch {
	case err != nil:
		return ebics.Subscriber{}, err
	case len(found) == 0:
		return ebics.Subscriber{}, notFound(CodeSubscriberNotFound, "no EBICS subscriber is bound to this bank account")
	case len(found) > 1:
		return ebics.Subscriber{}, httpx.NewProblem(http.StatusConflict, CodeSubscriberDuplicate, "more than one EBICS subscriber is bound to this bank account")
	}
	return found[0], nil
}

// TryLockSubscriber takes a session advisory lock on one subscriber so that only
// one replica at a time runs a statement download for it: two overlapping
// downloads would each acknowledge the same files and the bank would see
// receipts for a transaction it already closed. The lock lives on a pooled
// connection until unlock; a connection whose lock state is unknown is closed,
// which drops the lock with the session.
func (s *Postgres) TryLockSubscriber(ctx context.Context, id string) (func(), bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	key := "ebics-poll:" + id
	var got bool
	if err := conn.QueryRow(ctx, `select pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&got); err != nil {
		closeConn(conn)
		return nil, false, err
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRow(ctx, `select pg_advisory_unlock(hashtextextended($1, 0))`, key).Scan(&released); err != nil || !released {
			closeConn(conn)
			return
		}
		conn.Release()
	}, true, nil
}

func closeConn(conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Hijack().Close(ctx)
}
