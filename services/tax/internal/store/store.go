package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	taxevents "github.com/BenjaminSRussell/passage/services/tax/internal/events"
)

const (
	outboxRecommit   = "tax_recommit"
	maxReasonLength  = 200
	MaxRecommitBatch = 50
	statusPending    = "pending"
	statusDone       = "done"
)

var ErrNoLegalEntity = errors.New("tax: the request carries no legal entity")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func scope(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return tenant.Principal{}, errors.New("tax: no principal in context")
	}
	if p.LegalEntityID == "" {
		return tenant.Principal{}, ErrNoLegalEntity
	}
	return p, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (s *Store) RecordTransactions(ctx context.Context, txns []domain.Txn) (int, error) {
	p, err := scope(ctx)
	if err != nil {
		return 0, err
	}
	inserted := 0
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, t := range txns {
			tag, err := tx.Exec(ctx, `insert into tax_transaction (tenant_id, id, legal_entity_id, code, direction, tax_date, amount)
				values ($1, $2, $3, $4, $5, $6, $7) on conflict (tenant_id, id) do nothing`,
				p.TenantID, t.ID, p.LegalEntityID, t.Code, t.Direction, t.TaxDate.Time, t.Amount.Decimal)
			if err != nil {
				return fmt.Errorf("record tax transaction %s: %w", t.ID, err)
			}
			inserted += int(tag.RowsAffected())
		}
		return nil
	})
	return inserted, err
}

type SettleInput struct {
	Period   domain.Period
	Accounts map[string]string
}

type Run struct {
	ID string `json:"id"`
	domain.Settlement
}

func (s *Store) SettleRun(ctx context.Context, in SettleInput, gl domain.GLMovement) (Run, error) {
	p, err := scope(ctx)
	if err != nil {
		return Run{}, err
	}
	var out Run
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		txns, err := lockTransactions(ctx, tx, p, in.Period)
		if err != nil {
			return err
		}
		for _, t := range txns {
			if key := domain.MovementKey(t.Code, t.Direction); in.Accounts[key] == "" {
				return &domain.Error{Code: domain.CodeValidation, Field: "accounts", Detail: fmt.Sprintf("accounts has no GL account for %s", key)}
			}
		}
		var prior int
		if err := tx.QueryRow(ctx, `select coalesce(max(version), 0) from tax_settlement_run
			where tenant_id = $1 and legal_entity_id = $2 and period_from = $3 and period_to = $4`,
			p.TenantID, p.LegalEntityID, in.Period.From.Time, in.Period.To.Time).Scan(&prior); err != nil {
			return fmt.Errorf("read settlement version: %w", err)
		}
		settlement, err := domain.Settle(ctx, txns, in.Period, prior, gl)
		if err != nil {
			return err
		}
		if len(settlement.TransactionIDs) == 0 {
			return &domain.Error{Code: domain.CodeNothingToSettle, Detail: "no unsettled tax transactions in the period"}
		}
		lines, err := json.Marshal(settlement.Lines)
		if err != nil {
			return err
		}
		out = Run{ID: ids.New(), Settlement: settlement}
		if _, err := tx.Exec(ctx, `insert into tax_settlement_run (tenant_id, id, legal_entity_id, period_from, period_to, version, output_tax_total, input_tax_total, net_amount, lines)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.TenantID, out.ID, p.LegalEntityID, in.Period.From.Time, in.Period.To.Time, settlement.Version,
			settlement.OutputTotal.Decimal, settlement.InputTotal.Decimal, settlement.NetAmount.Decimal, lines); err != nil {
			return fmt.Errorf("insert settlement run: %w", err)
		}
		if _, err := tx.Exec(ctx, `update tax_transaction set settlement_run_id = $3 where tenant_id = $1 and id = any($2::uuid[])`,
			p.TenantID, settlement.TransactionIDs, out.ID); err != nil {
			return fmt.Errorf("mark transactions settled: %w", err)
		}
		return nil
	})
	return out, err
}

func lockTransactions(ctx context.Context, tx pgx.Tx, p tenant.Principal, period domain.Period) ([]domain.Txn, error) {
	rows, err := tx.Query(ctx, `select id, code, direction, tax_date, amount, coalesce(settlement_run_id::text, '') from tax_transaction
		where tenant_id = $1 and legal_entity_id = $2 and tax_date between $3 and $4 order by tax_date, id for update`,
		p.TenantID, p.LegalEntityID, period.From.Time, period.To.Time)
	if err != nil {
		return nil, fmt.Errorf("lock tax transactions: %w", err)
	}
	defer rows.Close()
	var txns []domain.Txn
	for rows.Next() {
		var t domain.Txn
		if err := rows.Scan(&t.ID, &t.Code, &t.Direction, &t.TaxDate.Time, &t.Amount.Decimal, &t.SettlementRunID); err != nil {
			return nil, fmt.Errorf("scan tax transaction: %w", err)
		}
		txns = append(txns, t)
	}
	return txns, rows.Err()
}

type Total struct {
	Code      string
	Direction string
	Amount    decimal.Decimal
	Count     int
}

func (s *Store) Totals(ctx context.Context, period domain.Period) ([]Total, error) {
	p, err := scope(ctx)
	if err != nil {
		return nil, err
	}
	var totals []Total
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select code, direction, sum(amount), count(*) from tax_transaction
			where tenant_id = $1 and legal_entity_id = $2 and tax_date between $3 and $4 group by code, direction order by code, direction`,
			p.TenantID, p.LegalEntityID, period.From.Time, period.To.Time)
		if err != nil {
			return fmt.Errorf("sum tax transactions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t Total
			if err := rows.Scan(&t.Code, &t.Direction, &t.Amount, &t.Count); err != nil {
				return fmt.Errorf("scan tax total: %w", err)
			}
			totals = append(totals, t)
		}
		return rows.Err()
	})
	return totals, err
}

