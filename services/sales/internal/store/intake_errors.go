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
	// CodeIntakeNotReplayable is the 409 of a replay of a row whose request
	// body was not kept (a row logged before slice 30).
	CodeIntakeNotReplayable = "SLS-0118"

	IntakeErrorOpen           = "OPEN"
	IntakeErrorResolvedStatus = "RESOLVED"

	maxIntakeMessage = 500
	// MaxReplayAttempts is the dead-letter threshold of PRD 5.27.10: a row
	// that failed this many times is left out of the bulk replay and waits for
	// an operator.
	MaxReplayAttempts = 10
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
// code; the payload is kept only while the row is OPEN, for a replay, and is
// never returned (Replayable says whether it is there).
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
	// Replayable is true while the refused request body is kept (an OPEN row
	// logged since slice 30); the body itself is never returned.
	Replayable bool `json:"replayable"`
	// ReplayCount counts the replays tried; ReplayedOrderID is the order the
	// successful replay created or found.
	ReplayCount     int     `json:"replayCount"`
	ReplayedOrderID *string `json:"replayedOrderId,omitempty"`
	Version         int     `json:"version"`
}

// IntakeReplay is what a replay needs of a refused order: the request body and
// the Idempotency-Key it arrived with (empty when it had none).
// A sealed body is opened before it is returned. Neither is ever serialized.
type IntakeReplay struct {
	IntakeError
	Payload        []byte `json:"-"`
	IdempotencyKey string `json:"-"`
}

// NewIntakeError is a refusal to record.
type NewIntakeError struct {
	LegalEntityID string
	Channel       string
	ExternalRef   string
	RequestHash   string
	ErrorCode     string
	Message       string
	// Payload is the refused request body as JSON and IdempotencyKey the
	// header it carried; both let the row be replayed later.
	Payload        []byte
	IdempotencyKey string
	// PersonalData marks a body that carries a one-time buyer: it is kept only
	// sealed (intakePayloadField), and not at all without field encryption keys.
	PersonalData bool
}

// intakePayloadField is the fieldcrypt field a one-time customer request body
// is sealed under on intake_error.
const intakePayloadField = "intake_error.request_payload"

// IntakeErrorFilter narrows the list. Empty fields do not filter.
type IntakeErrorFilter struct {
	LegalEntityID string
	Channel       string
	Status        string
	ErrorCode     string
	Limit         int
}

const intakeErrorColumns = `id::text, legal_entity_id::text, channel, nullif(external_ref, ''), request_hash, error_code, message,
	status, attempts, first_seen_at, last_seen_at, resolved_by, resolved_at, resolution_note,
	(request_payload is not null or request_payload_sealed is not null), replay_count, replayed_order_id::text, version`

func scanIntakeError(row pgx.Row) (IntakeError, error) {
	var e IntakeError
	err := row.Scan(&e.ID, &e.LegalEntityID, &e.Channel, &e.ExternalRef, &e.RequestHash, &e.ErrorCode, &e.Message, &e.Status,
		&e.Attempts, &e.FirstSeenAt, &e.LastSeenAt, &e.ResolvedBy, &e.ResolvedAt, &e.ResolutionNote, &e.Replayable, &e.ReplayCount, &e.ReplayedOrderID, &e.Version)
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
	clear, sealed, err := s.keepPayload(p.TenantID, in)
	if err != nil {
		return IntakeError{}, err
	}
	var out IntakeError
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `insert into intake_error (tenant_id, id, legal_entity_id, channel, external_ref, request_hash,
				error_code, message, first_seen_at, last_seen_at, request_payload, request_payload_sealed, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, $11, $12)
			on conflict (tenant_id, legal_entity_id, channel, external_ref, request_hash, error_code) do update
			set attempts = intake_error.attempts + 1, last_seen_at = excluded.last_seen_at, message = excluded.message,
				status = 'OPEN', resolved_by = null, resolved_at = null, resolution_note = null, replayed_order_id = null,
				request_payload = excluded.request_payload, request_payload_sealed = excluded.request_payload_sealed,
				idempotency_key = excluded.idempotency_key, version = intake_error.version + 1
			returning `+intakeErrorColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.Channel, in.ExternalRef, in.RequestHash, in.ErrorCode,
			truncate(in.Message, maxIntakeMessage), now, clear, sealed, nullable(in.IdempotencyKey))
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
			request_payload = null, request_payload_sealed = null, idempotency_key = null, version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, id, actor, now, note); err != nil {
			return err
		}
		if out, err = loadIntakeError(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "intakeerror", id, salesevents.IntakeErrorResolved, p.TenantID, intakeErrorData(out, actor))
	})
	return out, err
}

