package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// applyQuotaToRun applies the quota arrangements stored for the run's site to
// the planned orders of the run (FR-22.17, BR-22.12): an order of an item with
// an arrangement valid on its delivery day goes to a source (a purchase order
// also gets the source's vendor), ahead of any default vendor. Items without
// an arrangement and orders no source can take are left as planned. A store
// error fails the run before anything is written.
func (a *API) applyQuotaToRun(ctx context.Context, planCode string, req runRequest, asOf time.Time, res domain.Result) (domain.Result, error) {
	if len(res.Orders) == 0 {
		return res, nil
	}
	seen := map[string]bool{}
	var itemIDs []string
	first, last := res.Orders[0].DeliveryDate, res.Orders[0].DeliveryDate
	for _, o := range res.Orders {
		if !seen[o.ItemID] {
			seen[o.ItemID] = true
			itemIDs = append(itemIDs, o.ItemID)
		}
		first, last = min(first, o.DeliveryDate), max(last, o.DeliveryDate)
	}
	entity, err := a.store.PlanLegalEntity(ctx, planCode, req.LegalEntityID)
	if err != nil {
		return res, err
	}
	quotas, err := a.store.RunQuotas(ctx, req.SiteID, entity, itemIDs, asOf.AddDate(0, 0, first), asOf.AddDate(0, 0, last))
	if err != nil || len(quotas) == 0 {
		return res, err
	}
	out, outcome, err := domain.ApplyQuota(asOf, res, quotas)
	if err != nil {
		return res, err
	}
	if outcome.Unassigned > 0 {
		// PLAN-0009: the orders stay as planned, without a source.
		slog.WarnContext(ctx, "planned orders no quota source could take", "code", "PLAN-0009", "plan", planCode,
			"site", req.SiteID, "count", outcome.Unassigned, "orders", outcome.UnassignedOrders)
	}
	return out, nil
}
