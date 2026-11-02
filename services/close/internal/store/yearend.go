package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
	yearEndColumn = `id::text, legal_entity_id::text, ledger_id::text, fiscal_year, status, currency, closing_date::text, opening_date::text, proposals, coalesce(closing_journal_id, ''), coalesce(opening_journal_id, ''), created_by, request_hash`
)

func scanRun(row pgx.Row) (domain.YearEndRun, error) {
	var run domain.YearEndRun
	var proposals []byte
	err := row.Scan(&run.ID, &run.LegalEntityID, &run.LedgerID, &run.FiscalYear, &run.Status, &run.Currency, &run.ClosingDate, &run.OpeningDate,
		&proposals, &run.ClosingJournalID, &run.OpeningJournalID, &run.CreatedBy, &run.RequestHash)
	if err != nil {
		return run, err
	}
	return run, json.Unmarshal(proposals, &run.Proposals)
}

func (s *Postgres) BeginYearEnd(ctx context.Context, in domain.YearEndRun) (domain.YearEndRun, bool, error) {
	proposals, err := json.Marshal(in.Proposals)
	if err != nil {
		return domain.YearEndRun{}, false, fmt.Errorf("encode year-end proposals: %w", err)
	}
	var run domain.YearEndRun
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		inserted, err := tx.Exec(ctx, `insert into year_end_run(tenant_id, id, legal_entity_id, ledger_id, fiscal_year, request_hash, status, currency, closing_date, opening_date, proposals, created_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, 'pending', $6, $7, $8, $9, $10) on conflict (tenant_id, ledger_id, fiscal_year) do nothing`,
			ids.New(), in.LegalEntityID, in.LedgerID, in.FiscalYear, in.RequestHash, in.Currency, in.ClosingDate, in.OpeningDate, proposals, in.CreatedBy)
		if err != nil {
			return err
		}
		created = inserted.RowsAffected() == 1
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where ledger_id = $1 and fiscal_year = $2 for update`, in.LedgerID, in.FiscalYear))
		return err
	})
	if err != nil {
		return domain.YearEndRun{}, false, fmt.Errorf("begin year-end run: %w", err)
	}
	if run.RequestHash != in.RequestHash {
		return domain.YearEndRun{}, false, &domain.Error{Code: domain.CodeIdempotencyReuse, Detail: "the year-end run of this ledger and fiscal year exists with a different request"}
	}
	return run, created, nil
}

func (s *Postgres) CompleteYearEnd(ctx context.Context, runID, closingJournalID, openingJournalID string) (domain.YearEndRun, error) {
	var run domain.YearEndRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		updated, err := tx.Exec(ctx, `update year_end_run set status = 'completed', closing_journal_id = nullif($2, ''), opening_journal_id = nullif($3, ''), completed_at = now() where id = $1 and status = 'pending'`,
			runID, closingJournalID, openingJournalID)
		if err != nil {
			return err
		}
		run, err = scanRun(tx.QueryRow(ctx, `select `+yearEndColumn+` from year_end_run where id = $1`, runID))
		if err != nil || updated.RowsAffected() == 0 {
			return err
		}
		p, _ := tenant.FromContext(ctx)
		evt, err := events.New(domain.EventYearEndCompleted, eventSource, run.ID, p.TenantID, domain.YearEndCompleted{
			RunID: run.ID, LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, FiscalYear: run.FiscalYear,
			ClosingJournalID: run.ClosingJournalID, OpeningJournalID: run.OpeningJournalID,
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
