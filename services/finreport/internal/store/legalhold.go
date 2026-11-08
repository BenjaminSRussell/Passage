package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// Problem codes of the report library retention rules (slice 17). They are
// slice-local, not yet in the PRD catalog.
const (
	CodeHoldActive      = "REPORT-0045" // the entity's library is already under legal hold
	CodeHoldReleased    = "REPORT-0046" // the hold was released already
	CodeHoldNotFound    = "REPORT-0047"
	CodeHoldOutOfScope  = "REPORT-0048" // the legal entity is outside the caller's scope
	CodeHoldReasonShape = "REPORT-0049" // the reason is empty or too long
)

// MaxHoldReason is the longest hold reason or release note.
const MaxHoldReason = 500

// SweepActor is the in-process system actor of the library retention sweep.
const SweepActor = "system:finreport-library-sweep"

// LegalHold is a hold on the report library of one legal entity (FR-15.8).
type LegalHold struct {
	ID            string     `json:"id"`
	LegalEntityID string     `json:"legalEntityId"`
	Reason        string     `json:"reason"`
	PlacedBy      string     `json:"placedBy"`
	PlacedAt      time.Time  `json:"placedAt"`
	Active        bool       `json:"active"`
	ReleasedBy    string     `json:"releasedBy,omitempty"`
	ReleasedAt    *time.Time `json:"releasedAt,omitempty"`
	ReleaseNote   string     `json:"releaseNote,omitempty"`
	Revision      int        `json:"revision"`
	// Replayed is true when a repeated Idempotency-Key returned the stored hold.
	Replayed bool `json:"-"`
}

// HoldSpec is a request to place a hold.
type HoldSpec struct {
	LegalEntityID  string
	Reason         string
	Actor          string
	IdempotencyKey string
	RequestHash    string
}

const holdColumns = `id::text, legal_entity_id::text, reason, placed_by, placed_at, released_at is null, coalesce(released_by, ''),
	released_at, release_note, revision`

func scanHold(row pgx.Row) (LegalHold, error) {
	var h LegalHold
	err := row.Scan(&h.ID, &h.LegalEntityID, &h.Reason, &h.PlacedBy, &h.PlacedAt, &h.Active, &h.ReleasedBy, &h.ReleasedAt, &h.ReleaseNote, &h.Revision)
	return h, err
}

