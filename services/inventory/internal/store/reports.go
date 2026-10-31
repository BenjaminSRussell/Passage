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

// maxReportRows is the most rows one report returns; one more is read to tell
// the caller the result was cut.
const maxReportRows = 1000

// CountVarianceFilter selects the lines of the count variance report (3.16.14).
// Only counted lines of sheets that are not CANCELLED appear (a cancelled
// sheet never posts a variance). From and To bound the day the line was
// counted (UTC), inclusive.
type CountVarianceFilter struct {
	LegalEntityID string
	PlanID        string
	SheetID       string
	Warehouse     string
	Counter       string
	From          *time.Time
	To            *time.Time
}

// CountVarianceRow is one counted sheet line with its variance basis. A line
// of a POSTED sheet is valued at the cost of its COUNT journal line (JOURNAL;
// a zero variance has no journal line and values at zero), any other line at
// today's running average rounded as posting rounds it (ESTIMATE).
type CountVarianceRow struct {
	SheetID        string
	SheetNumber    string
	SheetStatus    string
	PlanID         string
	PlanName       string
	Warehouse      string
	LegalEntityID  string
	LineID         string
	LineNo         int
	ProductID      string
	Dims           domain.Dimensions
	Unit           string
	Currency       string
	Expected       decimal.Decimal
	Counted        decimal.Decimal
	CountedBy      string
	CountedAt      time.Time
	PostedVariance *decimal.Decimal
	UnitCost       decimal.Decimal
	ValueBasis     string
}

// ListCountVariance returns counted lines, newest count first.
func (s *Postgres) ListCountVariance(ctx context.Context, f CountVarianceFilter) ([]CountVarianceRow, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []CountVarianceRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select s.id::text, s.number, s.status, p.id::text, p.name, coalesce(p.warehouse, ''), s.legal_entity_id::text,
			l.id::text, l.line_no, l.product_id::text, d.dims, p.unit, p.currency::text, l.expected_qty::text, l.counted_qty::text,
			coalesce(l.counted_by, ''), l.counted_at, l.posted_variance::text,
			coalesce(jl.cost_price, case when oh.posted_qty > 0 then round(oh.posted_value / oh.posted_qty, 6) else 0 end)::text,
			s.status = 'POSTED'
			from count_sheet_line l
			join count_sheet s on s.tenant_id = l.tenant_id and s.id = l.sheet_id
			join count_plan p on p.tenant_id = s.tenant_id and p.id = s.plan_id
			join dimension_set d on d.tenant_id = l.tenant_id and d.hash = l.dimension_set_hash
			left join inventory_journal_line jl on jl.tenant_id = s.tenant_id and jl.journal_id = s.journal_id
			  and jl.product_id = l.product_id and jl.dimension_set_hash = l.dimension_set_hash
			left join on_hand oh on oh.tenant_id = l.tenant_id and oh.legal_entity_id = s.legal_entity_id
			  and oh.product_id = l.product_id and oh.dimension_set_hash = l.dimension_set_hash
			where l.tenant_id = $1 and l.counted_qty is not null and s.status <> 'CANCELLED'
			  and ($2 = '' or s.legal_entity_id::text = $2)
			  and ($3 = '' or p.id::text = $3)
			  and ($4 = '' or s.id::text = $4)
			  and ($5 = '' or p.warehouse = $5 or d.dims->>'warehouse' = $5)
			  and ($6 = '' or l.counted_by = $6)
			  and ($7::date is null or (l.counted_at at time zone 'UTC')::date >= $7::date)
			  and ($8::date is null or (l.counted_at at time zone 'UTC')::date <= $8::date)
			order by l.counted_at desc, s.number, l.line_no
			limit $9`, tp.TenantID, f.LegalEntityID, f.PlanID, f.SheetID, f.Warehouse, f.Counter, f.From, f.To, maxReportRows+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r CountVarianceRow
			var raw []byte
			var expected, counted, cost string
			var postedVariance *string
			var posted bool
			if err := rows.Scan(&r.SheetID, &r.SheetNumber, &r.SheetStatus, &r.PlanID, &r.PlanName, &r.Warehouse, &r.LegalEntityID, &r.LineID,
				&r.LineNo, &r.ProductID, &raw, &r.Unit, &r.Currency, &expected, &counted, &r.CountedBy, &r.CountedAt, &postedVariance, &cost, &posted); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r.Dims); err != nil {
				return err
			}
			r.Expected, r.Counted, r.UnitCost = decimal.RequireFromString(expected), decimal.RequireFromString(counted), decimal.RequireFromString(cost)
			if postedVariance != nil {
				v := decimal.RequireFromString(*postedVariance)
				r.PostedVariance = &v
			}
			r.ValueBasis = domain.ValueBasisEstimate
			if posted {
				r.ValueBasis = domain.ValueBasisJournal
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxReportRows {
		return out[:maxReportRows], true, nil
	}
	return out, false, nil
}

// NegativeOnHandFilter selects the negative on-hand list (3.16.14).
type NegativeOnHandFilter struct {
	LegalEntityID string
	ProductID     string
	SiteID        string
	WarehouseID   string
}

// NegativeOnHandRow is one projection row with negative physical stock.
// FirstNegative is the first physical date of the current negative run: the
// day after whose movements the end-of-day physical quantity went below zero
// and has stayed below zero since. Movements of one day are netted, so a
// same-day receipt and issue never show a phantom negative, and an earlier
// negative spell that was corrected does not count. Nil when the transactions
// do not show it.
type NegativeOnHandRow struct {
	LegalEntityID  string
	ProductID      string
	Dims           domain.Dimensions
	PhysicalInvent decimal.Decimal
	PostedValue    decimal.Decimal
	Currency       string
	FirstNegative  *time.Time
}

// ListNegativeOnHand returns rows with physical stock below zero, the largest
// shortfall first.
func (s *Postgres) ListNegativeOnHand(ctx context.Context, f NegativeOnHandFilter) ([]NegativeOnHandRow, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []NegativeOnHandRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select n.legal_entity_id::text, n.product_id::text, n.dims, n.phys::text, n.posted_value::text, coalesce(n.currency::text, ''),
			(select min(b.physical_date) from (
			   select r.physical_date, r.run, max(case when r.run >= 0 then r.physical_date end) over () as last_ok
			   from (select dq.physical_date, sum(dq.qty) over (order by dq.physical_date) as run
			         from (select t.physical_date, sum(t.qty) as qty
			               from inventory_transaction t
			               where t.tenant_id = n.tenant_id and t.legal_entity_id = n.legal_entity_id and t.product_id = n.product_id
			                 and t.dimension_set_hash = n.dimension_set_hash
			                 and (t.receipt_status in ('REGISTERED', 'RECEIVED', 'PURCHASED') or t.issue_status in ('PICKED', 'DEDUCTED', 'SOLD'))
			               group by t.physical_date) dq) r
			 ) b where b.run < 0 and (b.last_ok is null or b.physical_date > b.last_ok))
			from (select o.*, d.dims, o.posted_qty + o.received + o.registered - o.deducted - o.picked as phys
			      from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
			      where o.tenant_id = $1 and o.posted_qty + o.received + o.registered - o.deducted - o.picked < 0
			        and ($2 = '' or o.legal_entity_id::text = $2) and ($3 = '' or o.product_id::text = $3)
			        and ($4 = '' or d.dims->>'site' = $4) and ($5 = '' or d.dims->>'warehouse' = $5)) n
			order by n.phys, n.product_id, n.dimension_set_hash
			limit $6`, tp.TenantID, f.LegalEntityID, f.ProductID, f.SiteID, f.WarehouseID, maxReportRows+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r NegativeOnHandRow
			var raw []byte
			var phys, value string
			if err := rows.Scan(&r.LegalEntityID, &r.ProductID, &raw, &phys, &value, &r.Currency, &r.FirstNegative); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r.Dims); err != nil {
				return err
			}
			r.PhysicalInvent, r.PostedValue = decimal.RequireFromString(phys), decimal.RequireFromString(value)
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxReportRows {
		return out[:maxReportRows], true, nil
	}
	return out, false, nil
}

