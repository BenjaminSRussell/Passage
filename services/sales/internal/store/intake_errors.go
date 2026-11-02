package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// Intake error problem codes and statuses.
const (
	CodeIntakeErrorNotFound = "SLS-0115"
	CodeIntakeErrorResolved = "SLS-0116"

	IntakeErrorOpen           = "OPEN"
	IntakeErrorResolvedStatus = "RESOLVED"

	maxIntakeMessage = 500
)

// ConcurrentIntake is the 409 SLS-0007 answered when a concurrent request with
// the same Idempotency-Key stored the order first. Unlike a reference reused
// with a different payload it is not a refusal of the order: a retry returns
// it, so the intake error log leaves it out.
type ConcurrentIntake struct{ *httpx.Problem }

// Unwrap gives the problem, so httpx.Fail answers it unchanged.
func (c ConcurrentIntake) Unwrap() error { return c.Problem }

// IsConcurrentIntake reports whether err is a ConcurrentIntake.
func IsConcurrentIntake(err error) bool {
	var c ConcurrentIntake
	return errors.As(err, &c)
}

// IntakeError is one refused channel order (PRD 5.27.14 Intake errors).
// Attempts counts the times the same payload (by hash) failed with the same
// code; the payload itself is not kept.
type IntakeError struct {
	ID             string     `json:"id"`
	LegalEntityID  string     `json:"legalEntityId"`
	Channel        string     `json:"channel"`
	ExternalRef    *string    `json:"externalRef,omitempty"`
	RequestHash    string     `json:"requestHash"`
	ErrorCode      string     `json:"errorCode"`
	Message        string     `json:"message"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	FirstSeenAt    time.Time  `json:"firstSeenAt"`
	LastSeenAt     time.Time  `json:"lastSeenAt"`
	ResolvedBy     *string    `json:"resolvedBy,omitempty"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
	ResolutionNote *string    `json:"resolutionNote,omitempty"`
	Version        int        `json:"version"`
}

// NewIntakeError is a refusal to record.
type NewIntakeError struct {
	LegalEntityID string
	Channel       string
	ExternalRef   string
	RequestHash   string
	ErrorCode     string
	Message       string
}

// IntakeErrorFilter narrows the list. Empty fields do not filter.
type IntakeErrorFilter struct {
	LegalEntityID string
	Channel       string
	Status        string
	ErrorCode     string
	Limit         int
}

const intakeErrorColumns = `id::text, legal_entity_id::text, channel, nullif(external_ref, ''), request_hash, error_code, message,
	status, attempts, first_seen_at, last_seen_at, resolved_by, resolved_at, resolution_note, version`

func scanIntakeError(row pgx.Row) (IntakeError, error) {
	var e IntakeError
	err := row.Scan(&e.ID, &e.LegalEntityID, &e.Channel, &e.ExternalRef, &e.RequestHash, &e.ErrorCode, &e.Message, &e.Status,
		&e.Attempts, &e.FirstSeenAt, &e.LastSeenAt, &e.ResolvedBy, &e.ResolvedAt, &e.ResolutionNote, &e.Version)
	return e, err
}

func intakeErrorData(e IntakeError, actor string) salesevents.IntakeErrorData {
	ref := ""
	if e.ExternalRef != nil {
		ref = *e.ExternalRef
	}
	return salesevents.IntakeErrorData{IntakeErrorID: e.ID, LegalEntityID: e.LegalEntityID, Channel: e.Channel, ExternalRef: ref,
		RequestHash: e.RequestHash, ErrorCode: e.ErrorCode, Attempts: e.Attempts, Actor: actor, Version: e.Version}
}

func truncate(s string, n int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > n {
		return string(r[:n])
	}
	return strings.TrimSpace(s)
}

// RecordIntakeError stores a refusal in its own transaction. The same
// (legal entity, channel, external reference, payload hash, error code) raises
// attempts and reopens a RESOLVED row. intakeerror.recorded is written in the
// same transaction.
func (s *Postgres) RecordIntakeError(ctx context.Context, in NewIntakeError, now time.Time) (IntakeError, error) {
	p, _ := tenant.FromContext(ctx)
	var out IntakeError
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into intake_error (tenant_id, id, legal_entity_id, channel, external_ref, request_hash,
				error_code, message, first_seen_at, last_seen_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
			on conflict (tenant_id, legal_entity_id, channel, external_ref, request_hash, error_code) do update
			set attempts = intake_error.attempts + 1, last_seen_at = excluded.last_seen_at, message = excluded.message,
				status = 'OPEN', resolved_by = null, resolved_at = null, resolution_note = null, version = intake_error.version + 1
			returning `+intakeErrorColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.Channel, in.ExternalRef, in.RequestHash, in.ErrorCode,
			truncate(in.Message, maxIntakeMessage), now)
		var err error
		if out, err = scanIntakeError(row); err != nil {
			return err
		}
		return enqueue(ctx, tx, "intakeerror", out.ID, salesevents.IntakeErrorRecorded, p.TenantID, intakeErrorData(out, ""))
	})
	return out, err
}

// ListIntakeErrors returns refusals in the caller's scope, most recently seen first.
func (s *Postgres) ListIntakeErrors(ctx context.Context, f IntakeErrorFilter) ([]IntakeError, error) {
	p, _ := tenant.FromContext(ctx)
	out := []IntakeError{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+intakeErrorColumns+` from intake_error
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or channel = $3)
				and ($4 = '' or status = $4) and ($5 = '' or error_code = $5)
			order by last_seen_at desc, id limit $6`, p.TenantID, f.LegalEntityID, f.Channel, f.Status, f.ErrorCode, f.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (IntakeError, error) { return scanIntakeError(row) })
		return err
	})
	return out, err
}

func loadIntakeError(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (IntakeError, error) {
	q := `select ` + intakeErrorColumns + ` from intake_error where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	e, err := scanIntakeError(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return IntakeError{}, problem(http.StatusNotFound, CodeIntakeErrorNotFound, "intake error not found")
	}
	return e, err
}

// GetIntakeError reads one refusal.
func (s *Postgres) GetIntakeError(ctx context.Context, id string) (IntakeError, error) {
	p, _ := tenant.FromContext(ctx)
	var out IntakeError
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadIntakeError(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

// ResolveIntakeError closes an OPEN refusal with a note (the operator fixed the
// channel mapping or confirmed the sender will not retry). A stale version is
// 412 and a refusal that is already RESOLVED is 409 (SLS-0116).
// intakeerror.resolved is written in the same transaction.
func (s *Postgres) ResolveIntakeError(ctx context.Context, id string, version int, note, actor string, now time.Time) (IntakeError, error) {
	p, _ := tenant.FromContext(ctx)
	var out IntakeError
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := loadIntakeError(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if e.Version != version {
			return versionConflict()
		}
		if e.Status != IntakeErrorOpen {
			return problem(http.StatusConflict, CodeIntakeErrorResolved, "the intake error is already resolved")
		}
		if _, err := tx.Exec(ctx, `update intake_error set status = 'RESOLVED', resolved_by = $3, resolved_at = $4, resolution_note = $5,
			version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, actor, now, note); err != nil {
			return err
		}
		if out, err = loadIntakeError(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "intakeerror", id, salesevents.IntakeErrorResolved, p.TenantID, intakeErrorData(out, actor))
	})
	return out, err
}