// keepPayload is how a refused body is stored: in clear (request_payload) for
// an ordinary request, sealed (request_payload_sealed) when it carries a
// one-time buyer, and not at all for such a request when the service has no
// field encryption keys (the row is then not replayable).
func (s *Postgres) keepPayload(tenantID string, in NewIntakeError) (clear, sealed any, err error) {
	if len(in.Payload) == 0 {
		return nil, nil, nil
	}
	if !in.PersonalData {
		return in.Payload, nil, nil
	}
	if s.crypt == nil {
		return nil, nil, nil
	}
	env, err := s.crypt.Seal(tenantID, intakePayloadField, in.Payload)
	if err != nil {
		return nil, nil, err
	}
	return nil, env, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// GetIntakeReplay reads an OPEN refusal with the request body kept for it. A
// RESOLVED row is 409 SLS-0116 and a row without a kept body 409 SLS-0118.
func (s *Postgres) GetIntakeReplay(ctx context.Context, id string) (IntakeReplay, error) {
	p, _ := tenant.FromContext(ctx)
	var out IntakeReplay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := loadIntakeError(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if e.Status != IntakeErrorOpen {
			return problem(http.StatusConflict, CodeIntakeErrorResolved, "the intake error is already resolved")
		}
		var clear, sealed []byte
		var key *string
		if err := tx.QueryRow(ctx, `select request_payload, request_payload_sealed, idempotency_key from intake_error
			where tenant_id = $1 and id = $2`, p.TenantID, id).Scan(&clear, &sealed, &key); err != nil {
			return err
		}
		out, err = s.replayOf(p.TenantID, e, clear, sealed, key)
		return err
	})
	return out, err
}

// replayOf assembles a replay from the kept columns, opening a sealed body.
func (s *Postgres) replayOf(tenantID string, e IntakeError, clear, sealed []byte, key *string) (IntakeReplay, error) {
	out := IntakeReplay{IntakeError: e, Payload: clear}
	if key != nil {
		out.IdempotencyKey = *key
	}
	if len(sealed) > 0 {
		if s.crypt == nil {
			return IntakeReplay{}, errNoFieldCrypt()
		}
		plain, err := s.crypt.Open(tenantID, intakePayloadField, sealed)
		if err != nil {
			return IntakeReplay{}, err
		}
		out.Payload = plain
	}
	if len(out.Payload) == 0 {
		return IntakeReplay{}, problem(http.StatusConflict, CodeIntakeNotReplayable, "the refused request was not kept; ask the sender to send it again and resolve this error")
	}
	return out, nil
}

