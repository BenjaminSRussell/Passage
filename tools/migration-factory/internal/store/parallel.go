package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// ErrParallelRunExists is returned when a parallel run for the period and source system already
// exists with different input (starting is idempotent by (period, system), 9.49.5).
var ErrParallelRunExists = errors.New("a parallel run for this period and source system already exists with different input")

// StartParallelRun stores a diffed run with its variances and writes parallelrun.diffed in one
// transaction. A run that already exists for (period, system) with the same input hash is
// returned with replayed=true; with another hash it fails with ErrParallelRunExists.
func (s *Postgres) StartParallelRun(ctx context.Context, idem Idem, run domain.ParallelRun) (domain.ParallelRun, bool, error) {
	var out domain.ParallelRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		var existed bool
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.ParallelRun, error) {
			var r domain.ParallelRun
			var err error
			r, existed, err = startParallelRun(ctx, tx, run)
			return r, err
		})
		replayed = replayed || existed
		return err
	})
	return out, replayed, err
}

func startParallelRun(ctx context.Context, tx pgx.Tx, run domain.ParallelRun) (domain.ParallelRun, bool, error) {
	var created time.Time
	err := tx.QueryRow(ctx, `insert into parallel_run (tenant_id, parallel_run_id, period, source_system, documents_replayed, revenue,
		explained_max_pct, input_hash, state, unexplained_count, unexplained_amount, explained_amount, started_by, version)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		on conflict (tenant_id, period, source_system) do nothing returning created_at`,
		run.ID, run.Period, string(run.SourceSystem), run.DocumentsReplayed, run.Revenue.String(), run.ExplainedMaxPct.String(),
		run.InputHash, string(run.State), run.Summary.UnexplainedCount, run.Summary.UnexplainedAmount.String(),
		run.Summary.ExplainedAmount.String(), run.StartedBy, run.Version).Scan(&created)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err := getParallelRunBy(ctx, tx, `period = $1 and source_system = $2`, false, run.Period, string(run.SourceSystem))
		if err != nil {
			return domain.ParallelRun{}, false, err
		}
		if existing.InputHash != run.InputHash {
			return domain.ParallelRun{}, false, fmt.Errorf("%w: run %s for %s %s", ErrParallelRunExists, existing.ID, existing.Period, existing.SourceSystem)
		}
		return existing, true, nil
	}
	if err != nil {
		return domain.ParallelRun{}, false, fmt.Errorf("insert parallel_run: %w", err)
	}
	run.CreatedAt = created
	if n := len(run.Variances); n > 0 {
		vids, seqs, areas, keys := make([]string, n), make([]int32, n), make([]string, n), make([]string, n)
		legacy, passage, delta := make([]string, n), make([]string, n), make([]string, n)
		classes, rules := make([]string, n), make([]string, n)
		for i, v := range run.Variances {
			vids[i], seqs[i], areas[i], keys[i] = v.ID, int32(i+1), string(v.Area), v.Key
			legacy[i], passage[i], delta[i] = v.Legacy.String(), v.Passage.String(), v.Delta.String()
			classes[i], rules[i] = v.Class, v.RuleID
		}
		tag, err := tx.Exec(ctx, `insert into variance (tenant_id, variance_id, parallel_run_id, seq, area, key_text, legacy_value, passage_value, delta, class, rule_id)
			select current_setting('passage.tenant_id', true)::uuid, v, $1, s, a, k, l, p, d, c, r
			from unnest($2::uuid[], $3::int[], $4::text[], $5::text[], $6::numeric[], $7::numeric[], $8::numeric[], $9::text[], $10::text[]) as t(v, s, a, k, l, p, d, c, r)`,
			run.ID, vids, seqs, areas, keys, legacy, passage, delta, classes, rules)
		if err != nil {
			return domain.ParallelRun{}, false, fmt.Errorf("insert variance: %w", err)
		}
		if int(tag.RowsAffected()) != n {
			return domain.ParallelRun{}, false, fmt.Errorf("insert variance: %d of %d rows", tag.RowsAffected(), n)
		}
	}
	if err := enqueue(ctx, tx, mfevents.ParallelRunDiffed, "parallel_run", run.ID, mfevents.ParallelRunDiffedData{
		ParallelRunID: run.ID, Period: run.Period, SourceSystem: string(run.SourceSystem), VarianceCount: len(run.Variances),
		UnexplainedCount: run.Summary.UnexplainedCount, UnexplainedAmount: run.Summary.UnexplainedAmount.StringFixed(6),
	}); err != nil {
		return domain.ParallelRun{}, false, fmt.Errorf("enqueue parallelrun.diffed: %w", err)
	}
	return run, false, nil
}

