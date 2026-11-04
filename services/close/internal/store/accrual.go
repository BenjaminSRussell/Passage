package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

const accrualRunColumns = `id::text, legal_entity_id::text, ledger_id::text, scheme_code, amount, currency, debit_account, credit_account, auto_reverse, start_date::text, status, occurrences, entry_ids, created_by, last_error, tenant_id::text`

func (s *Postgres) PutAccrualScheme(ctx context.Context, in domain.AccrualDefinition) (domain.AccrualDefinition, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.AccrualDefinition{}, errors.New("put accrual scheme: no principal")
	}
	custom, err := json.Marshal(in.Custom)
	if err != nil {
		return in, err
	}
	amount, err := decimal.NewFromString(in.Amount)
	if err != nil {
		return in, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into accrual_scheme(tenant_id, legal_entity_id, ledger_id, code, debit_account, credit_account, amount, frequency, occurrences, spread, custom, auto_reverse, updated_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12)
			on conflict (tenant_id, ledger_id, code) do update set debit_account = excluded.debit_account, credit_account = excluded.credit_account, amount = excluded.amount, frequency = excluded.frequency,
				occurrences = excluded.occurrences, spread = excluded.spread, custom = excluded.custom, auto_reverse = excluded.auto_reverse, updated_by = excluded.updated_by, updated_at = now()`,
			in.LegalEntityID, in.LedgerID, in.Code, in.DebitAccount, in.CreditAccount, amount, in.Frequency, in.Occurrences, in.Spread, custom, in.AutoReverse, p.UserID)
		return err
	})
	if err != nil {
		return in, fmt.Errorf("put accrual scheme: %w", err)
	}
	return in, nil
}

func (s *Postgres) AccrualScheme(ctx context.Context, ledgerID, code string) (domain.AccrualDefinition, error) {
	var in domain.AccrualDefinition
	var custom []byte
	var amount decimal.Decimal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select legal_entity_id::text, ledger_id::text, code, debit_account, credit_account, amount, frequency, occurrences, spread, custom, auto_reverse, updated_by from accrual_scheme where ledger_id = $1 and code = $2`, ledgerID, code).
			Scan(&in.LegalEntityID, &in.LedgerID, &in.Code, &in.DebitAccount, &in.CreditAccount, &amount, &in.Frequency, &in.Occurrences, &in.Spread, &custom, &in.AutoReverse, &in.UpdatedBy)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "accrual scheme not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read accrual scheme: %w", err)
	}
	in.Amount = amount.String()
	_ = json.Unmarshal(custom, &in.Custom)
	return in, nil
}

func (s *Postgres) SaveAccrualRun(ctx context.Context, in domain.AccrualRun, key, requestHash string) (domain.AccrualRun, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return in, errors.New("save accrual run: no principal")
	}
	if in.ID == "" {
		in.ID = ids.New()
	}
	occ, err := json.Marshal(in.Occurrences)
	if err != nil {
		return in, err
	}
	idsRaw, err := json.Marshal(in.EntryIDs)
	if err != nil {
		return in, err
	}
	amount, err := decimal.NewFromString(in.Amount)
	if err != nil {
		return in, err
	}
	var saved domain.AccrualRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existingID string
		scanErr := tx.QueryRow(ctx, `select id::text from accrual_run where id = $1`, in.ID).Scan(&existingID)
		switch {
		case scanErr == nil:
			tag, err := tx.Exec(ctx, `update accrual_run set status = $2, entry_ids = $3::jsonb, attempts = 0, last_error = '', next_attempt_at = coalesce($4, next_attempt_at) where id = $1 and status <> 'cancelled'`, in.ID, in.Status, idsRaw, in.NextDue)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "the accrual run was cancelled"}
			}
			saved = in
			return nil
		case !errors.Is(scanErr, pgx.ErrNoRows):
			return scanErr
		}
		tag, err := tx.Exec(ctx, `insert into accrual_run(tenant_id, id, legal_entity_id, ledger_id, scheme_code, amount, currency, debit_account, credit_account, auto_reverse, start_date, status, occurrences, entry_ids, next_attempt_at, idempotency_key, request_hash, created_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb, coalesce($14, now()), $15, $16, $17)
			on conflict (tenant_id, idempotency_key) do nothing`,
			in.ID, in.LegalEntityID, in.LedgerID, in.SchemeCode, amount, in.Currency, in.DebitAccount, in.CreditAccount, in.AutoReverse, in.StartDate, in.Status, occ, idsRaw, in.NextDue, key, requestHash, in.CreatedBy)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			saved = in
			return enqueueAccrual(ctx, tx, p.TenantID, in)
		}
		existing, hash, err := scanAccrualByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		if hash != requestHash {
			return reuse("Idempotency-Key was already used with a different request")
		}
		saved = existing
		return nil
	})
	if err != nil {
		return in, fmt.Errorf("save accrual run: %w", err)
	}
	return saved, nil
}

