package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// Split is a `:split` request (FR-21.15). MinQty and Multiple are the item's
// minimum order quantity and multiple (zero: no rule); the caller passes them
// until released-product settings (wave 8) are read here.
type Split struct {
	OrderID  string
	IfMatch  int
	SplitQty decimal.Decimal
	MinQty   decimal.Decimal
	Multiple decimal.Decimal
	// Allocation decides, for a Started order, whether the posted consumption,
	// hours and WIP move in proportion (default) or stay on the original (BR-21.11).
	Allocation     domain.Allocation
	Actor          string
	TraceID        string
	IdempotencyKey string
	RequestHash    string
}

// SplitResult is the original order after the split and the new order.
type SplitResult struct {
	Original Order `json:"original"`
	New      Order `json:"new"`
	Replayed bool  `json:"-"`
}

// SplitOrder splits a Created to Started order (FR-21.15). The original keeps
// quantity − splitQty; the new order receives splitQty, the same header, BOM and
// route snapshot and status, a number from the legal entity's sequence, origin
// Split with originId and splitFromOrderId set to the original. Both orders are
// re-exploded for their quantity and, once estimated, re-estimated. The new
// order is announced by order.created and the original by order.updated. The
// Idempotency-Key is stored on the new order: a replay with the same request
// returns the pair with replayed=true, any other request is refused.
// A Started order also moves its share of the posted consumption, route hours
// and WIP to the new order (BR-21.11) through a pair of posted split-transfer
// journals, unless the allocation keeps everything on the original.
func (s *Postgres) SplitOrder(ctx context.Context, in Split) (SplitResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out SplitResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var replayID, replayHash string
		var replayFrom *string
		err := tx.QueryRow(ctx, `select id::text, request_hash, split_from_order_id::text from production_order where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&replayID, &replayHash, &replayFrom)
		if err == nil {
			if replayFrom == nil || *replayFrom != in.OrderID || replayHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another request")
			}
			out.Replayed = true
			if out.New, err = loadOrder(ctx, tx, p.TenantID, replayID); err != nil {
				return err
			}
			out.Original, err = loadOrder(ctx, tx, p.TenantID, in.OrderID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var used bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_status_log where tenant_id = $1 and idempotency_key = $2)`,
			p.TenantID, in.IdempotencyKey).Scan(&used); err != nil {
			return err
		}
		if used {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another request")
		}

		h, err := lockOrder(ctx, tx, p.TenantID, in.OrderID, in.IfMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckSplittable(h.status); err != nil {
			return domainProblem(err)
		}
		if err := checkNoCoProducts(ctx, tx, p.TenantID, in.OrderID); err != nil {
			return err
		}
		lines, err := loadLines(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		ops, err := loadOperations(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		plan, err := domain.SplitOrder(h.quantity, in.SplitQty, in.MinQty, in.Multiple, lines, ops)
		if err != nil {
			return domainProblem(err)
		}
		var transfer domain.Transfer
		if h.status == domain.Started {
			if err := checkStartedSplit(ctx, tx, p.TenantID, h); err != nil {
				return err
			}
			if in.Allocation != domain.AllocKeepOnOriginal {
				if transfer, err = planTransfer(ctx, tx, p.TenantID, h, in.SplitQty, lines, ops); err != nil {
					return err
				}
			}
		}

		var seq int64
		if err := tx.QueryRow(ctx, `insert into production_order_sequence (tenant_id, legal_entity_id, next_value) values ($1, $2, 2)
			on conflict (tenant_id, legal_entity_id) do update set next_value = production_order_sequence.next_value + 1
			returning next_value - 1`, p.TenantID, h.legalEntityID).Scan(&seq); err != nil {
			return err
		}
		newID := ids.New()
		number := fmt.Sprintf("PO-%06d", seq)
		// A rework order stays a rework order of the same reference (its settlement
		// depends on it); every other split is origin Split from the original.
		if _, err := tx.Exec(ctx, `insert into production_order (tenant_id, id, legal_entity_id, order_number, product_id, site_id, warehouse_id, currency,
				quantity, delivery_date, origin_type, origin_id, idempotency_key, request_hash, item_group, rework_reference_type, rework_reference_id,
				status, split_from_order_id, order_bom_modified)
			select tenant_id, $2, legal_entity_id, $3, product_id, site_id, warehouse_id, currency,
				$4::numeric, delivery_date, case when origin_type = 'Rework' then 'Rework' else 'Split' end,
				case when origin_type = 'Rework' then origin_id else id end, $5, $6, item_group, rework_reference_type, rework_reference_id,
				status, id, order_bom_modified
			from production_order where tenant_id = $1 and id = $7`,
			p.TenantID, newID, number, plan.SplitQty.String(), in.IdempotencyKey, in.RequestHash, in.OrderID); err != nil {
			return err
		}
		if err := insertLines(ctx, tx, p.TenantID, newID, plan.NewLines); err != nil {
			return err
		}
		if err := insertOperations(ctx, tx, p.TenantID, newID, plan.NewOps); err != nil {
			return err
		}
		requested, err := requestedSubcontracts(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		if err := writeQuantities(ctx, tx, p.TenantID, in.OrderID, plan.OriginalLines, plan.OriginalOps); err != nil {
			return err
		}
		// FR-21.10: the original keeps its PO at the smaller quantity.
		if err := settleSubcontracts(ctx, tx, p.TenantID, in.OrderID, in.Actor, requested, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update production_order set quantity = $3, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, in.OrderID, plan.OriginalQty.String()); err != nil {
			return err
		}
		if h.status != domain.Created {
			for _, id := range []string{in.OrderID, newID} {
				if err := replaceEstimate(ctx, tx, p.TenantID, id); err != nil {
					return err
				}
			}
		}
		for _, row := range []struct{ id, key string }{{in.OrderID, in.IdempotencyKey}, {newID, in.IdempotencyKey + "#" + newID}} {
			if err := logCommand(ctx, tx, p.TenantID, h.legalEntityID, row.id, domain.CmdSplit, h.status, h.status, in.Actor, in.TraceID, row.key, in.RequestHash); err != nil {
				return err
			}
		}
		// FR-21.10: the new order of a released split needs its own vendor PO; the
		// original keeps its request (the PO quantity change is purchasing's).
		var requests []prodevents.SubcontractPORequestedData
		if h.status == domain.Released || h.status == domain.Started {
			if requests, err = claimSubcontractRequests(ctx, tx, p.TenantID, newID, in.Actor); err != nil {
				return err
			}
		}
		if out.Original, err = loadOrder(ctx, tx, p.TenantID, in.OrderID); err != nil {
			return err
		}
		if out.New, err = loadOrder(ctx, tx, p.TenantID, newID); err != nil {
			return err
		}
		created := prodevents.OrderCreatedData{
			OrderID: newID, OrderNumber: number, ProductID: out.New.ProductID, Quantity: out.New.Quantity.String(), SiteID: out.New.SiteID,
			DeliveryDate: out.New.DeliveryDate, OriginType: out.New.OriginType, OriginID: out.New.OriginID,
		}
		if ref := out.New.ReworkReference; ref != nil {
			created.ReworkReference = &prodevents.ReworkReference{Type: string(ref.Type), ID: ref.ID}
		}
		if err := enqueue(ctx, tx, newID, prodevents.OrderCreated, p.TenantID, in.Actor, created); err != nil {
			return err
		}
		if err := enqueueSubcontractRequests(ctx, tx, p.TenantID, newID, in.Actor, requests); err != nil {
			return err
		}
		// The transfer follows order.created, so a consumer knows the new order
		// before the journals that name it; the orders are read again with the moved figures.
		if !transfer.Empty() {
			if err := applyTransfer(ctx, tx, p.TenantID, h, newID, transfer, ops, in); err != nil {
				return err
			}
			if out.Original, err = loadOrder(ctx, tx, p.TenantID, in.OrderID); err != nil {
				return err
			}
			if out.New, err = loadOrder(ctx, tx, p.TenantID, newID); err != nil {
				return err
			}
		}
		return enqueueUpdated(ctx, tx, p.TenantID, in.Actor, out.Original)
	})
	return out, err
}

// MergeRef is one order of a merge with the version the caller read.
type MergeRef struct {
	OrderID string
	Version int
}

// Merge is a merge request: the first order survives and the others move into it.
type Merge struct {
	Orders         []MergeRef
	Actor          string
	TraceID        string
	IdempotencyKey string
	RequestHash    string
}

// MergeResult is the survivor and the ids of the orders merged into it.
type MergeResult struct {
	Survivor Order    `json:"survivor"`
	Merged   []string `json:"merged"`
	Replayed bool     `json:"-"`
}

// MergeOrders merges orders of one product, BOM/route snapshot and site that
// are Created to Released and have no posted journals into the first one
// (FR-21.15, PROD-0016). The survivor takes the summed quantity, is
// re-exploded and, once estimated, re-estimated; the others become Cancelled
// with mergedIntoOrderId set. Rows are locked in id order so concurrent
// merges cannot deadlock. A replay of the same Idempotency-Key and request
// returns the survivor with replayed=true.
func (s *Postgres) MergeOrders(ctx context.Context, in Merge) (MergeResult, error) {
	p, _ := tenant.FromContext(ctx)
	if len(in.Orders) < 2 {
		return MergeResult{}, domainProblem(&domain.Error{Code: domain.CodeMergeRefused, Detail: "a merge needs at least two orders"})
	}
	out := MergeResult{Merged: []string{}}
	for _, ref := range in.Orders[1:] {
		out.Merged = append(out.Merged, ref.OrderID)
	}
	survivorID := in.Orders[0].OrderID
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var loggedOrder, loggedHash string
		err := tx.QueryRow(ctx, `select order_id::text, request_hash from production_status_log where tenant_id = $1 and idempotency_key = $2`,
			p.TenantID, in.IdempotencyKey).Scan(&loggedOrder, &loggedHash)
		if err == nil {
			if loggedOrder != survivorID || loggedHash != in.RequestHash {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another request")
			}
			out.Replayed = true
			out.Survivor, err = loadOrder(ctx, tx, p.TenantID, survivorID)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		sorted := append([]MergeRef(nil), in.Orders...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].OrderID < sorted[j].OrderID })
		heads := map[string]orderHead{}
		for _, ref := range sorted {
			h, err := lockOrder(ctx, tx, p.TenantID, ref.OrderID, ref.Version)
			if err != nil {
				return err
			}
			heads[ref.OrderID] = h
		}
		if err := checkMergeJournals(ctx, tx, p.TenantID, survivorID, out.Merged); err != nil {
			return err
		}
		if err := checkNoCoProducts(ctx, tx, p.TenantID, append([]string{survivorID}, out.Merged...)...); err != nil {
			return err
		}
		inputs := make([]domain.MergeInput, 0, len(in.Orders))
		// The survivor carries the merged orders' BOM, so it is flagged when any of them was edited (FR-21.18).
		modified := false
		for _, ref := range in.Orders {
			o, err := loadOrder(ctx, tx, p.TenantID, ref.OrderID)
			if err != nil {
				return err
			}
			modified = modified || o.OrderBOMModified
			inputs = append(inputs, domain.MergeInput{ID: o.ID, LegalEntityID: o.LegalEntityID, ProductID: o.ProductID, SiteID: o.SiteID,
				WarehouseID: o.WarehouseID, Currency: o.Currency, ItemGroup: o.ItemGroup, ReworkReference: o.ReworkReference,
				Status: o.Status, Quantity: o.Quantity, Lines: o.Lines, Operations: o.Operations})
		}
		merged, err := domain.MergeOrders(inputs)
		if err != nil {
			return domainProblem(err)
		}

		survivor := heads[survivorID]
		requested, err := requestedSubcontracts(ctx, tx, p.TenantID, survivorID)
		if err != nil {
			return err
		}
		if err := writeQuantities(ctx, tx, p.TenantID, survivorID, merged.Lines, merged.Operations); err != nil {
			return err
		}
		// FR-21.10: the survivor's PO grows with the merged quantity.
		if err := settleSubcontracts(ctx, tx, p.TenantID, survivorID, in.Actor, requested, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update production_order set quantity = $3, order_bom_modified = order_bom_modified or $4, version = version + 1,
				updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, survivorID, merged.Quantity.String(), modified); err != nil {
			return err
		}
		if survivor.status != domain.Created {
			if err := replaceEstimate(ctx, tx, p.TenantID, survivorID); err != nil {
				return err
			}
		}
		if err := logCommand(ctx, tx, p.TenantID, survivor.legalEntityID, survivorID, domain.CmdMerge, survivor.status, survivor.status,
			in.Actor, in.TraceID, in.IdempotencyKey, in.RequestHash); err != nil {
			return err
		}
		for _, id := range out.Merged {
			h := heads[id]
			// FR-21.10: an order merged away withdraws its vendor POs.
			cancelled, err := requestedSubcontracts(ctx, tx, p.TenantID, id)
			if err != nil {
				return err
			}
			if err := settleSubcontracts(ctx, tx, p.TenantID, id, in.Actor, cancelled, true); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update production_order set status = 'Cancelled', merged_into_order_id = $3, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, id, survivorID); err != nil {
				return err
			}
			if err := logCommand(ctx, tx, p.TenantID, h.legalEntityID, id, domain.CmdMerge, h.status, domain.Cancelled,
				in.Actor, in.TraceID, in.IdempotencyKey+"#"+id, in.RequestHash); err != nil {
				return err
			}
			if err := enqueue(ctx, tx, id, prodevents.OrderStatusChanged, p.TenantID, in.Actor, prodevents.OrderStatusChangedData{
				OrderID: id, From: string(h.status), To: string(domain.Cancelled), Quantity: h.quantity.String(), QtyRAF: h.qtyRAF.String(),
				QtyError: h.qtyError.String(), Actor: in.Actor, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
			}); err != nil {
				return err
			}
		}
		if out.Survivor, err = loadOrder(ctx, tx, p.TenantID, survivorID); err != nil {
			return err
		}
		return enqueueUpdated(ctx, tx, p.TenantID, in.Actor, out.Survivor)
	})
	return out, err
}

