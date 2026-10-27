package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
)

// maxListLimit is the most rows one reservation or transaction page reads: the
// API's 500-row page plus the one-row lookahead that tells whether more exist.
const maxListLimit = 501

// TransactionFilter selects inventory transactions (FR-16.13). Empty fields do
// not filter. Status matches the issue or the receipt status. From and To
// bound the physical date, inclusive. Cursor is the id of the last row of the
// previous page; pages run newest first (ids are UUIDv7, so id order is
// creation order).
type TransactionFilter struct {
	ProductID  string
	Status     domain.Status
	SourceType string
	From       *time.Time
	To         *time.Time
	Cursor     string
	Limit      int
}

// TransactionRow is one inventory transaction with its origin. JournalID is
// set for journal lines; Reference is the source document of other origins.
type TransactionRow struct {
	ID                 string
	LegalEntityID      string
	OriginID           string
	SourceType         string
	SourceID           string
	JournalID          string
	Reference          string
	ProductID          string
	Dims               domain.Dimensions
	Qty                decimal.Decimal
	EnteredQty         decimal.Decimal
	EnteredUnit        string
	IssueStatus        string
	ReceiptStatus      string
	PhysicalDate       string
	FinancialDate      string
	Currency           string
	CostAmountPhysical decimal.Decimal
	CostAmountPosted   *decimal.Decimal
	CreatedAt          time.Time
}

// ListTransactions returns up to f.Limit transactions, newest first, after the cursor.
func (s *Postgres) ListTransactions(ctx context.Context, f TransactionFilter) ([]TransactionRow, error) {
	limit := f.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = 100
	}
	var cursor *string
	if f.Cursor != "" {
		cursor = &f.Cursor
	}
	p, _ := tenant.FromContext(ctx)
	out := []TransactionRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select t.id::text, t.legal_entity_id::text, t.origin_id::text, o.source_type, o.source_id::text,
			coalesce(l.journal_id::text, ''), coalesce(o.reference_number, ''), t.product_id::text, d.dims, t.qty::text, t.entered_qty::text,
			t.entered_unit, coalesce(t.issue_status, ''), coalesce(t.receipt_status, ''), to_char(t.physical_date, 'YYYY-MM-DD'),
			coalesce(to_char(t.financial_date, 'YYYY-MM-DD'), ''), t.currency::text, t.cost_amount_physical::text, t.cost_amount_posted::text, t.created_at
			from inventory_transaction t
			join inventory_transaction_origin o on o.tenant_id = t.tenant_id and o.id = t.origin_id
			join dimension_set d on d.tenant_id = t.tenant_id and d.hash = t.dimension_set_hash
			left join inventory_journal_line l on o.source_type = 'INVENT_JOURNAL_LINE' and l.tenant_id = o.tenant_id and l.id = o.source_id
			where t.tenant_id = $1
			  and ($2 = '' or t.product_id::text = $2)
			  and ($3 = '' or t.issue_status = $3 or t.receipt_status = $3)
			  and ($4 = '' or o.source_type = $4)
			  and ($5::date is null or t.physical_date >= $5::date)
			  and ($6::date is null or t.physical_date <= $6::date)
			  and ($7::uuid is null or t.id < $7::uuid)
			order by t.id desc
			limit $8`, p.TenantID, f.ProductID, string(f.Status), f.SourceType, f.From, f.To, cursor, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r TransactionRow
			var raw []byte
			var qty, entered, cost string
			var posted *string
			if err := rows.Scan(&r.ID, &r.LegalEntityID, &r.OriginID, &r.SourceType, &r.SourceID, &r.JournalID, &r.Reference, &r.ProductID, &raw,
				&qty, &entered, &r.EnteredUnit, &r.IssueStatus, &r.ReceiptStatus, &r.PhysicalDate, &r.FinancialDate, &r.Currency,
				&cost, &posted, &r.CreatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r.Dims); err != nil {
				return err
			}
			r.Qty, r.EnteredQty, r.CostAmountPhysical = decimal.RequireFromString(qty), decimal.RequireFromString(entered), decimal.RequireFromString(cost)
			if posted != nil {
				v := decimal.RequireFromString(*posted)
				r.CostAmountPosted = &v
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
