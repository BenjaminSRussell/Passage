// Package store persists condition records. Every write runs in a tenant-scoped
// transaction (RLS) and enqueues its event in the same transaction.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// Problem codes raised by the store (message catalog PRC-*).
const (
	CodeRecordNotFound  = "PRC-0030"
	CodeStateInvalid    = "PRC-0031"
	CodeVersionConflict = "conflict_version"
	CodeIdempotencyUse  = "idempotency_key_reuse"
	eventSource         = "pricing"
	MaxListLimit        = 100
)

// Entry is a stored condition record with its owning legal entity.
type Entry struct {
	domain.Record
	LegalEntityID string
}

// Filter narrows a record list. AsOf (when set) keeps records valid on that day.
// Customer and Product match the record's customer or product key exactly.
type Filter struct {
	LegalEntityID string
	Status        string
	Type          string
	Customer      string
	Product       string
	AsOf          *time.Time
	Limit         int
}

// Store is what the API needs. Postgres implements it; tests use an in-memory fake.
type Store interface {
	CreateRecord(ctx context.Context, in Entry, idempotencyKey string) (Entry, error)
	GetRecord(ctx context.Context, id string) (Entry, error)
	ListRecords(ctx context.Context, f Filter) ([]Entry, error)
	ActiveRecords(ctx context.Context, legalEntityID, currency string, asOf time.Time) ([]domain.Record, error)
	ActivateRecord(ctx context.Context, id string, version int, idempotencyKey string) (Entry, error)
	WithdrawRecord(ctx context.Context, id string, version int, reason, idempotencyKey string) (Entry, error)
	CreateJournal(ctx context.Context, in JournalInput, idempotencyKey string) (Journal, error)
	ListJournals(ctx context.Context, f JournalFilter) ([]Journal, error)
	GetJournal(ctx context.Context, id string) (Journal, error)
	ValidateJournal(ctx context.Context, id string) (Journal, error)
	PostJournal(ctx context.Context, id, idempotencyKey string) (Journal, error)
	ReverseJournal(ctx context.Context, id, idempotencyKey string) (Journal, error)
	CreateAgreement(ctx context.Context, a domain.RebateAgreement, idempotencyKey string) (domain.RebateAgreement, error)
	GetAgreement(ctx context.Context, id string) (domain.RebateAgreement, error)
	ListAgreements(ctx context.Context, legalEntityID string, limit int) ([]domain.RebateAgreement, error)
	AccrueInvoiceLine(ctx context.Context, in AccrualInput) (Accrual, bool, error)
	ListAccruals(ctx context.Context, f AccrualFilter) ([]Accrual, error)
	Ping(ctx context.Context) error
}

// Postgres is the production store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps a pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database is reachable.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const recordColumns = `id, legal_entity_id, condition_type, customer_kind, customer_key, product_kind, product_key, variant,
	currency, unit, from_quantity::text, amount::text, disc_pct_1::text, disc_pct_2::text, valid_from, valid_to,
	search_next, exclusive, status, version`

// KeyHash is the lookup key for overlap checks; it mirrors the domain key tuple.
func KeyHash(r domain.Record) string {
	parts := []string{string(r.Type), string(r.CustomerKind), r.CustomerKey, string(r.ProductKind), r.ProductKey, r.Variant, r.Currency, r.Unit, r.FromQuantity.String()}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func scanEntry(row pgx.Row) (Entry, error) {
	var e Entry
	var ctype, ckind, pkind string
	var from, amount, p1, p2 string
	err := row.Scan(&e.ID, &e.LegalEntityID, &ctype, &ckind, &e.CustomerKey, &pkind, &e.ProductKey, &e.Variant,
		&e.Currency, &e.Unit, &from, &amount, &p1, &p2, &e.ValidFrom, &e.ValidTo, &e.SearchNext, &e.Exclusive, &e.Status, &e.Version)
	if err != nil {
		return Entry{}, err
	}
	e.Type, e.CustomerKind, e.ProductKind = domain.ConditionType(ctype), domain.CustomerKind(ckind), domain.ProductKind(pkind)
	e.FromQuantity, e.Amount = decimal.RequireFromString(from), decimal.RequireFromString(amount)
	e.DiscPct1, e.DiscPct2 = decimal.RequireFromString(p1), decimal.RequireFromString(p2)
	return e, nil
}

func notFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeRecordNotFound, "no condition record "+id)
}

// CreateRecord inserts a DRAFT record. A repeated Idempotency-Key returns the
// original row; the insert uses ON CONFLICT so two concurrent requests with the
// same key cannot both create a record or fail with a unique violation.
func (s *Postgres) CreateRecord(ctx context.Context, in Entry, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		created, err := insertDraft(ctx, tx, p.TenantID, in, key)
		if err == nil {
			out = created
			return nil
		}
		if key == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		replay, err := scanEntry(tx.QueryRow(ctx, `select `+recordColumns+` from condition_record where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
		if err != nil {
			return err
		}
		if !sameBody(replay, in) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
		}
		out = replay
		return nil
	})
	return out, err
}

