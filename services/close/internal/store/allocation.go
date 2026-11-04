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

func (s *Postgres) PutAllocationRule(ctx context.Context, in domain.AllocationRule) (domain.AllocationRule, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.AllocationRule{}, errors.New("put allocation rule: no principal")
	}
	if in.ID == "" {
		in.ID = ids.New()
	}
	src, err := json.Marshal(in.SourceAccounts)
	if err != nil {
		return domain.AllocationRule{}, err
	}
	dest, err := json.Marshal(in.Destinations)
	if err != nil {
		return domain.AllocationRule{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into allocation_rule(tenant_id, id, legal_entity_id, ledger_id, name, method, source_filter, destinations, offset_account, offset_dimensions, allocate_from_zero, updated_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10, $11)
			on conflict (tenant_id, id) do update set name = excluded.name, method = excluded.method, source_filter = excluded.source_filter, destinations = excluded.destinations,
				offset_account = excluded.offset_account, offset_dimensions = excluded.offset_dimensions, allocate_from_zero = excluded.allocate_from_zero, updated_by = excluded.updated_by, updated_at = now()`,
			in.ID, in.LegalEntityID, in.LedgerID, in.Name, in.Method, src, dest, in.OffsetAccount, in.OffsetDimensions, in.AllocateFromZero, p.UserID)
		return err
	})
	if err != nil {
		return domain.AllocationRule{}, fmt.Errorf("put allocation rule: %w", err)
	}
	return in, nil
}

func (s *Postgres) AllocationRule(ctx context.Context, id string) (domain.AllocationRule, error) {
	var in domain.AllocationRule
	var src, dest []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, name, method, source_filter, destinations, offset_account, offset_dimensions, allocate_from_zero from allocation_rule where id = $1`, id).
			Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.Name, &in.Method, &src, &dest, &in.OffsetAccount, &in.OffsetDimensions, &in.AllocateFromZero)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "allocation rule not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read allocation rule: %w", err)
	}
	_ = json.Unmarshal(src, &in.SourceAccounts)
	_ = json.Unmarshal(dest, &in.Destinations)
	return in, nil
}

func (s *Postgres) SaveAllocationRun(ctx context.Context, in domain.AllocationRun, key, requestHash string) (domain.AllocationRun, error) {
	if in.ID == "" {
		in.ID = ids.New()
	}
	proposal, err := json.Marshal(in.Proposal)
	if err != nil {
		return in, err
	}
	amount, err := decimal.NewFromString(in.SourceAmount)
	if err != nil {
		return in, err
	}
	var saved domain.AllocationRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into allocation_run(tenant_id, id, legal_entity_id, ledger_id, fiscal_period_id, rule_id, posting_date, currency, source_amount, mode, status, proposal, idempotency_key, request_hash, created_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14)
			on conflict (tenant_id, idempotency_key) do nothing`,
			in.ID, in.LegalEntityID, in.LedgerID, in.FiscalPeriodID, in.RuleID, in.PostingDate, in.Currency, amount, in.Mode, in.Status, proposal, key, requestHash, in.CreatedBy)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			saved = in
			return nil
		}
		existing, err := scanAllocByKey(ctx, tx, key)
		if err != nil {
			return err
		}
		hash, err := allocHash(ctx, tx, existing.ID)
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
		return in, fmt.Errorf("save allocation run: %w", err)
	}
	return saved, nil
}

func scanAllocByKey(ctx context.Context, tx pgx.Tx, key string) (domain.AllocationRun, error) {
	var in domain.AllocationRun
	var proposal []byte
	var amount decimal.Decimal
	err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, fiscal_period_id::text, rule_id::text, posting_date::text, currency, source_amount, mode, status, proposal, entry_id, reversal_entry_id, created_by from allocation_run where idempotency_key = $1`, key).
		Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.FiscalPeriodID, &in.RuleID, &in.PostingDate, &in.Currency, &amount, &in.Mode, &in.Status, &proposal, &in.EntryID, &in.ReversalEntryID, &in.CreatedBy)
	in.SourceAmount = amount.String()
	_ = json.Unmarshal(proposal, &in.Proposal)
	return in, err
}

func allocHash(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var hash string
	err := tx.QueryRow(ctx, `select request_hash from allocation_run where id = $1`, id).Scan(&hash)
	return hash, err
}

func (s *Postgres) AllocationRun(ctx context.Context, id string) (domain.AllocationRun, error) {
	var in domain.AllocationRun
	var proposal []byte
	var amount decimal.Decimal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, fiscal_period_id::text, rule_id::text, posting_date::text, currency, source_amount, mode, status, proposal, entry_id, reversal_entry_id, created_by from allocation_run where id = $1`, id).
			Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.FiscalPeriodID, &in.RuleID, &in.PostingDate, &in.Currency, &amount, &in.Mode, &in.Status, &proposal, &in.EntryID, &in.ReversalEntryID, &in.CreatedBy)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "allocation run not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read allocation run: %w", err)
	}
	in.SourceAmount = amount.String()
	_ = json.Unmarshal(proposal, &in.Proposal)
	return in, nil
}

func (s *Postgres) MarkAllocationPosted(ctx context.Context, runID, entryID string) (domain.AllocationRun, error) {
	return s.finishAllocation(ctx, runID, `update allocation_run set status = 'posted', entry_id = $2 where id = $1`, entryID, false)
}

func (s *Postgres) MarkAllocationReversed(ctx context.Context, runID, entryID string) (domain.AllocationRun, error) {
	return s.finishAllocation(ctx, runID, `update allocation_run set status = 'reversed', reversal_entry_id = $2 where id = $1`, entryID, true)
}

func (s *Postgres) finishAllocation(ctx context.Context, runID, sql, entryID string, reversed bool) (domain.AllocationRun, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.AllocationRun{}, errors.New("allocation run: no principal")
	}
	var run domain.AllocationRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, runID, entryID); err != nil {
			return err
		}
		var err error
		run, err = scanAllocRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		evt, err := events.New(domain.EventAllocationCompleted, eventSource, run.ID, p.TenantID, domain.AllocationCompleted{
			RunID: run.ID, LedgerID: run.LedgerID, EntryID: entryID, Reversed: reversed,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "allocation_run", run.ID, evt)
	})
	return run, err
}

func scanAllocRun(ctx context.Context, tx pgx.Tx, id string) (domain.AllocationRun, error) {
	var in domain.AllocationRun
	var proposal []byte
	var amount decimal.Decimal
	err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, fiscal_period_id::text, rule_id::text, posting_date::text, currency, source_amount, mode, status, proposal, entry_id, reversal_entry_id, created_by from allocation_run where id = $1`, id).
		Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.FiscalPeriodID, &in.RuleID, &in.PostingDate, &in.Currency, &amount, &in.Mode, &in.Status, &proposal, &in.EntryID, &in.ReversalEntryID, &in.CreatedBy)
	in.SourceAmount = amount.String()
	_ = json.Unmarshal(proposal, &in.Proposal)
	return in, err
}