// TraceFilter selects the movements of one batch or serial (3.16.14). Batch or
// Serial must be set. Direction is domain.TraceBoth, TraceForward (issues) or
// TraceBackward (receipts).
type TraceFilter struct {
	LegalEntityID string
	ProductID     string
	Batch         string
	Serial        string
	Direction     string
}

// TraceTracking returns the transactions that carry the batch or serial in
// date order (oldest first), with their origin documents.
func (s *Postgres) TraceTracking(ctx context.Context, f TraceFilter) ([]TransactionRow, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []TransactionRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+transactionColumns+transactionFrom+`
			where t.tenant_id = $1 and ($2 = '' or t.legal_entity_id::text = $2) and ($3 = '' or t.product_id::text = $3)
			  and ($4 = '' or d.dims->>'batch' = $4) and ($5 = '' or d.dims->>'serial' = $5)
			  and ($6 = 'BOTH' or ($6 = 'FORWARD' and t.qty < 0) or ($6 = 'BACKWARD' and t.qty > 0))
			order by t.physical_date, t.id
			limit $7`, tp.TenantID, f.LegalEntityID, f.ProductID, f.Batch, f.Serial, f.Direction, maxReportRows+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanTransaction(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxReportRows {
		return out[:maxReportRows], true, nil
	}
	return out, false, nil
}

// DimensionCombinationFilter selects the dimension combinations inquiry.
type DimensionCombinationFilter struct {
	LegalEntityID string
	ProductID     string
}

// DimensionCombinationRow is one dimension set a product has used.
type DimensionCombinationRow struct {
	LegalEntityID    string
	ProductID        string
	Hash             string // hex of the dimension-set hash
	Dims             domain.Dimensions
	FirstUse         time.Time
	LastUse          time.Time
	TransactionCount int
}

// ListDimensionCombinations returns every product x dimension set that has
// transactions, with the first and last physical date of use.
func (s *Postgres) ListDimensionCombinations(ctx context.Context, f DimensionCombinationFilter) ([]DimensionCombinationRow, bool, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []DimensionCombinationRow{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select t.legal_entity_id::text, t.product_id::text, encode(t.dimension_set_hash, 'hex'), d.dims,
			min(t.physical_date), max(t.physical_date), count(*)
			from inventory_transaction t
			join dimension_set d on d.tenant_id = t.tenant_id and d.hash = t.dimension_set_hash
			where t.tenant_id = $1 and ($2 = '' or t.legal_entity_id::text = $2) and ($3 = '' or t.product_id::text = $3)
			group by t.legal_entity_id, t.product_id, t.dimension_set_hash, d.dims
			order by t.product_id, min(t.physical_date), t.dimension_set_hash
			limit $4`, tp.TenantID, f.LegalEntityID, f.ProductID, maxReportRows+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r DimensionCombinationRow
			var raw []byte
			if err := rows.Scan(&r.LegalEntityID, &r.ProductID, &r.Hash, &raw, &r.FirstUse, &r.LastUse, &r.TransactionCount); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &r.Dims); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxReportRows {
		return out[:maxReportRows], true, nil
	}
	return out, false, nil
}