const parallelColumns = `parallel_run_id::text, period, source_system, documents_replayed, revenue::text, explained_max_pct::text, input_hash,
	state, unexplained_count, unexplained_amount::text, explained_amount::text, started_by, signed_off_by, signed_off_at, version, created_at`

func scanParallel(row pgx.Row) (domain.ParallelRun, error) {
	var p domain.ParallelRun
	var sys, state, revenue, pct, unexplained, explained string
	if err := row.Scan(&p.ID, &p.Period, &sys, &p.DocumentsReplayed, &revenue, &pct, &p.InputHash, &state, &p.Summary.UnexplainedCount,
		&unexplained, &explained, &p.StartedBy, &p.SignedOffBy, &p.SignedOffAt, &p.Version, &p.CreatedAt); err != nil {
		return domain.ParallelRun{}, err
	}
	p.SourceSystem, p.State = domain.SourceSystem(sys), domain.ParallelState(state)
	p.Revenue, p.ExplainedMaxPct = decimal.RequireFromString(revenue), decimal.RequireFromString(pct)
	p.Summary.UnexplainedAmount, p.Summary.ExplainedAmount = decimal.RequireFromString(unexplained), decimal.RequireFromString(explained)
	return p, nil
}

// getParallelRunBy reads one run matching where (with its variances); forUpdate locks the row.
func getParallelRunBy(ctx context.Context, tx pgx.Tx, where string, forUpdate bool, args ...any) (domain.ParallelRun, error) {
	q := `select ` + parallelColumns + ` from parallel_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and ` + where
	if forUpdate {
		q += ` for update`
	}
	run, err := scanParallel(tx.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ParallelRun{}, ErrNotFound
	}
	if err != nil {
		return domain.ParallelRun{}, err
	}
	rows, err := tx.Query(ctx, `select variance_id::text, area, key_text, legacy_value::text, passage_value::text, delta::text, class, rule_id,
		reason, explained_by, explained_at from variance
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and parallel_run_id = $1 order by seq`, run.ID)
	if err != nil {
		return domain.ParallelRun{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var v domain.ParallelVariance
		var area, l, p, d string
		if err := rows.Scan(&v.ID, &area, &v.Key, &l, &p, &d, &v.Class, &v.RuleID, &v.Reason, &v.ExplainedBy, &v.ExplainedAt); err != nil {
			return domain.ParallelRun{}, err
		}
		v.Area = domain.Area(area)
		v.Legacy, v.Passage, v.Delta = decimal.RequireFromString(l), decimal.RequireFromString(p), decimal.RequireFromString(d)
		run.Variances = append(run.Variances, v)
	}
	if err := rows.Err(); err != nil {
		return domain.ParallelRun{}, err
	}
	run.Summarize()
	return run, nil
}

// GetParallelRun returns a run with its variances.
func (s *Postgres) GetParallelRun(ctx context.Context, id string) (domain.ParallelRun, error) {
	var run domain.ParallelRun
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		run, err = getParallelRunBy(ctx, tx, `parallel_run_id = $1`, false, id)
		return err
	})
	return run, err
}

// ParallelFilter narrows ListParallelRuns. After is the id of the last row of the previous page.
type ParallelFilter struct {
	State string
	After string
	Limit int
}