func enqueueAccrual(ctx context.Context, tx pgx.Tx, tenantID string, in domain.AccrualRun) error {
	evt, err := events.New(domain.EventAccrualScheduled, eventSource, in.ID, tenantID, domain.AccrualScheduled{RunID: in.ID, SchemeCode: in.SchemeCode, LedgerID: in.LedgerID})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "accrual_run", in.ID, evt)
}

func scanAccrual(row pgx.Row) (domain.AccrualRun, error) {
	var in domain.AccrualRun
	var occ, idsRaw []byte
	var amount decimal.Decimal
	err := row.Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.SchemeCode, &amount, &in.Currency, &in.DebitAccount, &in.CreditAccount, &in.AutoReverse, &in.StartDate, &in.Status, &occ, &idsRaw, &in.CreatedBy, &in.LastError, &in.TenantID)
	if err != nil {
		return in, err
	}
	in.Amount = amount.String()
	_ = json.Unmarshal(occ, &in.Occurrences)
	_ = json.Unmarshal(idsRaw, &in.EntryIDs)
	if in.EntryIDs == nil {
		in.EntryIDs = map[string]string{}
	}
	return in, nil
}

func scanAccrualByKey(ctx context.Context, tx pgx.Tx, key string) (domain.AccrualRun, string, error) {
	var hash string
	in, err := scanAccrual(&hashedRow{row: tx.QueryRow(ctx, `select `+accrualRunColumns+`, request_hash from accrual_run where idempotency_key = $1`, key), hash: &hash})
	return in, hash, err
}

type hashedRow struct {
	row  pgx.Row
	hash *string
}

func (h *hashedRow) Scan(dest ...any) error { return h.row.Scan(append(dest, h.hash)...) }

func (s *Postgres) AccrualRun(ctx context.Context, id string) (domain.AccrualRun, error) {
	var in domain.AccrualRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		in, err = scanAccrual(tx.QueryRow(ctx, `select `+accrualRunColumns+` from accrual_run where id = $1`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "accrual run not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read accrual run: %w", err)
	}
	return in, nil
}

const (
	dueAccrualBatch     = 100
	accrualLeaseSeconds = 300.0
)

func (s *Postgres) DueAccrualRuns(ctx context.Context) ([]domain.AccrualRun, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()
	rows, err := conn.Query(ctx, `select close_accrual_due_tenants()`)
	if err != nil {
		return nil, fmt.Errorf("list due accrual tenants: %w", err)
	}
	var tenants []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		tenants = append(tenants, id)
	}
	rows.Close()
	var out []domain.AccrualRun
	var failed []error
	for _, tenantID := range tenants {
		runs, err := s.dueAccrualForTenant(ctx, tenantID)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tenantID, err))
			continue
		}
		out = append(out, runs...)
	}
	return out, errors.Join(failed...)
}

func (s *Postgres) dueAccrualForTenant(ctx context.Context, tenantID string) ([]domain.AccrualRun, error) {
	ctx = tenant.With(ctx, tenant.Principal{TenantID: tenantID, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	var out []domain.AccrualRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from accrual_run where status = 'scheduled' and next_attempt_at <= now() order by next_attempt_at limit $1 for update skip locked
			) update accrual_run set next_attempt_at = now() + make_interval(secs => $2) from claimed where accrual_run.id = claimed.claimed_id returning `+accrualRunColumns, dueAccrualBatch, accrualLeaseSeconds)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanAccrual(rows)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) RecordAccrualFailure(ctx context.Context, id, message string, park bool) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update accrual_run set attempts = attempts + 1, last_error = left($2, 500), status = case when $3 then 'parked' else status end,
			next_attempt_at = now() + make_interval(secs => least(60 * power(2, attempts), 3600)) where id = $1`, id, message, park)
		return err
	})
}

func (s *Postgres) CancelAccrualRun(ctx context.Context, id string) (domain.AccrualRun, error) {
	var out domain.AccrualRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := scanAccrual(tx.QueryRow(ctx, `select `+accrualRunColumns+` from accrual_run where id = $1 for update`, id))
		if err != nil {
			return err
		}
		out = run
		switch run.Status {
		case "cancelled":
			return nil
		case "scheduled", "parked":
		default:
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: "only a scheduled or parked accrual run can be cancelled"}
		}
		out.Status = "cancelled"
		_, err = tx.Exec(ctx, `update accrual_run set status = 'cancelled' where id = $1`, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &domain.Error{Code: domain.CodeNoRun, Detail: "accrual run not found"}
	}
	return out, err
}
