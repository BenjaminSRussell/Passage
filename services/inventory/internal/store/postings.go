package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// issueCostScale is the scale of the unit cost IssueBasis derives (cost prices
// are numeric(20,6)).
const issueCostScale = 6

// IssueBasis is what the postings consumer issues a sales delivery against on
// the on-hand row of a product and dimension set: the currency the row is
// valued in ("" when the row does not exist or holds no value) and the running
// average unit cost of the stock on it, physical included: the posted value
// plus the physical cost of the transactions that are physically but not
// financially updated (receipts at REGISTERED or RECEIVED, issues at PICKED or
// DEDUCTED), over the same quantities. It is zero when no stock is left. The
// store values an issue at the posted average when there is posted stock and
// at this cost (the issue's fallback cost price) when there is none, so a
// delivery of stock that was received but not yet invoiced is still valued
// at its receipt cost. An issue in another currency than the row's is
// refused (CodeCurrencyMismatch), which is why the currency is read too.
func (s *Postgres) IssueBasis(ctx context.Context, legalEntityID, productID string, dims domain.Dimensions) (string, decimal.Decimal, error) {
	norm, err := dims.Normalize()
	if err != nil {
		return "", decimal.Zero, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
	}
	var currency *string
	var qty, value decimal.Decimal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select o.currency::text, o.posted_qty + coalesce(p.qty, 0), o.posted_value + coalesce(p.value, 0)
			from on_hand o
			left join lateral (
				select sum(t.qty) as qty, sum(t.cost_amount_physical) as value from inventory_transaction t
				where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id and t.product_id = o.product_id
					and t.dimension_set_hash = o.dimension_set_hash
					and (t.receipt_status in ('REGISTERED', 'RECEIVED') or t.issue_status in ('PICKED', 'DEDUCTED'))
			) p on true
			where o.legal_entity_id = $1 and o.product_id = $2 and o.dimension_set_hash = $3`,
			legalEntityID, productID, norm.Hash()).Scan(&currency, &qty, &value)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", decimal.Zero, nil
	}
	if err != nil {
		return "", decimal.Zero, err
	}
	cur := ""
	if currency != nil {
		cur = *currency
	}
	if !qty.IsPositive() || value.IsNegative() {
		return cur, decimal.Zero, nil
	}
	return cur, value.DivRound(qty, issueCostScale), nil
}

// OriginBookings returns the transactions registered for the given origins of
// one source type in a legal entity, keyed by source id: what the postings
// consumer booked for a receipt line or a delivery line, read back so a
// correction or a cancellation offsets exactly that booking (#480). An origin
// without a transaction is absent from the map.
func (s *Postgres) OriginBookings(ctx context.Context, legalEntityID, sourceType string, sourceIDs []string) (map[string]TransactionRow, error) {
	out := map[string]TransactionRow{}
	if len(sourceIDs) == 0 {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+transactionColumns+transactionFrom+`
			where t.tenant_id = $1 and t.legal_entity_id = $2::uuid and o.source_type = $3 and o.source_id = any($4::uuid[])`,
			p.TenantID, legalEntityID, sourceType, sourceIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanTransaction(rows)
			if err != nil {
				return err
			}
			out[r.SourceID] = r
		}
		return rows.Err()
	})
	return out, err
}
