package store

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// CodeEditAfterRelease is the 403 of an edit on a Released order without the
// duty "Maintain order BOM after release" (FR-21.18).
const CodeEditAfterRelease = "AUTH-0003"

// OrderEdit is one order-specific BOM and route edit request (FR-21.18).
// ReleaseDuty says whether the caller holds "Maintain order BOM after release".
type OrderEdit struct {
	OrderID     string
	IfMatch     int
	Edits       domain.OrderEdits
	ReleaseDuty bool
	Actor       string
	TraceID     string
}

// OrderEditEntry is one audit row of an order's BOM and route edits: the line or
// operation before and after the edit (null before an add and after a remove).
type OrderEditEntry struct {
	OrderVersion int               `json:"orderVersion"`
	Seq          int               `json:"seq"`
	Target       domain.EditTarget `json:"target"`
	Op           domain.EditOp     `json:"op"`
	Key          string            `json:"key"`
	Before       json.RawMessage   `json:"before"`
	After        json.RawMessage   `json:"after"`
	Actor        string            `json:"actor"`
	TraceID      string            `json:"traceId"`
	OccurredAt   time.Time         `json:"occurredAt"`
}

// EditOrder applies order-specific BOM and route edits under the order row lock
// and If-Match (FR-21.18): the lines and operations are written in place (new
// lines go last), the operations re-planned, the planned cost re-estimated when
// the order has an estimate, the order flagged orderBomModified, one audit row
// written per edit and order.bomedited enqueued, all in one transaction.
func (s *Postgres) EditOrder(ctx context.Context, in OrderEdit) (Order, error) {
	p, _ := tenant.FromContext(ctx)
	var out Order
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = editOrderTx(ctx, tx, p.TenantID, in, true)
		return err
	})
	return out, err
}

