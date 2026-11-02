package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

const (
	eventSource   = "close"
	yearEndColumn = `id::text, legal_entity_id::text, ledger_id::text, fiscal_year, sequence, coalesce(supersedes_run_id::text, ''), status, currency, closing_date::text, opening_date::text, retained_earnings_account, retain_dimensions, proposals, journals, dispatched, created_by, request_hash`
)

func scanRun(row pgx.Row) (domain.YearEndRun, error) {
	var run domain.YearEndRun
	var proposals, journals []byte
	err := row.Scan(&run.ID, &run.LegalEntityID, &run.LedgerID, &run.FiscalYear, &run.Sequence, &run.SupersedesRunID, &run.Status, &run.Currency,
		&run.ClosingDate, &run.OpeningDate, &run.RetainedEarnings, &run.RetainDimensions, &proposals, &journals, &run.Dispatched, &run.CreatedBy, &run.RequestHash)
	if err != nil {
		return run, err
	}
	if proposals != nil {
		run.Proposals = &domain.YearEnd{}
		if err := json.Unmarshal(proposals, run.Proposals); err != nil {
			return run, err
		}
	}
	return run, json.Unmarshal(journals, &run.Journals)
}

func (s *Postgres) YearPeriods(ctx context.Context, legalEntityID, ledgerID, year string) ([]domain.Period, error) {
	var periods []domain.Period
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := tx.Query(ctx, `select fiscal_period_id::text, start_date, end_date, module, status from period_status
			where legal_entity_id = $1 and ledger_id = $2 and start_date >= make_date($3::int, 1, 1) and end_date <= make_date($3::int, 12, 31) order by start_date, fiscal_period_id, module`,
			legalEntityID, ledgerID, year)
		if err != nil {
			return err
		}
		defer found.Close()
		for found.Next() {
			var id, module, status string
			var start, end time.Time
			if err := found.Scan(&id, &start, &end, &module, &status); err != nil {
				return err
			}
			if n := len(periods); n == 0 || periods[n-1].FiscalPeriodID != id {
				periods = append(periods, domain.Period{FiscalPeriodID: id, Start: start, End: end})
			}
			last := &periods[len(periods)-1]
			last.Rows = append(last.Rows, domain.Row{Module: domain.Module(module), Status: domain.Status(status)})
		}
		return found.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read year periods: %w", err)
	}
	return periods, nil
}

func reuse(detail string) error {
	return &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: detail}
}

func (s *Postgres) BeginYearEnd(ctx context.Context, in domain.YearEndRun) (domain.YearEndRun, bool, error) {
	var run domain.YearEndRun
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, in.LedgerID+in.FiscalYear); err != nil {
			return err
		}
		var lastSequence int
		if err := tx.QueryRow(ctx, `select coalesce(max(sequence), 0) from year_end_run where ledger_id = $1 and fiscal_year = $2`, in.LedgerID, in.FiscalYear).Scan(&lastSequence); err != nil {
			return err
		}
		latest, err := scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where ledger_id = $1 and fiscal_year = $2 and status <> 'abandoned' order by sequence desc limit 1 for update`, in.LedgerID, in.FiscalYear))
		in.Sequence = lastSequence + 1
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if in.SupersedesRunID != "" {
				return reuse("supersedesRunId names a run that is not the latest of this ledger and year")
			}
		case err != nil:
			return err
		case latest.Status == domain.RunPending:
			if latest.RequestHash != in.RequestHash || latest.SupersedesRunID != in.SupersedesRunID {
				return reuse("an unfinished year-end run of this ledger and year exists with a different request")
			}
			run = latest
			return nil
		case in.SupersedesRunID == latest.ID:
		case in.SupersedesRunID == latest.SupersedesRunID && latest.RequestHash == in.RequestHash:
			run = latest
			return nil
		default:
			return reuse("a completed year-end run exists; name it in supersedesRunId to reverse and redo it")
		}
		created = true
		in.ID = ids.New()
		_, err = tx.Exec(ctx, `insert into year_end_run(tenant_id, id, legal_entity_id, ledger_id, fiscal_year, sequence, supersedes_run_id, request_hash, status, currency, closing_date, opening_date, retained_earnings_account, retain_dimensions, created_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, nullif($6, '')::uuid, $7, 'pending', $8, $9, $10, $11, $12, $13)`,
			in.ID, in.LegalEntityID, in.LedgerID, in.FiscalYear, in.Sequence, in.SupersedesRunID, in.RequestHash, in.Currency, in.ClosingDate, in.OpeningDate, in.RetainedEarnings, in.RetainDimensions, in.CreatedBy)
		if err != nil {
			return err
		}
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where id = $1`, in.ID))
		return err
	})
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			return domain.YearEndRun{}, false, err
		}
		return domain.YearEndRun{}, false, fmt.Errorf("begin year-end run: %w", err)
	}
	return run, created, nil
}

