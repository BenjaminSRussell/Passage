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

func (s *Postgres) PutAccrualScheme(ctx context.Context, in domain.AccrualDefinition) (domain.AccrualDefinition, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.AccrualDefinition{}, errors.New("put accrual scheme: no principal")
	}
	custom, err := json.Marshal(in.Custom)
	if err != nil {
		return in, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into accrual_scheme(tenant_id, legal_entity_id, ledger_id, code, debit_account, credit_account, frequency, occurrences, spread, custom, auto_reverse, updated_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11)
			on conflict (tenant_id, ledger_id, code) do update set debit_account = excluded.debit_account, credit_account = excluded.credit_account, frequency = excluded.frequency,
				occurrences = excluded.occurrences, spread = excluded.spread, custom = excluded.custom, auto_reverse = excluded.auto_reverse, updated_by = excluded.updated_by, updated_at = now()`,
			in.LegalEntityID, in.LedgerID, in.Code, in.DebitAccount, in.CreditAccount, in.Frequency, in.Occurrences, in.Spread, custom, in.AutoReverse, p.UserID)
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
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select legal_entity_id::text, ledger_id::text, code, debit_account, credit_account, frequency, occurrences, spread, custom, auto_reverse from accrual_scheme where ledger_id = $1 and code = $2`, ledgerID, code).
			Scan(&in.LegalEntityID, &in.LedgerID, &in.Code, &in.DebitAccount, &in.CreditAccount, &in.Frequency, &in.Occurrences, &in.Spread, &custom, &in.AutoReverse)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "accrual scheme not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read accrual scheme: %w", err)
	}
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
			if _, err := tx.Exec(ctx, `update accrual_run set status = $2, entry_ids = $3::jsonb where id = $1`, in.ID, in.Status, idsRaw); err != nil {
				return err
			}
			saved = in
			return nil
		case !errors.Is(scanErr, pgx.ErrNoRows):
			return scanErr
		}
		tag, err := tx.Exec(ctx, `insert into accrual_run(tenant_id, id, legal_entity_id, ledger_id, scheme_code, amount, currency, start_date, status, occurrences, entry_ids, idempotency_key, request_hash, created_by)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11, $12, $13)
			on conflict (tenant_id, idempotency_key) do nothing`,
			in.ID, in.LegalEntityID, in.LedgerID, in.SchemeCode, amount, in.Currency, in.StartDate, in.Status, occ, idsRaw, key, requestHash, in.CreatedBy)
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
	if in.Status != "scheduled" && in.Status != "posted" {
		return nil
	}
	evt, err := events.New(domain.EventAccrualScheduled, eventSource, in.ID, tenantID, domain.AccrualScheduled{RunID: in.ID, SchemeCode: in.SchemeCode, LedgerID: in.LedgerID})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "accrual_run", in.ID, evt)
}

func scanAccrualByKey(ctx context.Context, tx pgx.Tx, key string) (domain.AccrualRun, string, error) {
	var in domain.AccrualRun
	var occ, idsRaw []byte
	var amount decimal.Decimal
	var hash string
	err := tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, scheme_code, amount, currency, start_date::text, status, occurrences, entry_ids, created_by, request_hash from accrual_run where idempotency_key = $1`, key).
		Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.SchemeCode, &amount, &in.Currency, &in.StartDate, &in.Status, &occ, &idsRaw, &in.CreatedBy, &hash)
	in.Amount = amount.String()
	_ = json.Unmarshal(occ, &in.Occurrences)
	_ = json.Unmarshal(idsRaw, &in.EntryIDs)
	if in.EntryIDs == nil {
		in.EntryIDs = map[string]string{}
	}
	return in, hash, err
}

func (s *Postgres) AccrualRun(ctx context.Context, id string) (domain.AccrualRun, error) {
	var in domain.AccrualRun
	var occ, idsRaw []byte
	var amount decimal.Decimal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text, legal_entity_id::text, ledger_id::text, scheme_code, amount, currency, start_date::text, status, occurrences, entry_ids, created_by from accrual_run where id = $1`, id).
			Scan(&in.ID, &in.LegalEntityID, &in.LedgerID, &in.SchemeCode, &amount, &in.Currency, &in.StartDate, &in.Status, &occ, &idsRaw, &in.CreatedBy)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return in, &domain.Error{Code: domain.CodeNoRun, Detail: "accrual run not found"}
	}
	if err != nil {
		return in, fmt.Errorf("read accrual run: %w", err)
	}
	in.Amount = amount.String()
	_ = json.Unmarshal(occ, &in.Occurrences)
	_ = json.Unmarshal(idsRaw, &in.EntryIDs)
	if in.EntryIDs == nil {
		in.EntryIDs = map[string]string{}
	}
	return in, nil
}