// editOrderTx is the body of EditOrder inside the caller's transaction.
// markModified sets orderBomModified: an order-specific edit does, a change
// applied from an ECO (FR-21.16) does not, because the order then matches the
// new version again.
func editOrderTx(ctx context.Context, tx pgx.Tx, tenantID string, in OrderEdit, markModified bool) (Order, error) {
	p := tenant.Principal{TenantID: tenantID}
	var out Order
	err := func() error {
		h, err := lockOrder(ctx, tx, p.TenantID, in.OrderID, in.IfMatch)
		if err != nil {
			return err
		}
		if err := domain.CheckOrderEditable(h.status); err != nil {
			return domainProblem(err)
		}
		if h.status == domain.Released && !in.ReleaseDuty {
			return httpx.NewProblem(http.StatusForbidden, CodeEditAfterRelease,
				`editing the BOM or route of a Released order requires the duty "Maintain order BOM after release"`)
		}
		lines, err := loadLines(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		ops, err := loadOperations(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		inUse, err := journaledTargets(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		res, err := domain.ApplyOrderEdits(lines, ops, h.quantity, in.Edits, inUse)
		if err != nil {
			return domainProblem(err)
		}
		requested, err := requestedSubcontracts(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		if err := writeEditedLines(ctx, tx, p.TenantID, in.OrderID, lines, res.Lines); err != nil {
			return err
		}
		if err := writeEditedOperations(ctx, tx, p.TenantID, in.OrderID, ops, res.Operations); err != nil {
			return err
		}
		if h.status != domain.Created {
			// FR-21.18: planned cost is re-estimated after each edit (a Created order has no estimate yet).
			if err := replaceEstimate(ctx, tx, p.TenantID, in.OrderID); err != nil {
				return err
			}
		}
		version := h.version + 1
		for i, a := range res.Audit {
			before, after, err := auditImages(a)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into production_order_edit (tenant_id, id, legal_entity_id, order_id, edit_seq, target, op, target_key,
					before_image, after_image, order_version, actor, trace_id)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`, p.TenantID, ids.New(), h.legalEntityID, in.OrderID, i+1,
				string(a.Target), string(a.Op), a.Key, before, after, version, in.Actor, in.TraceID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update production_order set order_bom_modified = order_bom_modified or $4, version = $3, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, in.OrderID, version, markModified); err != nil {
			return err
		}
		if markModified {
			if err := flagPendingForReview(ctx, tx, p.TenantID, in.OrderID); err != nil {
				return err
			}
		}
		// FR-21.10: a subcontract operation added to (or turned on in) a released
		// order is requested like the ones release requested.
		// FR-21.10: an edit that changed, removed or switched off a requested
		// operation changes or cancels its PO first.
		if err := settleSubcontracts(ctx, tx, p.TenantID, in.OrderID, in.Actor, requested, false); err != nil {
			return err
		}
		var requests []prodevents.SubcontractPORequestedData
		if h.status == domain.Released {
			if requests, err = claimSubcontractRequests(ctx, tx, p.TenantID, in.OrderID, in.Actor); err != nil {
				return err
			}
		}
		if out, err = loadOrder(ctx, tx, p.TenantID, in.OrderID); err != nil {
			return err
		}
		data := prodevents.OrderBOMEditedData{OrderID: in.OrderID, Status: string(out.Status), Version: out.Version, Actor: in.Actor,
			Edits: make([]prodevents.EditApplied, 0, len(res.Audit)), Lines: make([]prodevents.RequirementLine, 0, len(out.Lines))}
		for _, a := range res.Audit {
			data.Edits = append(data.Edits, prodevents.EditApplied{Target: string(a.Target), Op: string(a.Op), Key: a.Key})
		}
		for _, l := range out.Lines {
			data.Lines = append(data.Lines, prodevents.RequirementLine{LineNo: l.LineNo, ItemID: l.ItemID, QtyRequired: l.QtyRequired.String(), Flushing: string(l.Flushing)})
		}
		if out.Status != domain.Created {
			planned := byGroup(out.Costs)
			data.PlannedCost = &planned
		}
		if err := enqueue(ctx, tx, in.OrderID, prodevents.OrderBOMEdited, p.TenantID, in.Actor, data); err != nil {
			return err
		}
		return enqueueSubcontractRequests(ctx, tx, p.TenantID, in.OrderID, in.Actor, requests)
	}()
	return out, err
}

// journaledTargets names the lines and operations any journal of the order
// references, posted, reversed or draft: they keep their item or resource.
func journaledTargets(ctx context.Context, tx pgx.Tx, tenantID, orderID string) (domain.InUse, error) {
	used := domain.InUse{Lines: map[string]bool{}, Operations: map[int]bool{}}
	rows, err := tx.Query(ctx, `select distinct l.bom_line_number, l.oper_number from production_journal_line l
		join production_journal j on j.tenant_id = l.tenant_id and j.id = l.journal_id
		where l.tenant_id = $1 and j.order_id = $2 and (l.bom_line_number is not null or l.oper_number is not null)`, tenantID, orderID)
	if err != nil {
		return used, err
	}
	defer rows.Close()
	for rows.Next() {
		var line *string
		var oper *int
		if err := rows.Scan(&line, &oper); err != nil {
			return used, err
		}
		if line != nil {
			used.Lines[*line] = true
		}
		if oper != nil {
			used.Operations[*oper] = true
		}
	}
	return used, rows.Err()
}

// writeEditedLines deletes the removed lines, updates the kept ones and appends
// the added ones after the last line.
func writeEditedLines(ctx context.Context, tx pgx.Tx, tenantID, orderID string, before, after []domain.OrderLine) error {
	kept := map[string]bool{}
	for _, l := range after {
		kept[l.LineNo] = true
	}
	existed := map[string]bool{}
	for _, l := range before {
		existed[l.LineNo] = true
		if !kept[l.LineNo] {
			if _, err := tx.Exec(ctx, `delete from production_bom_line where tenant_id = $1 and order_id = $2 and line_number = $3`,
				tenantID, orderID, l.LineNo); err != nil {
				return err
			}
		}
	}
	for _, l := range after {
		if existed[l.LineNo] {
			if _, err := tx.Exec(ctx, `update production_bom_line set item_id = $4, qty_per = $5, series_size = $6, scrap_pct = $7, round_up = $8,
					qty_required = $9, unit_cost_est = $10, flushing_principle = $11
				where tenant_id = $1 and order_id = $2 and line_number = $3`, tenantID, orderID, l.LineNo, l.ItemID, l.QtyPer.String(),
				l.SeriesSize.String(), l.ScrapPct.String(), l.RoundUp, l.QtyRequired.String(), l.UnitCost.String(), string(l.Flushing)); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(ctx, `insert into production_bom_line (tenant_id, order_id, line_number, line_seq, item_id, qty_per, series_size, scrap_pct,
				round_up, qty_required, unit_cost_est, flushing_principle)
			select $1, $2, $3, coalesce(max(line_seq), 0) + 1, $4, $5, $6, $7, $8, $9, $10, $11
			from production_bom_line where tenant_id = $1 and order_id = $2`, tenantID, orderID, l.LineNo, l.ItemID, l.QtyPer.String(),
			l.SeriesSize.String(), l.ScrapPct.String(), l.RoundUp, l.QtyRequired.String(), l.UnitCost.String(), string(l.Flushing)); err != nil {
			return err
		}
	}
	return nil
}

// writeEditedOperations deletes the removed operations, updates the kept ones
// (with their re-planned process quantity) and inserts the added ones.
func writeEditedOperations(ctx context.Context, tx pgx.Tx, tenantID, orderID string, before, after []domain.Operation) error {
	kept := map[int]bool{}
	for _, op := range after {
		kept[op.OperNumber] = true
	}
	existed := map[int]bool{}
	for _, op := range before {
		existed[op.OperNumber] = true
		if !kept[op.OperNumber] {
			if _, err := tx.Exec(ctx, `delete from production_operation where tenant_id = $1 and order_id = $2 and oper_number = $3`,
				tenantID, orderID, op.OperNumber); err != nil {
				return err
			}
		}
	}
	var added []domain.Operation
	for _, op := range after {
		if !existed[op.OperNumber] {
			added = append(added, op)
			continue
		}
		if _, err := tx.Exec(ctx, `update production_operation set resource_id = $4, setup_time_h = $5, run_time_h_per = $6, process_qty = $7,
				scrap_pct = $8, labor_rate = $9, overhead_rate = $10, subcontract = $11, subcontract_price = $12
			where tenant_id = $1 and order_id = $2 and oper_number = $3`, tenantID, orderID, op.OperNumber, op.ResourceID, op.SetupH.String(),
			op.RunHPer.String(), op.ProcessQty.String(), op.ScrapPct.String(), op.LaborRate.String(), op.OverheadRate.String(),
			op.Subcontract, op.SubcontractPrice.String()); err != nil {
			return err
		}
	}
	return insertOperations(ctx, tx, tenantID, orderID, added)
}

// auditImages renders the before and after images of an edit as JSON (nil for none).
func auditImages(a domain.EditAudit) (before, after []byte, err error) {
	var b, f any
	switch a.Target {
	case domain.TargetLine:
		if a.LineBefore != nil {
			b = a.LineBefore
		}
		if a.LineAfter != nil {
			f = a.LineAfter
		}
	default:
		if a.OperBefore != nil {
			b = a.OperBefore
		}
		if a.OperAfter != nil {
			f = a.OperAfter
		}
	}
	if b != nil {
		if before, err = json.Marshal(b); err != nil {
			return nil, nil, err
		}
	}
	if f != nil {
		if after, err = json.Marshal(f); err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

// OrderEdits returns the audit trail of an order's BOM and route edits, oldest first.
func (s *Postgres) OrderEdits(ctx context.Context, orderID string) ([]OrderEditEntry, error) {
	p, _ := tenant.FromContext(ctx)
	out := []OrderEditEntry{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from production_order where tenant_id = $1 and id = $2)`, p.TenantID, orderID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound()
		}
		rows, err := tx.Query(ctx, `select order_version, edit_seq, target, op, target_key, before_image, after_image, actor, trace_id, occurred_at
			from production_order_edit where tenant_id = $1 and order_id = $2 order by order_version, edit_seq`, p.TenantID, orderID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e OrderEditEntry
			var target, op string
			var before, after []byte
			if err := rows.Scan(&e.OrderVersion, &e.Seq, &target, &op, &e.Key, &before, &after, &e.Actor, &e.TraceID, &e.OccurredAt); err != nil {
				return err
			}
			e.Target, e.Op = domain.EditTarget(target), domain.EditOp(op)
			e.Before, e.After = jsonOrNull(before), jsonOrNull(after)
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

func jsonOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
