package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/events"
)

// MinAdhocRetentionDays is the lowest retention of ad-hoc runs the
// configuration allows (REPORTING_RETENTION_ADHOC_DAYS is 7 to 730); the delete
// guard of migration 0020 refuses younger runs whatever the caller asks.
const MinAdhocRetentionDays = 7

// RunSweep is the record of one pass of the sweep over the ad-hoc statement
// runs of one legal entity (FR-15.8, PRD §2.15.14 reporting.retention_adhoc_days).
type RunSweep struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	SweptAt       time.Time `json:"sweptAt"`
	Cutoff        time.Time `json:"cutoff"`
	RetentionDays int       `json:"retentionDays"`
	DeletedRuns   int       `json:"deletedRuns"`
	DeletedCells  int64     `json:"deletedCells"`
	HeldRuns      int       `json:"heldRuns"`
	HoldID        string    `json:"holdId,omitempty"`
	Actor         string    `json:"actor"`
}

// ListRunSweeps returns the recorded run sweep passes, newest first.
func (s *Postgres) ListRunSweeps(ctx context.Context, limit int) ([]RunSweep, error) {
	if limit < 1 || limit > MaxSweepPage {
		limit = MaxSweepPage
	}
	out := []RunSweep{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, legal_entity_id::text, swept_at, cutoff, retention_days, deleted_runs, deleted_cells,
				held_runs, coalesce(hold_id::text, ''), actor
			from report_run_sweep order by swept_at desc, id desc limit $1`, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w RunSweep
			if err := rows.Scan(&w.ID, &w.LegalEntityID, &w.SweptAt, &w.Cutoff, &w.RetentionDays, &w.DeletedRuns, &w.DeletedCells,
				&w.HeldRuns, &w.HoldID, &w.Actor); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// adhocRuns selects the ad-hoc runs of one legal entity that started before the
// cutoff: runs no schedule firing and no library file refers to. A consolidated
// run belongs to the first of its entities, but is held when any of them is.
const adhocRuns = `select r.id from statement_run r
	where coalesce(r.legal_entity_id, r.legal_entity_ids[1]) = $1::uuid and r.started_at < $2
	  and not exists (select 1 from report_schedule_firing f where f.tenant_id = r.tenant_id and f.run_id = r.id)
	  and not exists (select 1 from report_library_entry e where e.tenant_id = r.tenant_id and e.run_id = r.id)`

// heldRuns narrows adhocRuns to the runs one of whose entities has an active hold.
const heldRuns = `select r.id from statement_run r
	where r.id in (` + adhocRuns + `)
	  and exists (select 1 from report_legal_hold h
	    where h.tenant_id = r.tenant_id and h.released_at is null
	      and (h.legal_entity_id = r.legal_entity_id or h.legal_entity_id = any(coalesce(r.legal_entity_ids, '{}'))))`

// SweepRuns deletes the ad-hoc statement runs older than retentionDays, with
// their cells, entity by entity (FR-15.8, PRD retention_adhoc_days). An ad-hoc
// run is one that no schedule firing and no library file refers to; scheduled
// runs stay as long as their firing record exists. A run any of whose legal
// entities is under an active legal hold is kept, and the pass records how many
// the hold kept. Runs without a legal entity are never deleted (no hold could
// cover them). Each entity is one transaction that takes the hold lock of every
// entity its candidate runs belong to (report_legal_hold_lock, as the library
// sweep and hold placement do), deletes the runs, records the pass, writes the audit
// entry and runs.swept.v1; a pass that found nothing writes nothing. The cutoff
// is the earlier of now and the database clock, minus retentionDays of 24 hours each. Returns the recorded passes.
func (s *Postgres) SweepRuns(ctx context.Context, now time.Time, retentionDays int) ([]RunSweep, error) {
	if retentionDays < MinAdhocRetentionDays {
		return nil, fmt.Errorf("ad-hoc run retention is %d days, at least %d are required", retentionDays, MinAdhocRetentionDays)
	}
	var cutoff time.Time
	var entities []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select least($1::timestamptz, now()) - make_interval(hours => $2 * 24)`, now, retentionDays).Scan(&cutoff); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select distinct coalesce(legal_entity_id, legal_entity_ids[1])::text from statement_run
			where started_at < $1 and coalesce(legal_entity_id, legal_entity_ids[1]) is not null order by 1`, cutoff)
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
	var out []RunSweep
	for _, entity := range entities {
		var w RunSweep
		var wrote bool
		err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
			w, wrote = RunSweep{}, false
			// Take the hold lock of every entity the candidate runs belong to,
			// in uuid order (the order the delete guard takes them in), before
			// reading the holds: a hold placed on a second entity of a
			// consolidated run is then either committed and seen, or waits for
			// this pass; and two passes over overlapping consolidated runs
			// cannot deadlock by locking the same entities in opposite orders.
			rows, err := tx.Query(ctx, `select e::text from (
					select unnest(coalesce(r.legal_entity_ids, '{}') || array[r.legal_entity_id]) as e
					from statement_run r where r.id in (`+adhocRuns+`)
					union select $1::uuid) x
				where e is not null order by e`, entity, cutoff)
			if err != nil {
				return err
			}
			locks, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, e := range locks {
				if _, err := tx.Exec(ctx, `select report_legal_hold_lock($1::uuid, $2::uuid)`, tenantID(ctx), e); err != nil {
					return err
				}
			}
			w = RunSweep{ID: ids.New(), LegalEntityID: entity, Cutoff: cutoff.UTC(), RetentionDays: retentionDays, Actor: SweepActor}
			var holdID *string
			err = tx.QueryRow(ctx, `select h.id::text from report_legal_hold h, statement_run r
				where h.released_at is null and r.id in (`+heldRuns+`)
				  and (h.legal_entity_id = r.legal_entity_id or h.legal_entity_id = any(coalesce(r.legal_entity_ids, '{}')))
				order by (h.legal_entity_id = $1::uuid) desc, h.id limit 1`, entity, cutoff).Scan(&holdID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if holdID != nil {
				w.HoldID = *holdID
			}
			if err := tx.QueryRow(ctx, `select count(*) from (`+heldRuns+`) h`, entity, cutoff).Scan(&w.HeldRuns); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `with gone as (
					delete from statement_run where id in (`+adhocRuns+` except `+heldRuns+`) returning cell_count)
				select count(*), coalesce(sum(cell_count), 0)::bigint from gone`, entity, cutoff).Scan(&w.DeletedRuns, &w.DeletedCells); err != nil {
				return err
			}
			if w.DeletedRuns == 0 && w.HeldRuns == 0 {
				return nil
			}
			wrote = true
			if _, err := tx.Exec(ctx, `insert into report_run_sweep(tenant_id, id, legal_entity_id, cutoff, retention_days, deleted_runs,
					deleted_cells, held_runs, hold_id, actor)
				values($1,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,nullif($9,'')::uuid,$10)`,
				tenantID(ctx), w.ID, entity, w.Cutoff, w.RetentionDays, w.DeletedRuns, w.DeletedCells, w.HeldRuns, w.HoldID, w.Actor); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `select swept_at from report_run_sweep where id=$1::uuid`, w.ID).Scan(&w.SweptAt); err != nil {
				return err
			}
			if err := recordAuditTx(ctx, tx, domain.AuditEntry{Kind: domain.AuditRunsSwept, ActorID: w.Actor, ObjectRef: "run_sweep:" + w.ID,
				Details: map[string]any{"sweepId": w.ID, "legalEntityId": entity, "deletedRuns": w.DeletedRuns, "deletedCells": w.DeletedCells,
					"heldRuns": w.HeldRuns, "holdId": w.HoldID, "retentionDays": w.RetentionDays, "cutoff": w.Cutoff.Format(time.RFC3339)}}); err != nil {
				return err
			}
			return enqueue(ctx, tx, "runs", w.ID, events.RunsSwept, tenantID(ctx), events.RunsSweptData{
				SweepID: w.ID, LegalEntityID: entity, Cutoff: w.Cutoff.Format(time.RFC3339), RetentionDays: w.RetentionDays,
				DeletedRuns: w.DeletedRuns, DeletedCells: w.DeletedCells, HeldRuns: w.HeldRuns, HoldID: w.HoldID, Actor: w.Actor})
		})
		if err != nil {
			return out, fmt.Errorf("sweep runs of legal entity %s: %w", entity, err)
		}
		if wrote {
			out = append(out, w)
		}
	}
	return out, nil
}