func insertDraft(ctx context.Context, tx pgx.Tx, tenantID string, in Entry, key *string) (Entry, error) {
	return scanEntry(tx.QueryRow(ctx, `insert into condition_record (tenant_id, id, legal_entity_id, condition_type, customer_kind, customer_key,
		product_kind, product_key, variant, currency, unit, from_quantity, amount, disc_pct_1, disc_pct_2, valid_from, valid_to,
		search_next, exclusive, status, version, key_hash, idempotency_key)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::numeric, $13::numeric, $14::numeric, $15::numeric, $16, $17, $18, $19, 'DRAFT', 1, $20, $21)
		on conflict (tenant_id, idempotency_key) do nothing
		returning `+recordColumns,
		tenantID, ids.New(), in.LegalEntityID, string(in.Type), string(in.CustomerKind), in.CustomerKey, string(in.ProductKind), in.ProductKey,
		in.Variant, in.Currency, in.Unit, in.FromQuantity.String(), in.Amount.String(), in.DiscPct1.String(), in.DiscPct2.String(),
		in.ValidFrom, in.ValidTo, in.SearchNext, in.Exclusive, KeyHash(in.Record), key))
}

// sameBody reports whether a replayed key carries the same economic content.
func sameBody(a, b Entry) bool {
	return KeyHash(a.Record) == KeyHash(b.Record) && a.Amount.Equal(b.Amount) && a.DiscPct1.Equal(b.DiscPct1) &&
		a.DiscPct2.Equal(b.DiscPct2) && a.LegalEntityID == b.LegalEntityID && a.SearchNext == b.SearchNext && a.Exclusive == b.Exclusive &&
		dayStr(a.ValidFrom) == dayStr(b.ValidFrom) && dayStr(a.ValidTo) == dayStr(b.ValidTo)
}

func dayStr(t time.Time) string { return t.Format("2006-01-02") }