// checkMergeJournals refuses a merge when any order has posted journals
// (PROD-0016), or an order that would be cancelled has draft journals.
func checkMergeJournals(ctx context.Context, tx pgx.Tx, tenantID, survivorID string, merged []string) error {
	rows, err := tx.Query(ctx, `select o.order_number, j.posted, j.order_id::text from production_journal j
			join production_order o on o.tenant_id = j.tenant_id and o.id = j.order_id
		where j.tenant_id = $1 and j.order_id = any($2::uuid[])
		order by j.posted desc, o.order_number`, tenantID, append([]string{survivorID}, merged...))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var number, orderID string
		var posted bool
		if err := rows.Scan(&number, &posted, &orderID); err != nil {
			return err
		}
		if posted {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMergeRefused, fmt.Sprintf("merge refused: order %s has posted journals", number))
		}
		if orderID != survivorID {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMergeRefused, fmt.Sprintf("merge refused: order %s has draft journals; delete them first", number))
		}
	}
	return rows.Err()
}

// logCommand writes a status-log row for a split or merge.
func logCommand(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, orderID string, cmd domain.Command, from, to domain.Status, actor, traceID, key, hash string) error {
	_, err := tx.Exec(ctx, `insert into production_status_log (tenant_id, id, legal_entity_id, order_id, command, from_status, to_status, actor, trace_id, idempotency_key, request_hash)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, tenantID, ids.New(), legalEntityID, orderID, string(cmd),
		string(from), string(to), actor, traceID, key, hash)
	return err
}

// enqueueUpdated publishes order.updated with the header of o.
func enqueueUpdated(ctx context.Context, tx pgx.Tx, tenantID, actor string, o Order) error {
	return enqueue(ctx, tx, o.ID, prodevents.OrderUpdated, tenantID, actor, prodevents.OrderUpdatedData{
		OrderID: o.ID, Quantity: o.Quantity.String(), DeliveryDate: o.DeliveryDate, Status: string(o.Status), Version: o.Version, Actor: actor,
	})
}

// Bulk is a bulk status update (FR-21.12): one command on up to 500 orders.
// RequestHash fingerprints the command for each order's idempotency key.
type Bulk struct {
	OrderIDs       []string
	Command        domain.Command
	ZeroOutputDuty bool
	Actor          string
	TraceID        string
	IdempotencyKey string
	RequestHash    string
}

// BulkItem is the result for one order of a bulk status update.
type BulkItem struct {
	OrderID  string        `json:"orderId"`
	OK       bool          `json:"ok"`
	Status   domain.Status `json:"status,omitempty"`
	Version  int           `json:"version,omitempty"`
	Replayed bool          `json:"replayed,omitempty"`
	Code     string        `json:"code,omitempty"`
	Detail   string        `json:"detail,omitempty"`
}

// BulkTransition applies the command to each order in its own transaction
// (continue on error), in request order, and reports every order's outcome.
// Each order runs at its current version under the key IdempotencyKey:orderId,
// so retrying a batch replays the orders already moved and retries the rest.
func (s *Postgres) BulkTransition(ctx context.Context, in Bulk) []BulkItem {
	items := make([]BulkItem, 0, len(in.OrderIDs))
	for _, id := range in.OrderIDs {
		item := BulkItem{OrderID: id}
		current, err := s.GetOrder(ctx, id)
		if err == nil {
			var moved Order
			moved, item.Replayed, err = s.Transition(ctx, Transition{OrderID: id, IfMatch: current.Version, Command: in.Command,
				ZeroOutputDuty: in.ZeroOutputDuty, Actor: in.Actor, TraceID: in.TraceID,
				IdempotencyKey: in.IdempotencyKey + ":" + id, RequestHash: in.RequestHash})
			if err == nil {
				item.OK, item.Status, item.Version = true, moved.Status, moved.Version
			}
		}
		if err != nil {
			item.Code, item.Detail = problemOf(err)
			if item.Code == "" {
				slog.Error("bulk status update failed", "err", err, "orderId", id, "correlationId", in.TraceID)
				item.Code, item.Detail = "COM-0500", "internal error"
			}
		}
		items = append(items, item)
	}
	return items
}

// problemOf returns the code and detail of a refusal, or empty for an unexpected error.
func problemOf(err error) (string, string) {
	var prob *httpx.Problem
	if errors.As(err, &prob) {
		return prob.Code, prob.Detail
	}
	return "", ""
}