// ListIntakeReplays returns up to limit OPEN refusals with a kept body, oldest
// first, for a bulk replay. Rows at or beyond MaxReplayAttempts are the dead
// letters: they are counted in skipped and not returned.
func (s *Postgres) ListIntakeReplays(ctx context.Context, f IntakeErrorFilter) (rows []IntakeReplay, skipped int, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		const where = `tenant_id = $1 and status = 'OPEN' and (request_payload is not null or request_payload_sealed is not null)
			and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or channel = $3) and ($4 = '' or error_code = $4)`
		if err := tx.QueryRow(ctx, `select count(*) from intake_error where `+where+` and attempts >= $5`,
			p.TenantID, f.LegalEntityID, f.Channel, f.ErrorCode, MaxReplayAttempts).Scan(&skipped); err != nil {
			return err
		}
		rs, err := tx.Query(ctx, `select `+intakeErrorColumns+`, request_payload, request_payload_sealed, idempotency_key
			from intake_error where `+where+` and attempts < $5
			order by first_seen_at, id limit $6`, p.TenantID, f.LegalEntityID, f.Channel, f.ErrorCode, MaxReplayAttempts, f.Limit)
		if err != nil {
			return err
		}
		rows, err = pgx.CollectRows(rs, func(row pgx.CollectableRow) (IntakeReplay, error) {
			var e IntakeError
			var clear, sealed []byte
			var key *string
			if err := row.Scan(&e.ID, &e.LegalEntityID, &e.Channel, &e.ExternalRef, &e.RequestHash, &e.ErrorCode, &e.Message, &e.Status,
				&e.Attempts, &e.FirstSeenAt, &e.LastSeenAt, &e.ResolvedBy, &e.ResolvedAt, &e.ResolutionNote, &e.Replayable, &e.ReplayCount,
				&e.ReplayedOrderID, &e.Version, &clear, &sealed, &key); err != nil {
				return IntakeReplay{}, err
			}
			return s.replayOf(p.TenantID, e, clear, sealed, key)
		})
		return err
	})
	return rows, skipped, err
}

// RecordReplayFailure notes that a replay of an OPEN refusal was refused again:
// the attempt is counted, the message is replaced by the new refusal (with its
// code) and intakeerror.recorded is written. The row keeps its error code.
func (s *Postgres) RecordReplayFailure(ctx context.Context, id, code, message string, now time.Time) (IntakeError, error) {
	p, _ := tenant.FromContext(ctx)
	var out IntakeError
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := loadIntakeError(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if e.Status != IntakeErrorOpen {
			return problem(http.StatusConflict, CodeIntakeErrorResolved, "the intake error is already resolved")
		}
		if _, err := tx.Exec(ctx, `update intake_error set attempts = attempts + 1, replay_count = replay_count + 1, last_seen_at = $3,
			message = $4, version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, id, now, truncate(code+": "+message, maxIntakeMessage)); err != nil {
			return err
		}
		if out, err = loadIntakeError(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "intakeerror", id, salesevents.IntakeErrorRecorded, p.TenantID, intakeErrorData(out, ""))
	})
	return out, err
}

// ResolveReplayed closes an OPEN refusal whose replay stored (or found) the
// order orderID. The request body is cleared with the resolution, and
// intakeerror.resolved is written in the same transaction. A row another
// caller already resolved is returned unchanged.
func (s *Postgres) ResolveReplayed(ctx context.Context, id, orderID, orderNumber, actor string, now time.Time) (IntakeError, error) {
	p, _ := tenant.FromContext(ctx)
	var out IntakeError
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		e, err := loadIntakeError(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if e.Status != IntakeErrorOpen {
			out = e
			return nil
		}
		if _, err := tx.Exec(ctx, `update intake_error set status = 'RESOLVED', resolved_by = $3, resolved_at = $4, resolution_note = $5,
			replayed_order_id = $6, replay_count = replay_count + 1, request_payload = null, request_payload_sealed = null, idempotency_key = null,
			version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, id, actor, now, truncate("Replayed as order "+orderNumber, maxResolutionNoteLen), orderID); err != nil {
			return err
		}
		if out, err = loadIntakeError(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		return enqueue(ctx, tx, "intakeerror", id, salesevents.IntakeErrorResolved, p.TenantID, intakeErrorData(out, actor))
	})
	return out, err
}

const maxResolutionNoteLen = 500
