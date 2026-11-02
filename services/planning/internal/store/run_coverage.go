package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// RunCoverage is the stored site-scope coverage of one item with the settings
// of its coverage group, as a plan run reads it for an item whose run input
// leaves coverage, order type or lead time out (FR-22.2, FR-22.5, data model
// ItemCoverage 4.22.4).
type RunCoverage struct {
	ItemID            string
	GroupCode         string
	CoverageCode      string
	CoverageFenceDays int
	NegativeDays      int
	PositiveDays      int
	PeriodDays        int
	FreezeFenceDays   int
	ActionFenceDays   int
	FuturesFenceDays  int
	FirmingFenceDays  int
	AutoFirm          bool
	// SafetyStockPolicy is the coverage group's policy (BR-22.5).
	SafetyStockPolicy string
	// MinQty and MaxQty are the item coverage's minimum and maximum
	// (min/max coverage); exact decimals, never floats.
	MinQty decimal.Decimal
	MaxQty decimal.Decimal
	// MarginDays is the item coverage's safety margin on receipt.
	MarginDays int
	// IssueMarginDays and ReorderMarginDays are the FR-22.5 issue and reorder margins.
	IssueMarginDays, ReorderMarginDays int
	// LeadTimeDays is the item coverage's lead-time override; nil means none.
	LeadTimeDays     *int
	PlannedOrderType string
	// VendorID is the item coverage's default vendor (FR-22.3, FR-22.5); empty when none.
	VendorID string
}

// RunCoverages returns the stored site-scope coverage of the given items at
// siteID, keyed by item id; items without one are absent. Warehouse and
// subcontractor scopes are never returned: those are planned in their own
// scope (FR-22.18), not at site level. The scope key of item_coverage allows
// at most one site-scope row per item and site. itemIDs must be UUIDs.
func (s *Postgres) RunCoverages(ctx context.Context, siteID, planEntity string, itemIDs []string) (map[string]RunCoverage, error) {
	out := map[string]RunCoverage{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select c.item_id::text, g.code, g.coverage_code, g.coverage_time_fence_days, g.negative_days,
			       g.positive_days, g.period_days, g.freeze_fence_days, g.action_fence_days,
			       g.futures_fence_days, g.firming_fence_days, g.auto_firm, g.safety_stock_policy, c.min_qty::text, c.max_qty::text,
			       c.safety_margin_receipt_days, c.lead_time_days, c.planned_order_type,
			       c.safety_margin_issue_days, c.safety_margin_reorder_days, coalesce(c.vendor_id::text, '')
			from item_coverage c
			join coverage_group g on g.tenant_id = c.tenant_id and g.id = c.coverage_group_id
			where c.tenant_id = $1 and c.site_id = $2::uuid and c.planning_scope = 'site'
			  and c.item_id = any($3::uuid[])
				  and ($4::text = '' or c.legal_entity_id::text = $4::text)`,
			p.TenantID, siteID, itemIDs, planEntity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c RunCoverage
			var minQty, maxQty string
			if err := rows.Scan(&c.ItemID, &c.GroupCode, &c.CoverageCode, &c.CoverageFenceDays, &c.NegativeDays,
				&c.PositiveDays, &c.PeriodDays, &c.FreezeFenceDays, &c.ActionFenceDays, &c.FuturesFenceDays,
				&c.FirmingFenceDays, &c.AutoFirm, &c.SafetyStockPolicy, &minQty, &maxQty, &c.MarginDays, &c.LeadTimeDays,
				&c.PlannedOrderType, &c.IssueMarginDays, &c.ReorderMarginDays, &c.VendorID); err != nil {
				return err
			}
			if c.MinQty, err = decimal.NewFromString(minQty); err != nil {
				return err
			}
			if c.MaxQty, err = decimal.NewFromString(maxQty); err != nil {
				return err
			}
			out[c.ItemID] = c
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