// ListParallelRuns returns runs newest first, without variances.
func (s *Postgres) ListParallelRuns(ctx context.Context, f ParallelFilter) ([]domain.ParallelRun, error) {
	out := []domain.ParallelRun{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var after *time.Time
		var afterID *string
		if f.After != "" {
			var at time.Time
			err := tx.QueryRow(ctx, `select created_at from parallel_run where tenant_id = current_setting('passage.tenant_id', true)::uuid and parallel_run_id = $1`, f.After).Scan(&at)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			after, afterID = &at, &f.After
		}
		rows, err := tx.Query(ctx, `select `+parallelColumns+` from parallel_run
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and ($1 = '' or state = $1)
			  and ($2::timestamptz is null or (created_at, parallel_run_id) < ($2, $3::uuid))
			order by created_at desc, parallel_run_id desc limit $4`, f.State, after, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanParallel(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// ExplainVariance is a request to attach a reason to one variance. ExpectedVersion 0 skips the
// optimistic version check.
type ExplainVariance struct {
	ParallelRunID   string
	VarianceID      string
	Reason          string
	ExpectedVersion int
	By              string
}

// ExplainVariance applies the explanation under the run's row lock and stores the variance and
// the run's new aggregate. Explaining a variance again with the same reason returns the run
// unchanged with replayed=true (idempotent by variance).
func (s *Postgres) ExplainVariance(ctx context.Context, idem Idem, e ExplainVariance) (domain.ParallelRun, bool, error) {
	return s.review(ctx, idem, e.ParallelRunID, e.ExpectedVersion, func(tx pgx.Tx, run *domain.ParallelRun) (bool, error) {
		at := time.Now().UTC()
		changed, err := run.Explain(e.VarianceID, e.Reason, e.By, at)
		if err != nil || !changed {
			return changed, err
		}
		for _, v := range run.Variances {
			if v.ID != e.VarianceID {
				continue
			}
			if _, err := tx.Exec(ctx, `update variance set class = $3, reason = $4, explained_by = $5, explained_at = $6
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and parallel_run_id = $1 and variance_id = $2`,
				run.ID, v.ID, v.Class, v.Reason, v.ExplainedBy, v.ExplainedAt); err != nil {
				return false, fmt.Errorf("update variance: %w", err)
			}
		}
		return true, nil
	})
}

// SignOff is a request to sign off a parallel run.
type SignOff struct {
	ParallelRunID   string
	ExpectedVersion int
	By              string
}

// SignOffParallelRun records the sign-off under the run's row lock (FR-49.5 step 6).
func (s *Postgres) SignOffParallelRun(ctx context.Context, idem Idem, so SignOff) (domain.ParallelRun, bool, error) {
	return s.review(ctx, idem, so.ParallelRunID, so.ExpectedVersion, func(_ pgx.Tx, run *domain.ParallelRun) (bool, error) {
		return true, run.SignOff(so.By, time.Now().UTC())
	})
}

// review locks a run, checks its version, applies fn and stores the run's state, aggregate and
// version when fn reports a change.
func (s *Postgres) review(ctx context.Context, idem Idem, id string, expected int, fn func(pgx.Tx, *domain.ParallelRun) (bool, error)) (domain.ParallelRun, bool, error) {
	var out domain.ParallelRun
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		unchanged := false
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.ParallelRun, error) {
			run, err := getParallelRunBy(ctx, tx, `parallel_run_id = $1`, true, id)
			if err != nil {
				return domain.ParallelRun{}, err
			}
			if expected != 0 && expected != run.Version {
				return domain.ParallelRun{}, fmt.Errorf("%w: parallel run is at version %d, request expected %d", ErrVersion, run.Version, expected)
			}
			changed, err := fn(tx, &run)
			if err != nil {
				return domain.ParallelRun{}, err
			}
			if !changed {
				unchanged = true
				return run, nil
			}
			if _, err := tx.Exec(ctx, `update parallel_run set state = $2, unexplained_count = $3, unexplained_amount = $4, explained_amount = $5,
				signed_off_by = $6, signed_off_at = $7, version = $8
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and parallel_run_id = $1`,
				run.ID, string(run.State), run.Summary.UnexplainedCount, run.Summary.UnexplainedAmount.String(), run.Summary.ExplainedAmount.String(),
				run.SignedOffBy, run.SignedOffAt, run.Version); err != nil {
				return domain.ParallelRun{}, fmt.Errorf("update parallel_run: %w", err)
			}
			return run, nil
		})
		replayed = replayed || unchanged
		return err
	})
	return out, replayed, err
}
