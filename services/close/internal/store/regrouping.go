package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func (s *Postgres) PutRegroupingRule(ctx context.Context, in domain.StoredRegroupingRule) (domain.StoredRegroupingRule, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return in, errors.New("put regrouping rule: no principal")
	}
	buckets, err := json.Marshal(in.Buckets)
	if err != nil {
		return in, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into regrouping_rule(tenant_id, legal_entity_id, ledger_id, code, sort_method, source_account, buckets, adjustment_account, target_account, normal_balance, sequence, updated_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11)
			on conflict (tenant_id, ledger_id, code) do update set sort_method = excluded.sort_method, source_account = excluded.source_account, buckets = excluded.buckets,
				adjustment_account = excluded.adjustment_account, target_account = excluded.target_account, normal_balance = excluded.normal_balance, sequence = excluded.sequence, updated_by = excluded.updated_by, updated_at = now()`,
			in.LegalEntityID, in.LedgerID, in.Code, in.SortMethod, in.SourceAccount, buckets, in.AdjustmentAccount, in.TargetAccount, in.NormalBalance, in.Sequence, p.UserID)
		return err
	})
	if err != nil {
		return in, fmt.Errorf("put regrouping rule: %w", err)
	}
	return in, nil
}

func (s *Postgres) RegroupingRule(ctx context.Context, ledgerID, code string) (domain.StoredRegroupingRule, error) {
	var in domain.StoredRegroupingRule
	var buckets []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select legal_entity_id::text, ledger_id::text, code, sort_method, source_account, buckets, adjustment_account, target_account, normal_balance, sequence from regrouping_rule where ledger_id = $1 and code = $2`, ledgerID, code).
			Scan(&in.LegalEntityID, &in.LedgerID, &in.Code, &in.SortMethod, &in.SourceAccount, &buckets, &in.AdjustmentAccount, &in.TargetAccount, &in.NormalBalance, &in.Sequence)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "regrouping rule not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read regrouping rule: %w", err)
	}
	_ = json.Unmarshal(buckets, &in.Buckets)
	return in, nil
}

func (s *Postgres) ListRegroupingRules(ctx context.Context, ledgerID string, codes []string) ([]domain.StoredRegroupingRule, error) {
	if len(codes) == 0 {
		return nil, &domain.Error{Code: domain.CodeValidation, Detail: "at least one regrouping rule is required"}
	}
	out := make([]domain.StoredRegroupingRule, 0, len(codes))
	for _, code := range codes {
		rule, err := s.RegroupingRule(ctx, ledgerID, code)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

func (s *Postgres) SaveRegroupingRun(ctx context.Context, in domain.RegroupingRun, key, requestHash string) (domain.RegroupingRun, error) {
	if in.ID == "" {
		in.ID = ids.New()
	}
	proposal, err := json.Marshal(in.Proposal)
	if err != nil {
		return in, err
	}
	var saved domain.RegroupingRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into regrouping_run(tenant_id, id, legal_entity_id, ledger_id, key_date, rule_codes, mode, status, proposal, idempotency_key, request_hash, created_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11)
			on conflict (tenant_id, idempotency_key) do nothing`,
			in.ID, in.LegalEntityID, in.LedgerID, in.KeyDate, in.RuleCodes, in.Mode, in.Status, proposal, key, requestHash, in.CreatedBy)
		if err != nil {
			return uniqueRegroup(err)
		}
		if tag.RowsAffected() == 1 {
			saved = in
			return nil
		}
		existing, hash, err := scanRegroupByKey(ctx, tx, key)
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
		return in, fmt.Errorf("save regrouping run: %w", err)
	}
	return saved, nil
}

func scanRegroupByKey(ctx context.Context, tx pgx.Tx, key string) (domain.RegroupingRun, string, error) {
	var in domain.RegroupingRun
	var proposal []byte
	var hash string
	err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, key_date::text, rule_codes, mode, status, proposal, entry_id, reversal_entry_id, created_by, request_hash from regrouping_run where idempotency_key = $1`, key).
		Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.KeyDate, &in.RuleCodes, &in.Mode, &in.Status, &proposal, &in.EntryID, &in.ReversalEntryID, &in.CreatedBy, &hash)
	_ = json.Unmarshal(proposal, &in.Proposal)
	return in, hash, err
}

func uniqueRegroup(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return &domain.Error{Code: domain.CodeRegroupingExists, Detail: "a posted regrouping already exists for this ledger, key date and rules"}
	}
	return err
}

func (s *Postgres) RegroupingRun(ctx context.Context, id string) (domain.RegroupingRun, error) {
	var in domain.RegroupingRun
	var proposal []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, key_date::text, rule_codes, mode, status, proposal, entry_id, reversal_entry_id, created_by from regrouping_run where id = $1`, id).
			Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.KeyDate, &in.RuleCodes, &in.Mode, &in.Status, &proposal, &in.EntryID, &in.ReversalEntryID, &in.CreatedBy)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "regrouping run not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read regrouping run: %w", err)
	}
	_ = json.Unmarshal(proposal, &in.Proposal)
	return in, nil
}

func (s *Postgres) MarkRegroupingPosted(ctx context.Context, runID, entryID string) (domain.RegroupingRun, error) {
	return s.finishRegroup(ctx, runID, `update regrouping_run set status = 'posted', entry_id = $2 where id = $1`, entryID, false)
}

func (s *Postgres) MarkRegroupingReversed(ctx context.Context, runID, entryID string) (domain.RegroupingRun, error) {
	return s.finishRegroup(ctx, runID, `update regrouping_run set status = 'reversed', reversal_entry_id = $2 where id = $1`, entryID, true)
}

func (s *Postgres) finishRegroup(ctx context.Context, runID, sql, entryID string, reversed bool) (domain.RegroupingRun, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.RegroupingRun{}, errors.New("regrouping run: no principal")
	}
	var run domain.RegroupingRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sql, runID, entryID); err != nil {
			return uniqueRegroup(err)
		}
		var err error
		run, err = s.scanRegroup(ctx, tx, runID)
		if err != nil {
			return err
		}
		evt, err := events.New(domain.EventRegroupingCompleted, eventSource, run.ID, p.TenantID, domain.RegroupingCompleted{
			RunID: run.ID, LedgerID: run.LedgerID, KeyDate: run.KeyDate, EntryID: entryID, Reversed: reversed,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "regrouping_run", run.ID, evt)
	})
	return run, err
}

func (s *Postgres) scanRegroup(ctx context.Context, tx pgx.Tx, id string) (domain.RegroupingRun, error) {
	var in domain.RegroupingRun
	var proposal []byte
	err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, key_date::text, rule_codes, mode, status, proposal, entry_id, reversal_entry_id, created_by from regrouping_run where id = $1`, id).
		Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.KeyDate, &in.RuleCodes, &in.Mode, &in.Status, &proposal, &in.EntryID, &in.ReversalEntryID, &in.CreatedBy)
	_ = json.Unmarshal(proposal, &in.Proposal)
	return in, err
}
