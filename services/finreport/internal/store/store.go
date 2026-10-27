package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Problem codes raised by the store (PRD §2.15.18 and the slice's additions).
const (
	CodeDefinitionNotFound = "REPORT-0020"
	CodeRunNotFound        = "REPORT-0024"
	CodeCellNotFound       = "REPORT-0025"
	CodeNotPublished       = "REPORT-0001"
	CodeImportDuplicate    = "REPORT-0011"
	CodeRevisionConflict   = "conflict_version"
	CodeIdempotencyReuse   = "idempotency_key_reuse"
	// CodeIdempotencyInFlight answers a concurrent request that lost the race
	// to store the same Idempotency-Key; the client retries and gets the replay.
	CodeIdempotencyInFlight = "idempotency_key_in_flight"
	uniqueViolation         = "23505"
	eventSource             = "finreport"
)

// Postgres is the reporting store. Every method runs inside a tenant
// transaction so row-level security applies.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// enqueue writes an event to the outbox inside tx (CONVENTIONS: same transaction).
func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType, tenantID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func tenantID(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

// isUniqueViolation reports whether err is a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func problem(status int, code, detail string) *httpx.Problem {
	return httpx.NewProblem(status, code, detail)
}
