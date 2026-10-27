package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

// Adjustment problem codes raised by the store (the planning refusals are the
// domain's CST-ADJ-002 to 005). CST-ADJ-006: the value changed since the
// caller's preview. CST-ADJ-007: no such adjustment.
const (
	CodeAdjustmentStale    = "CST-ADJ-006"
	CodeAdjustmentNotFound = "CST-ADJ-007"
	adjustmentSourcePrefix = "manual-adjustment:"
)

// CostChangedEventType is dev.passage.costing.cost.changed.v1.
var CostChangedEventType = events.Type("costing", "cost", "changed", 1)

// AdjustmentRequest is a validated adjustment. ValuationLedgerID is the stored
// form ("" for the primary valuation). An ON_HAND adjustment names the product,
// site, warehouse and valuation type; a RECEIPT adjustment names the receipt
// transaction and takes them from it. UnitCost is set in UNIT_COST mode and
// Amount in AMOUNT mode. ExpectedValue, when set, is the value the caller
// previewed: a different current value refuses the post (CST-ADJ-006).
type AdjustmentRequest struct {
	LegalEntityID        string `json:"legalEntityId"`
	Kind                 string `json:"kind"`
	Mode                 string `json:"mode"`
	ProductID            string `json:"productId,omitempty"`
	SiteID               string `json:"siteId,omitempty"`
	WarehouseID          string `json:"warehouseId,omitempty"`
	ValuationType        string `json:"valuationType,omitempty"`
	ValuationLedgerID    string `json:"valuationLedgerId,omitempty"`
	ReceiptTransactionID string `json:"receiptTransactionId,omitempty"`
	FinancialDate        string `json:"financialDate"`
	UnitCost             string `json:"unitCost,omitempty"`
	Amount               string `json:"amount,omitempty"`
	ExpectedValue        string `json:"expectedValue,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

// Adjustment is a planned or posted manual adjustment (FR-20.6). Amounts are
// decimal strings at the currency's minor unit; a preview has no ID,
// CostTransactionID or CreatedAt.
type Adjustment struct {
	ID                   string     `json:"id,omitempty"`
	LegalEntityID        string     `json:"legalEntityId"`
	Kind                 string     `json:"kind"`
	Mode                 string     `json:"mode"`
	ProductID            string     `json:"productId"`
	SiteID               string     `json:"siteId"`
	WarehouseID          string     `json:"warehouseId,omitempty"`
	ValuationType        string     `json:"valuationType,omitempty"`
	ValuationLedgerID    string     `json:"valuationLedgerId,omitempty"`
	ReceiptTransactionID string     `json:"receiptTransactionId,omitempty"`
	Currency             string     `json:"currency"`
	FinancialDate        string     `json:"financialDate"`
	Reason               string     `json:"reason,omitempty"`
	UnitCost             string     `json:"unitCost,omitempty"`
	Amount               string     `json:"amount,omitempty"`
	Qty                  string     `json:"qty"`
	OldValue             string     `json:"oldValue"`
	NewValue             string     `json:"newValue"`
	Delta                string     `json:"delta"`
	OldUnitCost          string     `json:"oldUnitCost,omitempty"`
	NewUnitCost          string     `json:"newUnitCost,omitempty"`
	CostTransactionID    string     `json:"costTransactionId,omitempty"`
	CreatedAt            *time.Time `json:"createdAt,omitempty"`
}

// AdjustmentFilter selects adjustments for the list. Empty fields do not filter.
type AdjustmentFilter struct {
	LegalEntityID string
	ProductID     string
	Kind          string
}

// AdjustmentPage is one page of adjustments, newest financial date first.
type AdjustmentPage struct {
	Items []Adjustment
	Next  string
}

// target is what an adjustment applies to, with its current quantity and value.
type target struct {
	productID, siteID, warehouseID, valuationType, currency string
	qty, value                                              decimal.Decimal
}

// fmtAmount prints an amount at the currency's minor unit, or in full when it
// carries more precision, so a sub-cent value is never hidden.
func fmtAmount(d decimal.Decimal, scale int32) string {
	if d.Equal(d.Round(scale)) {
		return d.StringFixed(scale)
	}
	return d.String()
}

// readTarget sums the inventory kinds (PPV never counts) of what the request
// adjusts, up to and including its financial date, in its valuation ledger.
func readTarget(ctx context.Context, tx pgx.Tx, tenantID string, in AdjustmentRequest) (target, error) {
	var rows pgx.Rows
	var err error
	if in.Kind == domain.AdjustReceipt {
		rows, err = tx.Query(ctx, `select product_id::text, site_id::text, coalesce(warehouse_id::text, ''), valuation_type, currency,
				sum(qty)::text, sum(amount_acct)::text
			from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2::uuid and valuation_ledger_id is not distinct from $3::uuid
			  and inventory_transaction_id = $4::uuid and financial_date <= $5::date and kind <> 'PPV'
			group by product_id, site_id, warehouse_id, valuation_type, currency`,
			tenantID, in.LegalEntityID, optUUID(in.ValuationLedgerID), in.ReceiptTransactionID, in.FinancialDate)
	} else {
		rows, err = tx.Query(ctx, `select product_id::text, site_id::text, coalesce(warehouse_id::text, ''), valuation_type, currency,
				sum(qty)::text, sum(amount_acct)::text
			from cost_transaction
			where tenant_id = $1 and legal_entity_id = $2::uuid and valuation_ledger_id is not distinct from $3::uuid
			  and product_id = $4::uuid and site_id = $5::uuid and warehouse_id is not distinct from $6::uuid
			  and valuation_type = $7 and financial_date <= $8::date and kind <> 'PPV'
			group by product_id, site_id, warehouse_id, valuation_type, currency`,
			tenantID, in.LegalEntityID, optUUID(in.ValuationLedgerID), in.ProductID, in.SiteID, optUUID(in.WarehouseID),
			in.ValuationType, in.FinancialDate)
	}
	if err != nil {
		return target{}, err
	}
	var found []target
	for rows.Next() {
		var t target
		var qty, value string
		if err := rows.Scan(&t.productID, &t.siteID, &t.warehouseID, &t.valuationType, &t.currency, &qty, &value); err != nil {
			rows.Close()
			return target{}, err
		}
		if t.qty, err = decimal.NewFromString(qty); err != nil {
			rows.Close()
			return target{}, err
		}
		if t.value, err = decimal.NewFromString(value); err != nil {
			rows.Close()
			return target{}, err
		}
		found = append(found, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return target{}, err
	}
	if in.Kind == domain.AdjustReceipt {
		if len(found) != 1 || !found[0].qty.IsPositive() {
			return target{}, &domain.Error{Code: domain.CodeAdjustNothing,
				Detail: "the transaction is not a receipt of this legal entity and valuation ledger on or before the financial date"}
		}
		return found[0], nil
	}
	switch len(found) {
	case 0:
		return target{}, &domain.Error{Code: domain.CodeAdjustNothing,
			Detail: "there is no stock or value to adjust for this product, site, warehouse and valuation type on the financial date"}
	case 1:
		if found[0].qty.IsZero() && found[0].value.IsZero() {
			return target{}, &domain.Error{Code: domain.CodeAdjustNothing,
				Detail: "there is no stock or value to adjust for this product, site, warehouse and valuation type on the financial date"}
		}
		return found[0], nil
	default:
		return target{}, &domain.Error{Code: domain.CodeReportCurrency,
			Detail: fmt.Sprintf("the valuation ledger holds cost transactions in %s and %s for this stock", found[0].currency, found[1].currency)}
	}
}

// planAdjustment computes the adjustment of a target read by readTarget.
func planAdjustment(in AdjustmentRequest, t target) (Adjustment, domain.AdjustmentPlan, error) {
	var err error
	scale := money.MinorUnits(t.currency)
	pi := domain.AdjustmentInput{Mode: in.Mode, Qty: t.qty, Value: t.value, Scale: scale}
	if in.UnitCost != "" {
		if pi.UnitCost, err = decimal.NewFromString(in.UnitCost); err != nil {
			return Adjustment{}, domain.AdjustmentPlan{}, err
		}
	}
	if in.Amount != "" {
		if pi.Amount, err = decimal.NewFromString(in.Amount); err != nil {
			return Adjustment{}, domain.AdjustmentPlan{}, err
		}
	}
	plan, err := domain.PlanAdjustment(pi)
	if err != nil {
		return Adjustment{}, domain.AdjustmentPlan{}, err
	}
	out := Adjustment{LegalEntityID: in.LegalEntityID, Kind: in.Kind, Mode: in.Mode, ProductID: t.productID, SiteID: t.siteID,
		WarehouseID: t.warehouseID, ValuationType: t.valuationType, ValuationLedgerID: in.ValuationLedgerID,
		ReceiptTransactionID: in.ReceiptTransactionID, Currency: t.currency, FinancialDate: in.FinancialDate, Reason: in.Reason,
		UnitCost: in.UnitCost, Amount: in.Amount, Qty: plan.Qty.String(), OldValue: fmtAmount(plan.OldValue, scale),
		NewValue: fmtAmount(plan.NewValue, scale), Delta: fmtAmount(plan.Delta, scale)}
	if plan.HasUnitCost {
		out.OldUnitCost, out.NewUnitCost = plan.OldUnitCost.String(), plan.NewUnitCost.String()
	}
	return out, plan, nil
}

// PreviewAdjustment computes an adjustment without posting it: the current
// quantity and value, the new value and the delta the post would write.
func (s *Postgres) PreviewAdjustment(ctx context.Context, in AdjustmentRequest) (Adjustment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Adjustment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := readTarget(ctx, tx, p.TenantID, in)
		if err != nil {
			return err
		}
		out, _, err = planAdjustment(in, t)
		return err
	})
	return out, err
}

// CreateAdjustment posts an adjustment in one transaction: it serialises
// adjustments of the same product x site (advisory lock), reads the current
// value, refuses a stale preview (CST-ADJ-006), stores the adjustment, appends
// its MANUAL_ADJ cost transaction and, for an ON_HAND adjustment of the primary
// valuation that changes the unit cost of positive stock, writes cost.changed
// to the outbox. A repeated Idempotency-Key returns the stored adjustment with
// created=false and writes nothing; with another request it is refused.
func (s *Postgres) CreateAdjustment(ctx context.Context, in AdjustmentRequest, idempotencyKey, requestHash string) (Adjustment, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Adjustment
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, ok, err := adjustmentByKey(ctx, tx, p.TenantID, idempotencyKey, requestHash)
		if err != nil || ok {
			out = replay
			return err
		}
		// One lock per product x site, whichever kind adjusts it, so an on-hand
		// revaluation never reads a value a concurrent receipt adjustment moves.
		product, site := in.ProductID, in.SiteID
		if in.Kind == domain.AdjustReceipt {
			t, err := readTarget(ctx, tx, p.TenantID, in)
			if err != nil {
				return err
			}
			product, site = t.productID, t.siteID
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"cost_adjustment:"+p.TenantID+":"+in.LegalEntityID+":"+product+"|"+site); err != nil {
			return err
		}
		t, err := readTarget(ctx, tx, p.TenantID, in)
		if err != nil {
			return err
		}
		// A stale preview is reported before any planning refusal: the caller
		// decided on a value that is no longer there and must preview again.
		if in.ExpectedValue != "" {
			if want, err := decimal.NewFromString(in.ExpectedValue); err != nil || !want.Equal(t.value) {
				return httpx.NewProblem(http.StatusConflict, CodeAdjustmentStale, fmt.Sprintf("the value changed since the preview: it is now %s %s",
					fmtAmount(t.value, money.MinorUnits(t.currency)), t.currency))
			}
		}
		adj, plan, err := planAdjustment(in, t)
		if err != nil {
			return err
		}
		adj.ID, adj.CostTransactionID = ids.New(), ids.New()
		var createdAt time.Time
		err = tx.QueryRow(ctx, `insert into cost_adjustment (tenant_id, id, legal_entity_id, kind, mode, product_id, site_id, warehouse_id,
				valuation_type, valuation_ledger_id, receipt_transaction_id, currency, financial_date, reason, unit_cost, amount, qty,
				old_value, new_value, delta, old_unit_cost, new_unit_cost, cost_transaction_id, idempotency_key, request_hash)
			values ($1, $2, $3::uuid, $4, $5, $6::uuid, $7::uuid, nullif($8, '')::uuid, $9, nullif($10, '')::uuid, nullif($11, '')::uuid,
				$12, $13::date, $14, nullif($15, '')::numeric, nullif($16, '')::numeric, $17::numeric, $18::numeric, $19::numeric,
				$20::numeric, nullif($21, '')::numeric, nullif($22, '')::numeric, $23::uuid, $24, $25)
			on conflict (tenant_id, idempotency_key) do nothing
			returning created_at`,
			p.TenantID, adj.ID, adj.LegalEntityID, adj.Kind, adj.Mode, adj.ProductID, adj.SiteID, adj.WarehouseID, adj.ValuationType,
			adj.ValuationLedgerID, adj.ReceiptTransactionID, adj.Currency, adj.FinancialDate, adj.Reason, adj.UnitCost, adj.Amount,
			plan.Qty.String(), plan.OldValue.String(), plan.NewValue.String(), plan.Delta.String(), adj.OldUnitCost, adj.NewUnitCost,
			adj.CostTransactionID, idempotencyKey, requestHash).Scan(&createdAt)
		if errors.Is(err, pgx.ErrNoRows) {
			// The same key committed by a concurrent request while this one planned.
			replay, _, err := adjustmentByKey(ctx, tx, p.TenantID, idempotencyKey, requestHash)
			out = replay
			return err
		}
		if err != nil {
			return err
		}
		adj.CreatedAt = &createdAt
		if _, err := tx.Exec(ctx, `insert into cost_transaction (tenant_id, id, legal_entity_id, source_key, inventory_transaction_id,
				product_id, site_id, warehouse_id, valuation_type, valuation_ledger_id, kind, qty, amount_acct, currency, financial_date)
			values ($1, $2::uuid, $3::uuid, $4, nullif($5, '')::uuid, $6::uuid, $7::uuid, nullif($8, '')::uuid, $9, nullif($10, '')::uuid,
				'MANUAL_ADJ', 0, $11::numeric, $12, $13::date)`,
			p.TenantID, adj.CostTransactionID, adj.LegalEntityID, adjustmentSourcePrefix+adj.ID, adj.ReceiptTransactionID, adj.ProductID,
			adj.SiteID, adj.WarehouseID, adj.ValuationType, adj.ValuationLedgerID, plan.Delta.String(), adj.Currency,
			adj.FinancialDate); err != nil {
			return err
		}
		out, created = adj, true
		if adj.Kind != domain.AdjustOnHand || adj.ValuationLedgerID != "" || !plan.HasUnitCost || plan.OldUnitCost.Equal(plan.NewUnitCost) {
			return nil
		}
		oldCost := adj.OldUnitCost
		evt, err := events.New(CostChangedEventType, eventSource, adj.ProductID, p.TenantID, costChanged{ProductID: adj.ProductID,
			SiteID: adj.SiteID, AdjustmentID: adj.ID, WarehouseID: adj.WarehouseID, ValuationType: adj.ValuationType,
			Currency: adj.Currency, OldCost: &oldCost, NewCost: adj.NewUnitCost, EffectiveFrom: adj.FinancialDate, OnHandQty: adj.Qty,
			RevaluationAmount: adj.Delta})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, costAggType, OnHandKey(adj.ProductID, adj.SiteID), evt)
	})
	if err != nil {
		return Adjustment{}, false, err
	}
	return out, created, nil
}

const adjustmentColumns = `id::text, legal_entity_id::text, kind, mode, product_id::text, site_id::text, coalesce(warehouse_id::text, ''),
	valuation_type, coalesce(valuation_ledger_id::text, ''), coalesce(receipt_transaction_id::text, ''), currency,
	financial_date::text, reason, coalesce(unit_cost::text, ''), coalesce(amount::text, ''), qty::text, old_value::text,
	new_value::text, delta::text, coalesce(old_unit_cost::text, ''), coalesce(new_unit_cost::text, ''),
	cost_transaction_id::text, created_at, request_hash`

// scanAdjustment reads a stored adjustment and prints its decimals the way a
// fresh one is printed, so a replay or a read returns what the post returned.
func scanAdjustment(row pgx.Row) (Adjustment, string, error) {
	var a Adjustment
	var createdAt time.Time
	var hash string
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.Kind, &a.Mode, &a.ProductID, &a.SiteID, &a.WarehouseID, &a.ValuationType,
		&a.ValuationLedgerID, &a.ReceiptTransactionID, &a.Currency, &a.FinancialDate, &a.Reason, &a.UnitCost, &a.Amount, &a.Qty,
		&a.OldValue, &a.NewValue, &a.Delta, &a.OldUnitCost, &a.NewUnitCost, &a.CostTransactionID, &createdAt, &hash); err != nil {
		return Adjustment{}, "", err
	}
	a.CreatedAt = &createdAt
	scale := money.MinorUnits(a.Currency)
	for _, f := range []struct {
		dst    *string
		amount bool
	}{{&a.UnitCost, false}, {&a.Amount, false}, {&a.Qty, false}, {&a.OldValue, true}, {&a.NewValue, true}, {&a.Delta, true},
		{&a.OldUnitCost, false}, {&a.NewUnitCost, false}} {
		if *f.dst == "" {
			continue
		}
		d, err := decimal.NewFromString(*f.dst)
		if err != nil {
			return Adjustment{}, "", err
		}
		if f.amount {
			*f.dst = fmtAmount(d, scale)
		} else {
			*f.dst = d.String()
		}
	}
	return a, hash, nil
}

// adjustmentByKey returns the adjustment stored under an Idempotency-Key, if
// any; one stored for another request is refused.
func adjustmentByKey(ctx context.Context, tx pgx.Tx, tenantID, key, requestHash string) (Adjustment, bool, error) {
	a, hash, err := scanAdjustment(tx.QueryRow(ctx, `select `+adjustmentColumns+` from cost_adjustment
		where tenant_id = $1 and idempotency_key = $2`, tenantID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Adjustment{}, false, nil
	}
	if err != nil {
		return Adjustment{}, false, err
	}
	if hash != requestHash {
		return Adjustment{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse,
			"Idempotency-Key was already used with a different request body")
	}
	return a, true, nil
}

// GetAdjustment returns one adjustment in the caller's scope.
func (s *Postgres) GetAdjustment(ctx context.Context, id string) (Adjustment, error) {
	p, _ := tenant.FromContext(ctx)
	var out Adjustment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, _, err := scanAdjustment(tx.QueryRow(ctx, `select `+adjustmentColumns+` from cost_adjustment where tenant_id = $1 and id = $2`,
			p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeAdjustmentNotFound, "cost adjustment not found")
		}
		out = a
		return err
	})
	return out, err
}

// ListAdjustments lists adjustments in the caller's scope, newest financial
// date first. after is the Next of the previous page ("date|id").
func (s *Postgres) ListAdjustments(ctx context.Context, f AdjustmentFilter, after string, limit int) (AdjustmentPage, error) {
	p, _ := tenant.FromContext(ctx)
	afterDate, afterID, _ := strings.Cut(after, "|")
	page := AdjustmentPage{Items: []Adjustment{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+adjustmentColumns+` from cost_adjustment
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or product_id::text = $3) and ($4 = '' or kind = $4)
			  and (not $5::boolean or (financial_date, id) < ($6::date, $7::uuid))
			order by financial_date desc, id desc
			limit $8`,
			p.TenantID, f.LegalEntityID, f.ProductID, f.Kind, after != "", nullDate(afterDate), nullUUID(afterID), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, _, err := scanAdjustment(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return AdjustmentPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items, page.Next = page.Items[:limit], last.FinancialDate+"|"+last.ID
	}
	return page, nil
}