// PlaceHold puts the report library of a legal entity under legal hold. One
// hold can be active per entity (REPORT-0045); the key makes a retry return
// the stored hold, and a different request under the same key is refused. The
// hold, its audit entry and legalhold.placed.v1 are written in one transaction.
func (s *Postgres) PlaceHold(ctx context.Context, spec HoldSpec) (LegalHold, error) {
	entity, ok := ids.Canonical(spec.LegalEntityID)
	if !ok || entity == ids.Nil {
		return LegalHold{}, problem(422, "COM-0002", "legalEntityId must be a UUID")
	}
	reason := strings.TrimSpace(spec.Reason)
	if reason == "" || len(reason) > MaxHoldReason {
		return LegalHold{}, problem(422, CodeHoldReasonShape, fmt.Sprintf("reason is required and at most %d characters", MaxHoldReason))
	}
	p, _ := tenant.FromContext(ctx)
	if !tenant.InLegalEntityScope(p, entity) {
		return LegalHold{}, problem(403, CodeHoldOutOfScope, "legal entity "+entity+" is outside your legal-entity scope")
	}
	var out LegalHold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var hash string
		err := tx.QueryRow(ctx, `select request_hash from report_legal_hold where idempotency_key=$1`, spec.IdempotencyKey).Scan(&hash)
		switch {
		case err == nil:
			if hash != spec.RequestHash {
				return problem(422, CodeIdempotencyReuse, "Idempotency-Key was used for a different request")
			}
			out, err = scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from report_legal_hold where idempotency_key=$1`, spec.IdempotencyKey))
			out.Replayed = true
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		id := ids.New()
		tag, err := tx.Exec(ctx, `insert into report_legal_hold(tenant_id, id, legal_entity_id, reason, placed_by, idempotency_key, request_hash)
			values($1,$2,$3::uuid,$4,$5,$6,$7)
			on conflict do nothing`, tenantID(ctx), id, entity, reason, spec.Actor, spec.IdempotencyKey, spec.RequestHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// The unique key is the active hold of the entity (or a concurrent request with the same key).
			var active bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from report_legal_hold where legal_entity_id=$1::uuid and released_at is null)`, entity).Scan(&active); err != nil {
				return err
			}
			if active {
				return problem(409, CodeHoldActive, "the report library of legal entity "+entity+" is already under legal hold")
			}
			return problem(409, CodeIdempotencyInFlight, "the same Idempotency-Key is being processed; retry")
		}
		if out, err = scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from report_legal_hold where id=$1::uuid`, id)); err != nil {
			return err
		}
		if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditLegalHoldPlaced, ActorID: spec.Actor, ObjectRef: "legal_hold:" + id,
			Details: map[string]any{"holdId": id, "legalEntityId": entity, "reason": reason}}); err != nil {
			return err
		}
		return enqueue(ctx, tx, "legalhold", id, events.LegalHoldPlaced, tenantID(ctx), events.LegalHoldData{
			HoldID: id, LegalEntityID: entity, Reason: reason, Actor: spec.Actor})
	})
	return out, err
}

// ReleaseHold ends a hold. ifMatch is the revision read (412 when stale, 428
// when missing); a hold released already is refused (REPORT-0046). The release,
// its audit entry and legalhold.released.v1 are written in one transaction.
func (s *Postgres) ReleaseHold(ctx context.Context, id string, ifMatch *int, note, actor string) (LegalHold, error) {
	note = strings.TrimSpace(note)
	if len(note) > MaxHoldReason {
		return LegalHold{}, problem(422, CodeHoldReasonShape, fmt.Sprintf("note is at most %d characters", MaxHoldReason))
	}
	var out LegalHold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from report_legal_hold where id=$1::uuid for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeHoldNotFound, "legal hold "+id+" does not exist")
		}
		if err != nil {
			return err
		}
		if !h.Active {
			return problem(409, CodeHoldReleased, "legal hold "+id+" was released already")
		}
		if ifMatch == nil {
			return problem(428, "precondition_required", "If-Match is required to release legal hold "+id)
		}
		if *ifMatch != h.Revision {
			return problem(412, CodeRevisionConflict, fmt.Sprintf("legal hold revision is %d, not %d", h.Revision, *ifMatch))
		}
		if _, err := tx.Exec(ctx, `update report_legal_hold set released_at=now(), released_by=$2, release_note=$3, revision=revision+1
			where id=$1::uuid`, id, actor, note); err != nil {
			return err
		}
		if out, err = scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from report_legal_hold where id=$1::uuid`, id)); err != nil {
			return err
		}
		if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditLegalHoldReleased, ActorID: actor, ObjectRef: "legal_hold:" + id,
			Details: map[string]any{"holdId": id, "legalEntityId": h.LegalEntityID, "note": note}}); err != nil {
			return err
		}
		return enqueue(ctx, tx, "legalhold", id, events.LegalHoldReleased, tenantID(ctx), events.LegalHoldData{
			HoldID: id, LegalEntityID: h.LegalEntityID, Reason: h.Reason, Actor: actor, Note: note})
	})
	return out, err
}

// ListHolds returns the legal holds the caller's scope sees, newest first; with
// activeOnly, only those still in force.
func (s *Postgres) ListHolds(ctx context.Context, activeOnly bool) ([]LegalHold, error) {
	out := []LegalHold{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+holdColumns+` from report_legal_hold
			where (not $1 or released_at is null) order by placed_at desc, id desc limit 500`, activeOnly)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			h, err := scanHold(rows)
			if err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// GetHold returns one hold.
func (s *Postgres) GetHold(ctx context.Context, id string) (LegalHold, error) {
	var h LegalHold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		h, err = scanHold(tx.QueryRow(ctx, `select `+holdColumns+` from report_legal_hold where id=$1::uuid`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeHoldNotFound, "legal hold "+id+" does not exist")
		}
		return err
	})
	return h, err
}

// Sweep is the record of one pass of the retention sweep over one legal entity.
type Sweep struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	SweptAt       time.Time `json:"sweptAt"`
	Cutoff        time.Time `json:"cutoff"`
	DeletedCount  int       `json:"deletedCount"`
	DeletedBytes  int64     `json:"deletedBytes"`
	HeldCount     int       `json:"heldCount"`
	HoldID        string    `json:"holdId,omitempty"`
	Actor         string    `json:"actor"`
}

// MaxSweepPage is the most sweeps one list call returns.
const MaxSweepPage = 200