// GetRecord reads one record.
func (s *Postgres) GetRecord(ctx context.Context, id string) (Entry, error) {
	var e Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		e, err = scanEntry(tx.QueryRow(ctx, `select `+recordColumns+` from condition_record where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(id)
		}
		return err
	})
	return e, err
}

// ListRecords returns records ordered by id, optionally filtered by status and validity.
func (s *Postgres) ListRecords(ctx context.Context, f Filter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	var asOf *string
	if f.AsOf != nil {
		day := dayStr(*f.AsOf)
		asOf = &day
	}
	out := []Entry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
			where ($1::text = '' or legal_entity_id::text = $1) and ($2::text = '' or status = $2)
			and ($3::date is null or (valid_from <= $3::date and valid_to >= $3::date))
			and ($5::text = '' or condition_type = $5) and ($6::text = '' or customer_key = $6) and ($7::text = '' or product_key = $7)
			order by id limit $4`, f.LegalEntityID, f.Status, asOf, f.Limit, f.Type, f.Customer, f.Product)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// ActiveRecords loads the ACTIVE records a resolve needs: one legal entity, one
// currency, valid on asOf. The engine applies the key and quantity filters.
func (s *Postgres) ActiveRecords(ctx context.Context, legalEntityID, currency string, asOf time.Time) ([]domain.Record, error) {
	out := []domain.Record{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
			where legal_entity_id::text = $1 and currency = $2 and status = 'ACTIVE' and valid_from <= $3::date and valid_to >= $3::date`,
			legalEntityID, currency, dayStr(asOf))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			out = append(out, e.Record)
		}
		return rows.Err()
	})
	return out, err
}

// lockRecord reads a record FOR UPDATE, so lifecycle commands on one record run one at a time.
func lockRecord(ctx context.Context, tx pgx.Tx, id string) (Entry, error) {
	cur, err := scanEntry(tx.QueryRow(ctx, `select `+recordColumns+` from condition_record where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, notFound(id)
	}
	return cur, err
}

// commandHash identifies a lifecycle command for Idempotency-Key replay.
func commandHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// replayCommand returns the stored response when key was already used for the same
// command, and idempotency_key_reuse when it was used for a different one. It runs
// after lockRecord, so a concurrent retry waits for the first call and then replays it.
func replayCommand(ctx context.Context, tx pgx.Tx, tenantID, key, hash string) (Entry, bool, error) {
	var e Entry
	ok, err := replayJSON(ctx, tx, tenantID, key, hash, &e)
	return e, ok, err
}

// rememberCommand stores the response of a lifecycle command under its key.
func rememberCommand(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, e Entry) error {
	return rememberJSON(ctx, tx, tenantID, key, hash, e)
}

// replayJSON decodes the response stored under key into out and reports whether one
// existed. A key stored for a different command hash is idempotency_key_reuse.
func replayJSON(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, out any) (bool, error) {
	if key == "" {
		return false, nil
	}
	var stored string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where tenant_id = $1 and key = $2`, tenantID, key).Scan(&stored, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != hash {
		return false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request")
	}
	return true, json.Unmarshal(body, out)
}

// rememberJSON stores a command's response under its key, in the command's transaction.
func rememberJSON(ctx context.Context, tx pgx.Tx, tenantID, key, hash string, v any) error {
	if key == "" {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, 200, $4)`,
		tenantID, key, hash, body)
	return err
}

// ActivateRecord moves DRAFT to ACTIVE (BR-26.6). Under a transaction-scoped advisory
// lock on the tenant and key hash it rejects any ACTIVE record with the same key and
// overlapping validity (409 PRC-0002, naming the conflict). A repeated Idempotency-Key
// replays the first response.
func (s *Postgres) ActivateRecord(ctx context.Context, id string, version int, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	var out Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("activate", id, strconv.Itoa(version))
		if replay, ok, err := replayCommand(ctx, tx, p.TenantID, idempotencyKey, hash); err != nil || ok {
			out = replay
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "record version does not match")
		}
		if cur.Status != domain.StatusDraft {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only DRAFT records can be activated")
		}
		// A price journal posting for this legal entity holds the entity lock exclusively and
		// checks overlaps against every ACTIVE record; activations share it, so the two never
		// validate against each other's uncommitted state.
		if err := lockEntityShared(ctx, tx, p.TenantID, cur.LegalEntityID); err != nil {
			return err
		}
		key := KeyHash(cur.Record)
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, p.TenantID+"|"+key); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+recordColumns+` from condition_record
			where legal_entity_id = $1 and key_hash = $2 and status = 'ACTIVE' and id <> $3`, cur.LegalEntityID, key, id)
		if err != nil {
			return err
		}
		var active []domain.Record
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				rows.Close()
				return err
			}
			active = append(active, e.Record)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if c, clash := domain.Conflict(cur.Record, active); clash {
			return httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, fmt.Sprintf("overlapping active record %s for the same key, currency and unit", c.ID))
		}
		var retroactive bool
		out, err = scanEntry(tx.QueryRow(ctx, `update condition_record set status = 'ACTIVE', version = version + 1
			where id = $1 and version = $2 returning `+recordColumns, id, version))
		if err != nil {
			return err
		}
		// BR-26.7: a record whose validity starts before today is announced as retroactive.
		if err := tx.QueryRow(ctx, `select $1::date < current_date`, dayStr(out.ValidFrom)).Scan(&retroactive); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, out, pevents.ConditionRecordActivated, pevents.ConditionRecordData{ID: out.ID, LegalEntityID: out.LegalEntityID,
			Type: string(out.Type), CustomerKind: string(out.CustomerKind), CustomerKey: out.CustomerKey, ProductKind: string(out.ProductKind),
			ProductKey: out.ProductKey, Variant: out.Variant, Currency: out.Currency, Unit: out.Unit, FromQuantity: out.FromQuantity.String(),
			ValidFrom: dayStr(out.ValidFrom), ValidTo: dayStr(out.ValidTo), Version: out.Version, Retroactive: retroactive}); err != nil {
			return err
		}
		return rememberCommand(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// WithdrawRecord moves ACTIVE to WITHDRAWN (BR-26.9) and announces it. A repeated
// Idempotency-Key replays the first response.
func (s *Postgres) WithdrawRecord(ctx context.Context, id string, version int, reason, idempotencyKey string) (Entry, error) {
	p, _ := tenant.FromContext(ctx)
	var out Entry
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockRecord(ctx, tx, id)
		if err != nil {
			return err
		}
		hash := commandHash("withdraw", id, strconv.Itoa(version), reason)
		if replay, ok, err := replayCommand(ctx, tx, p.TenantID, idempotencyKey, hash); err != nil || ok {
			out = replay
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "record version does not match")
		}
		if cur.Status != domain.StatusActive {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only ACTIVE records can be withdrawn")
		}
		out, err = scanEntry(tx.QueryRow(ctx, `update condition_record set status = 'WITHDRAWN', version = version + 1
			where id = $1 and version = $2 returning `+recordColumns, id, version))
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, out, pevents.ConditionRecordWithdrawn, pevents.ConditionRecordWithdrawnData{ID: out.ID, Version: out.Version, Reason: reason}); err != nil {
			return err
		}
		return rememberCommand(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// enqueue writes the event into the outbox inside tx (same transaction as the change).
func enqueue(ctx context.Context, tx pgx.Tx, e Entry, typ string, data any) error {
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(typ, eventSource, e.ID, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "conditionrecord", e.ID, evt)
}