func (s *Postgres) DispatchYearEnd(ctx context.Context, runID string, proposals *domain.YearEnd) (domain.YearEndRun, error) {
	var encoded []byte
	if proposals != nil {
		var err error
		if encoded, err = json.Marshal(proposals); err != nil {
			return domain.YearEndRun{}, fmt.Errorf("encode year-end proposals: %w", err)
		}
	}
	return s.updateRun(ctx, runID, `update year_end_run set dispatched = true, proposals = coalesce(proposals, $2::jsonb) where id = $1 and status = 'pending'`, encoded)
}

func (s *Postgres) CompletedYearEnd(ctx context.Context, legalEntityID, ledgerID, year, requestHash, supersedesRunID string) (domain.YearEndRun, bool, error) {
	var run domain.YearEndRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where legal_entity_id = $1 and ledger_id = $2 and fiscal_year = $3 and status <> 'abandoned' order by sequence desc limit 1`,
			legalEntityID, ledgerID, year))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.YearEndRun{}, false, nil
	}
	if err != nil {
		return domain.YearEndRun{}, false, fmt.Errorf("read completed year-end run: %w", err)
	}
	return run, run.Status == domain.RunCompleted && run.RequestHash == requestHash && run.SupersedesRunID == supersedesRunID, nil
}

func (s *Postgres) AbandonYearEnd(ctx context.Context, runID string) (domain.YearEndRun, error) {
	return s.updateRun(ctx, runID, `update year_end_run set status = 'abandoned' where id = $1 and status = 'pending' and journals = '{}'::jsonb`)
}

func (s *Postgres) SetYearEndJournal(ctx context.Context, runID, name, journalID string) (domain.YearEndRun, error) {
	return s.updateRun(ctx, runID, `update year_end_run set journals = journals || jsonb_build_object($2::text, $3::text) where id = $1`, name, journalID)
}

func (s *Postgres) updateRun(ctx context.Context, runID, statement string, args ...any) (domain.YearEndRun, error) {
	var run domain.YearEndRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, statement, append([]any{runID}, args...)...); err != nil {
			return err
		}
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where id = $1`, runID))
		return err
	})
	if err != nil {
		return domain.YearEndRun{}, fmt.Errorf("update year-end run: %w", err)
	}
	return run, nil
}

func (s *Postgres) CompleteYearEnd(ctx context.Context, runID string) (domain.YearEndRun, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.YearEndRun{}, errors.New("complete year-end run: no principal in context")
	}
	var run domain.YearEndRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		updated, err := tx.Exec(ctx, `update year_end_run set status = 'completed', completed_at = now() where id = $1 and status = 'pending'`, runID)
		if err != nil {
			return err
		}
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where id = $1`, runID))
		if err != nil || updated.RowsAffected() == 0 {
			return err
		}
		evt, err := events.New(domain.EventYearEndCompleted, eventSource, run.ID, p.TenantID, domain.YearEndCompleted{
			RunID: run.ID, LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, FiscalYear: run.FiscalYear,
			ClosingJournalID: run.Journals[domain.JournalClosing],
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "year_end_run", run.ID, evt)
	})
	if err != nil {
		return domain.YearEndRun{}, fmt.Errorf("complete year-end run: %w", err)
	}
	return run, nil
}

func (s *Postgres) YearEndRun(ctx context.Context, runID string) (domain.YearEndRun, error) {
	var run domain.YearEndRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where id = $1`, runID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.YearEndRun{}, &domain.Error{Code: domain.CodeNoRun, Detail: "year-end run not found"}
	}
	if err != nil {
		return domain.YearEndRun{}, fmt.Errorf("read year-end run: %w", err)
	}
	return run, nil
}
