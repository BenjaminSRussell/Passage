// Package store holds the EAM Postgres repositories. Every write runs inside a
// tenant-scoped transaction (RLS) and writes its outbox event in the same tx.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Service error codes (listed in services/eam/README.md).
const (
	CodeNotFound     = "EAM-NF-001"
	CodeAssetState   = "EAM-AST-001"
	CodeWOTransition = "EAM-WO-001"
	CodeWOTimes      = "EAM-WO-006"
	CodeLineInvalid  = "EAM-PLN-003"
	CodeRequestState = "EAM-REQ-001"
	CodeCapital      = "EAM-WO-005"
	CodeVersion      = "conflict_version"
	CodeValidation   = "EAM-VAL-001"
	CodeKeyReuse     = "idempotency_key_reuse"
	CodeDuplicate    = "EAM-DUP-001"
	CodeFaultMissing = "EAM-FLT-001"
	CodeFaultInvalid = "EAM-FLT-002"
	CodeDowntime     = "EAM-DT-001"
)

// Page is one page of a list, newest first. NextCursor is the id to pass as
// the next request's cursor; it is empty on the last page.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListOptions narrows and pages a list. Cursor is the last id of the previous
// page (ids are UUIDv7, so id order is creation order).
type ListOptions struct {
	Status  string
	AssetID string
	Cursor  string
	Limit   int
}

// pageOf trims a result fetched with limit+1 rows into a page.
func pageOf[T any](items []T, limit int, id func(T) string) Page[T] {
	if items == nil {
		items = []T{}
	}
	if len(items) > limit {
		items = items[:limit]
		return Page[T]{Items: items, NextCursor: id(items[limit-1])}
	}
	return Page[T]{Items: items}
}

const eventSource = "/passage/eam/eam"

// Postgres is the EAM repository.
type Postgres struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// New returns a repository that reads the wall clock.
func New(pool *pgxpool.Pool) *Postgres {
	return NewWithClock(pool, func() time.Time { return time.Now().UTC() })
}

// NewWithClock returns a repository with an injected clock (tests, simulations).
func NewWithClock(pool *pgxpool.Pool, now func() time.Time) *Postgres {
	return &Postgres{pool: pool, now: now}
}

// Ready checks the database connection (readyz).
func (s *Postgres) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

func problem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

func tenantOf(ctx context.Context) (string, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return "", problem(401, "AUTH-0001", "no principal")
	}
	return p.TenantID, nil
}

// withTx runs fn in a transaction scoped to the caller's tenant.
func (s *Postgres) withTx(ctx context.Context, fn func(tx pgx.Tx, t string) error) error {
	t, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx, t) })
}

// enqueue writes a validated event to the outbox inside tx.
func enqueue(ctx context.Context, tx pgx.Tx, tenantID, aggregateType, aggregateID, typ string, data any) error {
	evt, err := events.New(typ, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

// existingID returns the id of the row in table that was created with key, or "".
// Table names are internal constants, never request input.
func existingID(ctx context.Context, tx pgx.Tx, table, t, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, fmt.Sprintf("select id::text from %s where tenant_id = $1 and idempotency_key = $2", table), t, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// taken reports whether a row with the given unique value already exists.
// Table and column names are internal constants, never request input.
func taken(ctx context.Context, tx pgx.Tx, table, where string, args ...any) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, fmt.Sprintf("select exists(select 1 from %s where %s)", table, where), args...).Scan(&exists)
	return exists, err
}

// notFound maps a missing row to a 404 problem.
func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return problem(404, CodeNotFound, what+" not found")
	}
	return err
}

func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// numText renders a float as a decimal string for a ::numeric parameter.
func numText(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func numPtr(f *float64) *string {
	if f == nil {
		return nil
	}
	s := numText(*f)
	return &s
}
