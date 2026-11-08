package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

// maxRepriceOrders bounds one reprice run, and the confirmed orders it lists
// as skipped; a run that finds more orders to reprice says so (More) and the
// caller repeats it at the new ETag.
const maxRepriceOrders = 200

// RepricedOrder is one released order whose release line took the agreement price.
type RepricedOrder struct {
	OrderID     string          `json:"orderId"`
	OrderNumber string          `json:"orderNumber"`
	OldPrice    decimal.Decimal `json:"oldPrice"`
	NewPrice    decimal.Decimal `json:"newPrice"`
	NetTotal    decimal.Decimal `json:"netTotal"`
	Version     int             `json:"version"`
}

// SkippedOrder is a released order a reprice run left alone, with the reason.
type SkippedOrder struct {
	OrderID     string `json:"orderId"`
	OrderNumber string `json:"orderNumber"`
	Reason      string `json:"reason"`
}

// RepriceResult is the outcome of RepriceAgreementOrders. Agreement carries the
// new version when any order was repriced.
type RepriceResult struct {
	Agreement Agreement       `json:"agreement"`
	Repriced  []RepricedOrder `json:"repriced"`
	Skipped   []SkippedOrder  `json:"skipped"`
	// More is true when the run stopped at the per-run limit; run it again.
	More bool `json:"more"`
}

// RepriceAgreementOrders puts the agreement's current price on the release line
// (line 1) of every released order that is still DRAFT or OPEN (BR-27.9:
// agreement lines re-price only when the agreement price changes; a revised
// price otherwise applies to later releases only). It runs at the agreement's
// ETag, so the caller reprices at the price it saw. Each order goes through the
// same path as PATCH: line net and order total are recomputed, a version with
// the field diff is recorded and order.changed.v1 is written; the discount, the
// quantity and the commitment already drawn are not touched (the release keeps
// the price it drew at). A DRAFT or OPEN order is credit-checked when it is
// confirmed, as after any PATCH of it. A confirmed order priced differently is
// reported under Skipped, not changed: its price changes go through change
// management. Only orders that still need the price are taken, at most
// maxRepriceOrders per run, so a repeated run moves on and ends (More false).
// Orders are locked in id order before the agreement, the same order PATCH and
// cancel use, so a run cannot deadlock a change. A run that reprices nothing
// changes nothing and keeps the agreement's version; otherwise a REPRICED
// version and agreement.repriced.v1 commit with the orders. A closed agreement
// is 422 SLS-0052.
func (s *Postgres) RepriceAgreementOrders(ctx context.Context, id string, version int, reason, actor string, now time.Time) (RepriceResult, error) {
	return s.repriceAgreementOrders(ctx, id, version, reason, actor, now, maxRepriceOrders)
}

