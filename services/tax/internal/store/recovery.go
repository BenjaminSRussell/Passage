package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

func listRecovery(ctx context.Context, tx pgx.Tx, tenantID string) ([]domain.RecoveryPolicy, error) {
	rows, err := tx.Query(ctx, `select code, item_group, recoverable_pct::text, valid_from, valid_to, note from tax_recovery_policy
		where tenant_id = $1 order by code, item_group, valid_from`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list recovery policies: %w", err)
	}
	defer rows.Close()
	out := []domain.RecoveryPolicy{}
	for rows.Next() {
		var r domain.RecoveryPolicy
		var pct string
		var from time.Time
		var to *time.Time
		if err := rows.Scan(&r.Code, &r.ItemGroup, &pct, &from, &to, &r.Note); err != nil {
			return nil, fmt.Errorf("scan recovery policy: %w", err)
		}
		if r.RecoverablePct, err = parseDec(pct); err != nil {
			return nil, err
		}
		r.ValidFrom = domain.Date{Time: from}
		if to != nil {
			r.ValidTo = domain.Date{Time: *to}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListRecoveryPolicies(ctx context.Context) ([]domain.RecoveryPolicy, error) {
	p, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.RecoveryPolicy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out, err = listRecovery(ctx, tx, p.TenantID)
		return err
	})
	return out, err
}

// PutRecoveryPolicy creates or replaces the policy of a code, item group and start date. Two policies of one code and group may not
// overlap in time, so the one that applies on a date is never a matter of ordering.
func (s *Store) PutRecoveryPolicy(ctx context.Context, r domain.RecoveryPolicy) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		codes, err := listCodes(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		groups, err := listGroups(ctx, tx, p.TenantID, domain.KindItem)
		if err != nil {
			return err
		}
		knownCodes, knownGroups := map[string]bool{}, map[string]bool{}
		for _, c := range codes {
			knownCodes[c.Code.Code] = true
		}
		for _, g := range groups {
			knownGroups[g.Code] = true
		}
		if err := domain.ValidateRecoveryPolicy(r, knownCodes, knownGroups); err != nil {
			return err
		}
		var to any
		if !r.ValidTo.IsZero() {
			to = r.ValidTo.Time
		}
		var clash int
		if err := tx.QueryRow(ctx, `select count(*) from tax_recovery_policy where tenant_id = $1 and code = $2 and item_group = $3 and valid_from <> $4
			and valid_from <= coalesce($5::date, 'infinity'::date) and coalesce(valid_to, 'infinity'::date) >= $4`,
			p.TenantID, r.Code, r.ItemGroup, r.ValidFrom.Time, to).Scan(&clash); err != nil {
			return fmt.Errorf("check recovery policy overlap: %w", err)
		}
		if clash > 0 {
			return &domain.Error{Code: domain.CodeRateInvalid, Field: "valid_from", Detail: "another recovery policy of this code and item group is valid in the same dates"}
		}
		if _, err := tx.Exec(ctx, `insert into tax_recovery_policy (tenant_id, code, item_group, recoverable_pct, valid_from, valid_to, note)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, code, item_group, valid_from) do update set recoverable_pct = excluded.recoverable_pct, valid_to = excluded.valid_to,
				note = excluded.note, updated_at = now()`, p.TenantID, r.Code, r.ItemGroup, r.RecoverablePct.Decimal, r.ValidFrom.Time, to, r.Note); err != nil {
			return fmt.Errorf("save recovery policy: %w", err)
		}
		return nil
	})
}

func (s *Store) DeleteRecoveryPolicy(ctx context.Context, code, itemGroup string, validFrom domain.Date) error {
	p, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from tax_recovery_policy where tenant_id = $1 and code = $2 and item_group = $3 and valid_from = $4`,
			p.TenantID, code, itemGroup, validFrom.Time)
		if err != nil {
			return fmt.Errorf("delete recovery policy: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Reclaim totals the input tax of the request's legal entity over the period: what may be reclaimed and what stays in cost.
func (s *Store) Reclaim(ctx context.Context, period domain.Period) (domain.Reclaim, error) {
	p, err := scope(ctx)
	if err != nil {
		return domain.Reclaim{}, err
	}
	var out domain.Reclaim
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select code, tax_date, amount::text, recoverable_amount::text from tax_transaction
			where tenant_id = $1 and legal_entity_id = $2 and direction = 'input' and tax_date between $3 and $4`,
			p.TenantID, p.LegalEntityID, period.From.Time, period.To.Time)
		if err != nil {
			return fmt.Errorf("read input tax: %w", err)
		}
		var txns []domain.ReclaimTxn
		for rows.Next() {
			var t domain.ReclaimTxn
			var amount string
			var rec *string
			if err := rows.Scan(&t.Code, &t.TaxDate, &amount, &rec); err != nil {
				rows.Close()
				return fmt.Errorf("scan input tax: %w", err)
			}
			t.Direction = domain.DirInput
			if t.Amount, err = decimal.NewFromString(amount); err != nil {
				rows.Close()
				return err
			}
			if rec != nil {
				d, err := decimal.NewFromString(*rec)
				if err != nil {
					rows.Close()
					return err
				}
				t.Recoverable = &d
			}
			txns = append(txns, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		codes, err := listCodes(ctx, tx, p.TenantID)
		if err != nil {
			return err
		}
		pct := map[string]*domain.Dec{}
		for _, c := range codes {
			pct[c.Code.Code] = c.RecoverablePct
		}
		out = domain.ComputeReclaim(period, pct, txns)
		return nil
	})
	return out, err
}