// ListSweeps returns the recorded sweep passes, newest first.
func (s *Postgres) ListSweeps(ctx context.Context, limit int) ([]Sweep, error) {
	if limit < 1 || limit > MaxSweepPage {
		limit = MaxSweepPage
	}
	out := []Sweep{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, swept_at, cutoff, deleted_count, deleted_bytes, held_count,
				coalesce(hold_id::text, ''), actor
			from report_library_sweep order by swept_at desc, id desc limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w Sweep
			if err := rows.Scan(&w.ID, &w.LegalEntityID, &w.SweptAt, &w.Cutoff, &w.DeletedCount, &w.DeletedBytes, &w.HeldCount, &w.HoldID, &w.Actor); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// SweepLibrary deletes the report library files whose retention date is at or
// before now, entity by entity (PRD §2.15.9 `library-retention-sweep`). An
// entity under an active legal hold loses nothing: the pass records how many
// files the hold kept. Each entity is one transaction that takes the entity's
// hold lock (so a hold being placed at the same time is either seen or waits
// for the sweep to commit), deletes the files, records the pass, writes the
// audit entry and library.swept.v1; a pass that found nothing past retention
// writes nothing. Files without a legal entity are never deleted (no hold
// could cover them). The cutoff is the earlier of now and the database clock,
// so a job clock running ahead never asks the delete guard for a file it
// would refuse. Returns the recorded passes.
func (s *Postgres) SweepLibrary(ctx context.Context, now time.Time) ([]Sweep, error) {
	var entities []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select least($1::timestamptz, now())`, now).Scan(&now); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from report_library_entry
			where retain_until <= $1 and legal_entity_id is not null order by 1`, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e string
			if err := rows.Scan(&e); err != nil {
				return err
			}
			entities = append(entities, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	var out []Sweep
	for _, entity := range entities {
		var w Sweep
		var wrote bool
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			w, wrote = Sweep{}, false
			if _, err := tx.Exec(ctx, `select report_legal_hold_lock($1::uuid, $2::uuid)`, tenantID(ctx), entity); err != nil {
				return err
			}
			var holdID *string
			err := tx.QueryRow(ctx, `select id::text from report_legal_hold where legal_entity_id=$1::uuid and released_at is null`, entity).Scan(&holdID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			w = Sweep{ID: ids.New(), LegalEntityID: entity, Cutoff: now.UTC(), Actor: SweepActor}
			if holdID != nil {
				w.HoldID = *holdID
				err = tx.QueryRow(ctx, `select count(*) from report_library_entry where legal_entity_id=$1::uuid and retain_until <= $2`, entity, now).Scan(&w.HeldCount)
			} else {
				err = tx.QueryRow(ctx, `with gone as (
						delete from report_library_entry where legal_entity_id=$1::uuid and retain_until <= $2 returning size_bytes)
					select count(*), coalesce(sum(size_bytes), 0)::bigint from gone`, entity, now).Scan(&w.DeletedCount, &w.DeletedBytes)
			}
			if err != nil {
				return err
			}
			if w.DeletedCount == 0 && w.HeldCount == 0 {
				return nil
			}
			wrote = true
			if _, err := tx.Exec(ctx, `insert into report_library_sweep(tenant_id, id, legal_entity_id, cutoff, deleted_count, deleted_bytes,
					held_count, hold_id, actor)
				values($1,$2::uuid,$3::uuid,$4,$5,$6,$7,nullif($8,'')::uuid,$9)`,
				tenantID(ctx), w.ID, entity, w.Cutoff, w.DeletedCount, w.DeletedBytes, w.HeldCount, w.HoldID, w.Actor); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `select swept_at from report_library_sweep where id=$1::uuid`, w.ID).Scan(&w.SweptAt); err != nil {
				return err
			}
			if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditLibrarySwept, ActorID: w.Actor, ObjectRef: "library_sweep:" + w.ID,
				Details: map[string]any{"sweepId": w.ID, "legalEntityId": entity, "deleted": w.DeletedCount, "deletedBytes": w.DeletedBytes,
					"held": w.HeldCount, "holdId": w.HoldID, "cutoff": w.Cutoff.Format(time.RFC3339)}}); err != nil {
				return err
			}
			return enqueue(ctx, tx, "library", w.ID, events.LibrarySwept, tenantID(ctx), events.LibrarySweptData{
				SweepID: w.ID, LegalEntityID: entity, Cutoff: w.Cutoff.Format(time.RFC3339), DeletedCount: w.DeletedCount,
				DeletedBytes: w.DeletedBytes, HeldCount: w.HeldCount, HoldID: w.HoldID, Actor: w.Actor})
		})
		if err != nil {
			return out, fmt.Errorf("sweep legal entity %s: %w", entity, err)
		}
		if wrote {
			out = append(out, w)
		}
	}
	return out, nil
}
