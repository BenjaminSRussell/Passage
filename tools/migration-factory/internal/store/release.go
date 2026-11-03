package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// Release is a request to correct and release one quarantined record of a run (FR-49.11).
// Parties are master-data ids known now, checked together with the run's snapshot.
// ExpectedVersion 0 skips the optimistic version check.
type Release struct {
	RunID           string
	Seq             int
	Fix             domain.Correction
	Parties         []string
	ExpectedVersion int
	By              string
}

// ReleaseQuarantined loads one corrected quarantined record under the run's row lock: it writes
// the record's legacy_xref row, marks the quarantine row RELEASED with the corrected record,
// updates the run's counts, totals and version, and writes pack.reconciled when the release
// completes the reconciliation. A record already released returns the run unchanged with
// replayed=true (idempotent by record).
func (s *Postgres) ReleaseQuarantined(ctx context.Context, idem Idem, rq Release) (domain.PackRun, bool, error) {
	var out domain.PackRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		var already bool
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.PackRun, error) {
			run, done, err := release(ctx, tx, rq)
			already = done
			return run, err
		})
		replayed = replayed || already
		return err
	})
	return out, replayed, err
}

func release(ctx context.Context, tx pgx.Tx, rq Release) (domain.PackRun, bool, error) {
	run, err := getRun(ctx, tx, rq.RunID, true)
	if err != nil {
		return domain.PackRun{}, false, err
	}
	idx := -1
	for i, q := range run.Quarantined {
		if q.Seq == rq.Seq {
			idx = i
		}
	}
	if idx < 0 {
		return domain.PackRun{}, false, fmt.Errorf("%w: run has no quarantined record %d", ErrNotFound, rq.Seq)
	}
	q := run.Quarantined[idx]
	if q.Status == domain.QuarantineReleased {
		return run, true, nil
	}
	if rq.ExpectedVersion != 0 && rq.ExpectedVersion != run.Version {
		return domain.PackRun{}, false, fmt.Errorf("%w: run is at version %d, request expected %d", ErrVersion, run.Version, rq.ExpectedVersion)
	}
	parties := make(map[string]struct{}, len(run.Parties)+len(rq.Parties))
	for _, p := range append(append([]string{}, run.Parties...), rq.Parties...) {
		parties[p] = struct{}{}
	}
	before := len(run.Totals)
	original := append([]domain.ControlTotal(nil), run.Totals...)
	rec, err := domain.ReleaseOpenItem(&run, q.Record, rq.Fix, parties)
	if err != nil {
		return domain.PackRun{}, false, err
	}

	sys, company := string(run.SourceSystem), run.SourceCompany
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('xref|' || current_setting('passage.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`,
		sys, company, domain.TableOpenItems); err != nil {
		return domain.PackRun{}, false, fmt.Errorf("lock legacy_xref: %w", err)
	}
	tag, err := tx.Exec(ctx, `insert into legacy_xref (tenant_id, xref_id, source_system, source_company, source_table, source_id,
		passage_entity, passage_id, run_id, amount, currency)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, 'open_item', $6, $7, $8, $9)
		on conflict do nothing`,
		ids.New(), sys, company, domain.OpenItemsTable(rec.Side()), rec.LegacyID, ids.New(), run.RunID, rec.Amount.String(), rec.Currency)
	if err != nil {
		return domain.PackRun{}, false, fmt.Errorf("insert legacy_xref: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.PackRun{}, false, fmt.Errorf("%w: legacy id %s is already loaded by cross-reference; it cannot be released again", domain.ErrTransition, rec.LegacyID)
	}

	now := time.Now().UTC()
	released, err := json.Marshal(recordJSON(rec))
	if err != nil {
		return domain.PackRun{}, false, err
	}
	if _, err := tx.Exec(ctx, `update pack_quarantine set status = $3, released_record = $4, released_by = $5, released_at = $6
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and seq = $2`,
		run.RunID, q.Seq, domain.QuarantineReleased, released, rq.By, now); err != nil {
		return domain.PackRun{}, false, fmt.Errorf("update pack_quarantine: %w", err)
	}
	q.Status, q.Released, q.ReleasedBy, q.ReleasedAt = domain.QuarantineReleased, &rec, rq.By, &now
	run.Quarantined[idx] = q

	for i, t := range run.Totals[:before] {
		if t.Source.Equal(original[i].Source) && t.Target.Equal(original[i].Target) {
			continue
		}
		tag, err := tx.Exec(ctx, `update control_total set source_value = $3, target_value = $4, delta = $5, status = $6, computed_at = now()
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1 and seq = $2 and name = $7`,
			run.RunID, i+1, t.Source.String(), t.Target.String(), t.Delta.String(), string(t.Status), t.Name)
		if err != nil {
			return domain.PackRun{}, false, fmt.Errorf("update control_total: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return domain.PackRun{}, false, fmt.Errorf("update control_total %d of run %s: %d rows", i+1, run.RunID, tag.RowsAffected())
		}
	}
	if err := insertTotals(ctx, tx, run.RunID, run.Totals[before:], before); err != nil {
		return domain.PackRun{}, false, err
	}

	run.Version++
	if _, err := tx.Exec(ctx, `update pack_run set status = $2, records_loaded = $3, records_quarantined = $4, version = $5
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1`,
		run.RunID, string(run.Status), run.RecordsLoaded, run.RecordsQuarantined, run.Version); err != nil {
		return domain.PackRun{}, false, fmt.Errorf("update pack_run: %w", err)
	}
	if run.Status == domain.StatusReconciled {
		if err := enqueueReconciled(ctx, tx, run); err != nil {
			return domain.PackRun{}, false, err
		}
	}
	return run, false, nil
}