func (s *Postgres) repriceAgreementOrders(ctx context.Context, id string, version int, reason, actor string, now time.Time, limit int) (RepriceResult, error) {
	p, _ := tenant.FromContext(ctx)
	if len(reason) > agreementReasonMaxSize {
		return RepriceResult{}, problem(http.StatusBadRequest, "COM-0001", "reason is too long")
	}
	out := RepriceResult{Repriced: []RepricedOrder{}, Skipped: []SkippedOrder{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		peek, err := loadAgreement(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if peek.Version != version {
			return versionConflict()
		}
		if peek.Status == domain.AgreementClosed {
			return agreementClosed(peek)
		}
		orderIDs, err := repriceCandidates(ctx, tx, p.TenantID, peek, []string{domain.OrderDraft, domain.OrderOpen}, limit+1)
		if err != nil {
			return err
		}
		if len(orderIDs) > limit {
			orderIDs, out.More = orderIDs[:limit], true
		}
		var diff []domain.FieldChange
		for _, c := range orderIDs {
			oid := c.id
			o, err := loadOrder(ctx, tx, p.TenantID, oid, true)
			if err != nil {
				return err
			}
			// The candidate list was read without locks: check again under
			// the order's lock.
			var line *Line
			for i := range o.Lines {
				if o.Lines[i].LineNo == 1 {
					line = &o.Lines[i]
				}
			}
			skip := ""
			switch {
			case o.Status != domain.OrderDraft && o.Status != domain.OrderOpen:
				continue
			case line == nil || !domain.LineChangeable(line.LineStatus) || line.ProductID != peek.ProductID:
				skip = "the release line (line 1) is not an open line of the agreement's product"
			case line.UnitPrice.Equal(peek.UnitPrice):
				continue
			}
			if skip != "" {
				out.Skipped = append(out.Skipped, SkippedOrder{OrderID: o.ID, OrderNumber: o.OrderNumber, Reason: skip})
				continue
			}
			newPrice, oldPrice := peek.UnitPrice, line.UnitPrice
			changed, err := applyChangeTx(ctx, tx, p.TenantID, o, oid, ChangeRequest{
				Reason: fmt.Sprintf("agreement %s price %s", peek.AgreementNumber, newPrice),
				Lines:  []LineChange{{LineNo: 1, UnitPrice: &newPrice}},
			}, actor, now)
			if err != nil {
				return err
			}
			out.Repriced = append(out.Repriced, RepricedOrder{OrderID: oid, OrderNumber: o.OrderNumber, OldPrice: oldPrice, NewPrice: newPrice,
				NetTotal: changed.NetTotal, Version: changed.Version})
			diff = append(diff, domain.FieldChange{Field: "order " + o.OrderNumber + " unitPrice", Old: oldPrice.String(), New: newPrice.String()})
		}
		confirmed, err := repriceCandidates(ctx, tx, p.TenantID, peek, []string{domain.OrderConfirmed}, limit)
		if err != nil {
			return err
		}
		for _, c := range confirmed {
			out.Skipped = append(out.Skipped, SkippedOrder{OrderID: c.id, OrderNumber: c.number, Reason: "order is CONFIRMED: change its price with a change request"})
		}
		a, err := loadAgreement(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if a.Version != version {
			return versionConflict()
		}
		out.Agreement = a
		if len(out.Repriced) == 0 {
			return nil
		}
		v := a.Version + 1
		if _, err := tx.Exec(ctx, `update sales_agreement set version = $3, updated_at = $4 where tenant_id = $1 and id = $2`, p.TenantID, id, v, now); err != nil {
			return err
		}
		if err := insertAgreementVersion(ctx, tx, p.TenantID, a, v, "REPRICED", actor, reason, diff, now); err != nil {
			return err
		}
		data := lifecycleData(a, a.Status, a.Commitment, a.OverdeliveryPct, a.Released, a.UnitPrice, diff, v, actor)
		data.Reason = reason
		for _, r := range out.Repriced {
			data.RepricedOrders = append(data.RepricedOrders, salesevents.RepricedOrder{SalesOrderID: r.OrderID, OldPrice: r.OldPrice.String(), NewPrice: r.NewPrice.String()})
		}
		if err := enqueue(ctx, tx, "agreement", id, salesevents.AgreementRepriced, p.TenantID, data); err != nil {
			return err
		}
		out.Agreement, err = loadAgreement(ctx, tx, p.TenantID, id, false)
		return err
	})
	if err != nil {
		return RepriceResult{}, err
	}
	return out, nil
}

// repriceCandidate is a released order whose release line is not at the
// agreement price.
type repriceCandidate struct{ id, number string }

// repriceCandidates lists, in id order and at most limit, the orders released
// from agreement a that are in one of statuses and whose release line (line 1)
// is an open or reserved line of the agreement's product priced differently
// from the agreement. Orders already at the price, cancelled or settled ones
// are not listed, so a repeated run moves past what it has done.
func repriceCandidates(ctx context.Context, tx pgx.Tx, tenantID string, a Agreement, statuses []string, limit int) ([]repriceCandidate, error) {
	rows, err := tx.Query(ctx, `select o.id::text, o.order_number from sales_order o
		join sales_order_line l on l.tenant_id = o.tenant_id and l.order_id = o.id and l.line_no = 1
		where o.tenant_id = $1 and o.status = any($3::text[])
		  and o.id in (select r.sales_order_id from agreement_release r where r.tenant_id = $1 and r.agreement_id = $2)
		  and l.line_status in ('OPEN', 'RESERVED') and l.product_id = $4 and l.unit_price <> $5::numeric
		order by o.id limit $6`, tenantID, a.ID, statuses, a.ProductID, a.UnitPrice.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repriceCandidate
	for rows.Next() {
		var c repriceCandidate
		if err := rows.Scan(&c.id, &c.number); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
