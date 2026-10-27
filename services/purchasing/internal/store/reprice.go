package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// RepriceFilter narrows the POs a reprice run visits; empty fields do not filter.
// TradeAgreementID keeps POs with a line priced from that trade agreement row.
type RepriceFilter struct {
	LegalEntityID    string
	VendorID         string
	TradeAgreementID string
}

// RepriceOutcome is what applying a reprice plan wrote. VersionNo is the PO
// version the moved prices went into (the current one when only invoiced lines
// were handed over); ReapprovalRequired means it waits for re-approval.
// Replayed means the Idempotency-Key was used before and nothing was written.
type RepriceOutcome struct {
	Order              PurchaseOrder
	VersionNo          int
	ReapprovalRequired bool
	Replayed           bool
}

// RepriceCandidates returns the ids of CONFIRMED POs with an open line priced
// from a trade agreement that is not drawn against a purchase agreement
// (FR-17.22), in id order after the cursor.
func (s *Postgres) RepriceCandidates(ctx context.Context, f RepriceFilter, after string, limit int) (Page[string], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	var items []string
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text from purchase_order o
			where o.tenant_id = $1 and o.approval_status = 'CONFIRMED'
			and ($2 = '' or o.legal_entity_id::text = $2) and ($3 = '' or o.vendor_id::text = $3)
			and ($4::uuid is null or o.id > $4::uuid)
			and exists (select 1 from purchase_order_line l
				where l.tenant_id = o.tenant_id and l.purchase_order_id = o.id and l.status = 'OPEN'
				and l.agreement_line_id is null and l.product_id is not null and l.price_source like 'TRADE_AGREEMENT:%'
				and ($5 = '' or l.price_source = 'TRADE_AGREEMENT:' || $5))
			order by o.id limit $6`,
			p.TenantID, f.LegalEntityID, f.VendorID, nullable(after), f.TradeAgreementID, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return Page[string]{}, err
	}
	if items == nil {
		items = []string{}
	}
	return pageOf(items, limit, func(id string) string { return id }), nil
}

// RepricePurchaseOrder applies a reprice plan (FR-17.22) in one transaction.
// next is the PO with the moved lines already priced (the API builds it); it
// goes through the PO edit path under the row lock and If-Match, so the moved
// prices become the next version (FR-17.8), IN_REVIEW when the reapproval
// rules fire, and invoicing plans follow the new line amounts (BR-17.17).
// Invoiced lines in the plan are marked handed over (BR-17.18), and
// po.repriced.v1 is written to the outbox. A replayed Idempotency-Key returns
// the PO as it is now with Replayed set.
func (s *Postgres) RepricePurchaseOrder(ctx context.Context, id string, ifMatch int, next PurchaseOrder,
	plan domain.RepricePlan, idem Idem) (RepriceOutcome, error) {
	var out RepriceOutcome
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, id)
		if err != nil {
			return err
		}
		if replay {
			out.Order, err = loadPurchaseOrder(ctx, tx, existing)
			out.Replayed, out.VersionNo = true, out.Order.VersionNo
			return err
		}
		if err := lockPurchaseOrder(ctx, tx, id); err != nil {
			return err
		}
		cur, err := loadPurchaseOrder(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := checkRowVersion(cur, ifMatch); err != nil {
			return err
		}
		if err := domain.CheckRepriceable(cur.ApprovalStatus); err != nil {
			return err
		}
		out.Order, out.VersionNo = cur, cur.VersionNo
		if len(plan.Changes) == 0 && len(plan.Invoiced) == 0 {
			return nil // nothing moves and nothing is handed over
		}
		if len(plan.Changes) > 0 {
			var res editResult
			if out.Order, res, err = editLocked(ctx, tx, cur, ifMatch, next); err != nil {
				return err
			}
			if res.versionNo > 0 {
				out.VersionNo, out.ReapprovalRequired = res.versionNo, res.reapproval
			}
		}
		if len(plan.Invoiced) > 0 {
			if err := markHandedOver(ctx, tx, id, plan.Invoiced); err != nil {
				return err
			}
			if len(plan.Changes) == 0 {
				// The hand-over alone still changes what the next run plans.
				if err := bumpRowVersion(ctx, tx, id); err != nil {
					return err
				}
			}
			if out.Order, err = loadPurchaseOrder(ctx, tx, id); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, "purchase_order", id, purchasingevents.PORepriced, repricedData(out, plan))
	})
	return out, err
}

func markHandedOver(ctx context.Context, tx pgx.Tx, poID string, invoiced []domain.RepriceChange) error {
	p, _ := tenant.FromContext(ctx)
	for _, c := range invoiced {
		if _, err := tx.Exec(ctx, `update purchase_order_line set invoiced_reprice_source = $4
			where tenant_id = $1 and purchase_order_id = $2 and line_no = $3`, p.TenantID, poID, c.LineNo, c.NewSource); err != nil {
			return err
		}
	}
	return nil
}

func bumpRowVersion(ctx context.Context, tx pgx.Tx, poID string) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `update purchase_order set row_version = row_version + 1, updated_at = now()
		where tenant_id = $1 and id = $2`, p.TenantID, poID)
	return err
}

func repricedData(out RepriceOutcome, plan domain.RepricePlan) purchasingevents.PORepricedData {
	line := func(c domain.RepriceChange) purchasingevents.PORepricedLine {
		return purchasingevents.PORepricedLine{LineNo: c.LineNo, Qty: c.Qty.String(), OldPrice: c.OldPrice.String(),
			OldPriceUnit: c.OldPriceUnit.String(), NewPrice: c.NewPrice.String(), NewPriceUnit: c.NewPriceUnit.String(),
			OldAmount: c.OldAmount.String(), NewAmount: c.NewAmount.String(), Delta: c.Delta.String(),
			OldSource: c.OldSource, NewSource: c.NewSource}
	}
	data := purchasingevents.PORepricedData{POID: out.Order.ID, Number: out.Order.Number, VersionNo: out.VersionNo,
		ReapprovalRequired: out.ReapprovalRequired, Currency: out.Order.Currency, TotalDelta: plan.TotalDelta.String(),
		Lines:         make([]purchasingevents.PORepricedLine, 0, len(plan.Changes)),
		InvoicedLines: make([]purchasingevents.PORepricedLine, 0, len(plan.Invoiced))}
	for _, c := range plan.Changes {
		data.Lines = append(data.Lines, line(c))
	}
	for _, c := range plan.Invoiced {
		data.InvoicedLines = append(data.InvoicedLines, line(c))
	}
	return data
}