type Recommit struct {
	ID       string
	Provider string
	Request  json.RawMessage
	Attempts int
}

type RecommitSummary struct {
	Processed int `json:"processed"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

func (s *Store) EnqueueRecommit(ctx context.Context, provider, reason string, request json.RawMessage) (string, error) {
	p, err := scope(ctx)
	if err != nil {
		return "", err
	}
	id := ids.New()
	reason = truncate(reason, maxReasonLength)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into tax_recommit_queue (tenant_id, id, legal_entity_id, provider, reason, request) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, id, p.LegalEntityID, provider, reason, request); err != nil {
			return fmt.Errorf("queue recommit: %w", err)
		}
		evt, err := events.New(taxevents.ProviderFallback, taxevents.Source, id, p.TenantID,
			taxevents.ProviderFallbackData{LegalEntityID: p.LegalEntityID, RecommitID: id, Provider: provider, Reason: reason})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, outboxRecommit, id, evt)
	})
	return id, err
}

func (s *Store) Recommit(ctx context.Context, limit int, process func(context.Context, Recommit) (json.RawMessage, error)) (RecommitSummary, error) {
	p, err := scope(ctx)
	if err != nil {
		return RecommitSummary{}, err
	}
	limit = max(1, min(limit, MaxRecommitBatch))
	var sum RecommitSummary
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		pending, err := lockPending(ctx, tx, p, limit)
		if err != nil {
			return err
		}
		for _, item := range pending {
			result, perr := process(ctx, item)
			sum.Processed++
			if perr != nil {
				sum.Failed++
				if _, err := tx.Exec(ctx, `update tax_recommit_queue set attempts = attempts + 1, last_error = $3 where tenant_id = $1 and id = $2`,
					p.TenantID, item.ID, truncate(perr.Error(), maxReasonLength)); err != nil {
					return fmt.Errorf("record recommit failure: %w", err)
				}
				continue
			}
			sum.Completed++
			if _, err := tx.Exec(ctx, `update tax_recommit_queue set status = $3, attempts = attempts + 1, last_error = null, result = $4, completed_at = now() where tenant_id = $1 and id = $2`,
				p.TenantID, item.ID, statusDone, result); err != nil {
				return fmt.Errorf("record recommit result: %w", err)
			}
		}
		return nil
	})
	return sum, err
}

func lockPending(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int) ([]Recommit, error) {
	rows, err := tx.Query(ctx, `select id, provider, request, attempts from tax_recommit_queue
		where tenant_id = $1 and legal_entity_id = $2 and status = $3 order by created_at, id limit $4 for update skip locked`,
		p.TenantID, p.LegalEntityID, statusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("lock recommit queue: %w", err)
	}
	defer rows.Close()
	var items []Recommit
	for rows.Next() {
		var r Recommit
		if err := rows.Scan(&r.ID, &r.Provider, &r.Request, &r.Attempts); err != nil {
			return nil, fmt.Errorf("scan recommit item: %w", err)
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
