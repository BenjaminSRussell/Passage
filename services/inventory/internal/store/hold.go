package store

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

type HoldInput struct {
	IdempotencyKey string
	LegalEntityID  string
	ProductID      string
	Dims           domain.Dimensions
	Qty            decimal.Decimal
	WholeLot       bool
	AllowPartial   bool
	Reason         string
	SourceType     string
	SourceRef      string
	Note           string
}

type HoldResult struct {
	Holds     []domain.Hold
	Shortfall decimal.Decimal
	Replayed  bool
}

type HoldFilter struct {
	ProductID  string
	SourceType string
	SourceRef  string
	State      string
	Limit      int
}

type HoldSource struct {
	Type string
	Ref  string
}

type SourceRelease struct {
	Released []domain.Hold
	Known    bool
}

type holdRow struct {
	domain.Hold
	requested decimal.Decimal
}

type holdAllocation struct {
	row *row
	qty decimal.Decimal
}

// PlaceHold takes quantity out of availability without moving it: the held
// quantity stays in physical stock and is excluded from avail_physical and
// avail_ordered until it is released. A quantity hold names one exact dimension
// set; a whole-lot hold takes everything available in every unrestricted row of
// the product whose dimensions match the filter (the batch is mandatory), one
// hold per row. Rows are locked in ascending order and availability is read
// under the lock, so a hold never exceeds what can be issued (INV-HLD-001)
// unless AllowPartial, which holds what is available and returns the shortfall.
// A repeated key returns the stored holds with Replayed, or idempotency_key_reuse
// when the body differs.
func (s *Postgres) PlaceHold(ctx context.Context, in HoldInput) (HoldResult, error) {
	req := domain.HoldRequest{ProductID: in.ProductID, Dims: in.Dims, Qty: in.Qty, WholeLot: in.WholeLot, Reason: in.Reason,
		SourceType: in.SourceType, SourceRef: in.SourceRef}
	if err := domain.ValidateHoldRequest(req, s.rules.MaxScale); err != nil {
		return HoldResult{}, domainProblem(err)
	}
	filter, _ := in.Dims.Normalize()
	requested := in.Qty
	if in.WholeLot {
		requested = decimal.Zero
	}
	tp, _ := tenant.FromContext(ctx)
	var out HoldResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "hold", tp.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		prior, err := queryHolds(ctx, tx, `h.tenant_id = $1 and h.idempotency_key = $2 order by h.allocation_no`, tp.TenantID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if len(prior) > 0 {
			if !samePriorHold(prior[0], in, filter, requested) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = HoldResult{Replayed: true}
			for _, p := range prior {
				out.Holds = append(out.Holds, p.Hold)
			}
			return nil
		}
		allocs, shortfall, err := planHold(ctx, tx, tp.TenantID, in, filter)
		if err != nil {
			return err
		}
		out.Shortfall = shortfall
		for i, a := range allocs {
			a.row.oh.HeldPhysical = a.row.oh.HeldPhysical.Add(a.qty)
			if err := saveRow(ctx, tx, tp.TenantID, in.LegalEntityID, a.row, 0); err != nil {
				return err
			}
			h := domain.Hold{ID: ids.New(), LegalEntityID: in.LegalEntityID, ProductID: in.ProductID, Dims: a.row.dims, Qty: a.qty,
				ReleasedQty: decimal.Zero, WholeLot: in.WholeLot, Reason: in.Reason, SourceType: in.SourceType, SourceRef: in.SourceRef,
				Note: in.Note, State: domain.HoldActive, AllocationNo: i + 1, Version: 1}
			if err := tx.QueryRow(ctx, `insert into inventory_hold (tenant_id, legal_entity_id, id, idempotency_key, allocation_no, product_id,
				dimension_set_hash, qty, requested_qty, whole_lot, reason, source_type, source_ref, note, state, created_by)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 'ACTIVE', $15) returning created_at`,
				tp.TenantID, in.LegalEntityID, h.ID, in.IdempotencyKey, h.AllocationNo, in.ProductID, a.row.hash, a.qty.String(), requested.String(),
				in.WholeLot, in.Reason, in.SourceType, in.SourceRef, optionalText(in.Note), optionalText(tp.UserID)).Scan(&h.CreatedAt); err != nil {
				return scopeProblem(err)
			}
			if err := enqueue(ctx, tx, "hold", h.ID, invevents.HoldPlaced, tp.TenantID, holdData(h)); err != nil {
				return err
			}
			if err := enqueueOnHand(ctx, tx, tp.TenantID, in.LegalEntityID, a.row, invevents.BucketHeldPhysical, a.qty); err != nil {
				return err
			}
			out.Holds = append(out.Holds, h)
		}
		return nil
	})
	return out, err
}

func planHold(ctx context.Context, tx pgx.Tx, tenantID string, in HoldInput, filter domain.Dimensions) ([]holdAllocation, decimal.Decimal, error) {
	if !in.WholeLot {
		r := &row{product: in.ProductID, dims: filter, hash: filter.Hash()}
		if err := lockRow(ctx, tx, tenantID, in.LegalEntityID, "", r); err != nil {
			return nil, decimal.Zero, err
		}
		held, shortfall, err := domain.PlanHold(in.Qty, r.oh.AvailPhysical(), in.AllowPartial)
		if err != nil {
			return nil, decimal.Zero, domainProblem(err)
		}
		return []holdAllocation{{row: r, qty: held}}, shortfall, nil
	}
	rows, err := lockProductRows(ctx, tx, tenantID, in.LegalEntityID, in.ProductID)
	if err != nil {
		return nil, decimal.Zero, err
	}
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var allocs []holdAllocation
	for _, k := range keys {
		r := rows[k]
		if avail := r.oh.AvailPhysical(); avail.Sign() > 0 && matchesFilter(r.dims, filter) && domain.ValidateReservable(r.dims) == nil {
			allocs = append(allocs, holdAllocation{row: r, qty: avail})
		}
	}
	if len(allocs) == 0 {
		return nil, decimal.Zero, problem(http.StatusUnprocessableEntity, domain.CodeHoldExceeds, "no available stock of the lot to hold")
	}
	return allocs, decimal.Zero, nil
}

func samePriorHold(first holdRow, in HoldInput, filter domain.Dimensions, requested decimal.Decimal) bool {
	if first.LegalEntityID != in.LegalEntityID || first.ProductID != in.ProductID || first.Reason != in.Reason || first.SourceType != in.SourceType ||
		first.SourceRef != in.SourceRef || first.WholeLot != in.WholeLot || !first.requested.Equal(requested) {
		return false
	}
	return in.WholeLot || first.Dims.Canonical() == filter.Canonical()
}

// ReleaseHold returns qty (all of the remaining hold when nil) to availability.
// The version must match (If-Match). A released hold is returned unchanged, so
// a retry is harmless.
func (s *Postgres) ReleaseHold(ctx context.Context, id string, version int, qty *decimal.Decimal) (domain.Hold, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Hold
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := queryHolds(ctx, tx, `h.tenant_id = $1 and h.id = $2 for update of h`, tp.TenantID, id)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return problem(http.StatusNotFound, domain.CodeHoldNotFound, "hold not found")
		}
		h := found[0].Hold
		if h.State == domain.HoldReleased {
			out = h
			return nil
		}
		if h.Version != version {
			return problem(http.StatusPreconditionFailed, CodeConflictVersion, "hold changed since it was read; reread and retry")
		}
		release := h.Held()
		if qty != nil {
			release = *qty
		}
		if err := domain.CheckHoldRelease(h, release); err != nil {
			return domainProblem(err)
		}
		out, err = applyHoldRelease(ctx, tx, tp, h, release)
		return err
	})
	return out, err
}

// ReleaseHoldsBySource releases active holds of one source until at most keep
// remains held for it, oldest first. The call states the end level rather than a
// delta, so repeating it releases nothing more.
func (s *Postgres) ReleaseHoldsBySource(ctx context.Context, sourceType, sourceRef string, keep decimal.Decimal, moveTo *HoldSource) (SourceRelease, error) {
	tp, _ := tenant.FromContext(ctx)
	out := SourceRelease{Released: []domain.Hold{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := queryHolds(ctx, tx, `h.tenant_id = $1 and h.source_type = $2 and h.source_ref = $3 and h.state = 'ACTIVE'
			order by h.id for update of h`, tp.TenantID, sourceType, sourceRef)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select exists (select 1 from inventory_hold where tenant_id = $1 and source_type = $2 and source_ref = $3)`,
			tp.TenantID, sourceType, sourceRef).Scan(&out.Known); err != nil {
			return err
		}
		excess := decimal.Zero
		for _, f := range found {
			excess = excess.Add(f.Held())
		}
		excess = excess.Sub(keep)
		type planned struct {
			hold domain.Hold
			take decimal.Decimal
		}
		var plan []planned
		for _, f := range found {
			if excess.Sign() <= 0 {
				break
			}
			take := decimal.Min(excess, f.Held())
			plan = append(plan, planned{f.Hold, take})
			excess = excess.Sub(take)
		}
		slices.SortFunc(plan, func(a, b planned) int {
			return strings.Compare(rowKey(a.hold.ProductID, a.hold.Dims.Hash()), rowKey(b.hold.ProductID, b.hold.Dims.Hash()))
		})
		for _, pl := range plan {
			h, err := applyHoldRelease(ctx, tx, tp, pl.hold, pl.take)
			if err != nil {
				return err
			}
			out.Released = append(out.Released, h)
		}
		if moveTo != nil {
			_, err := tx.Exec(ctx, `update inventory_hold set source_type = $4, source_ref = $5, version = version + 1
				where tenant_id = $1 and source_type = $2 and source_ref = $3 and state = 'ACTIVE'`, tp.TenantID, sourceType, sourceRef, moveTo.Type, moveTo.Ref)
			return err
		}
		return nil
	})
	return out, err
}

func applyHoldRelease(ctx context.Context, tx pgx.Tx, tp tenant.Principal, h domain.Hold, qty decimal.Decimal) (domain.Hold, error) {
	r := &row{product: h.ProductID, dims: h.Dims, hash: h.Dims.Hash()}
	if err := lockRow(ctx, tx, tp.TenantID, h.LegalEntityID, "", r); err != nil {
		return h, err
	}
	r.oh.HeldPhysical = r.oh.HeldPhysical.Sub(qty)
	if err := saveRow(ctx, tx, tp.TenantID, h.LegalEntityID, r, 0); err != nil {
		return h, err
	}
	h.ReleasedQty = h.ReleasedQty.Add(qty)
	h.State = domain.HoldActive
	if h.ReleasedQty.Equal(h.Qty) {
		h.State = domain.HoldReleased
	}
	if err := tx.QueryRow(ctx, `update inventory_hold set released_qty = $3, state = $4, version = version + 1,
		released_at = case when $4 = 'RELEASED' then now() else null end,
		released_by = case when $4 = 'RELEASED' then $5 else null end
		where tenant_id = $1 and id = $2 returning version, released_at`,
		tp.TenantID, h.ID, h.ReleasedQty.String(), h.State, optionalText(tp.UserID)).Scan(&h.Version, &h.ReleasedAt); err != nil {
		return h, err
	}
	if err := enqueue(ctx, tx, "hold", h.ID, invevents.HoldReleased, tp.TenantID, holdData(h)); err != nil {
		return h, err
	}
	return h, enqueueOnHand(ctx, tx, tp.TenantID, h.LegalEntityID, r, invevents.BucketHeldPhysical, qty.Neg())
}

// ListHolds is the hold inquiry, newest first.
func (s *Postgres) ListHolds(ctx context.Context, f HoldFilter) ([]domain.Hold, error) {
	limit := f.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = 100
	}
	tp, _ := tenant.FromContext(ctx)
	var found []holdRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		found, err = queryHolds(ctx, tx, `h.tenant_id = $1 and ($2 = '' or h.product_id::text = $2) and ($3 = '' or h.source_type = $3)
			and ($4 = '' or h.source_ref = $4) and ($5 = '' or h.state = $5) order by h.id desc limit $6`,
			tp.TenantID, f.ProductID, f.SourceType, f.SourceRef, f.State, limit)
		return err
	})
	out := make([]domain.Hold, 0, len(found))
	for _, h := range found {
		out = append(out, h.Hold)
	}
	return out, err
}

func queryHolds(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]holdRow, error) {
	rows, err := tx.Query(ctx, `select h.id::text, h.legal_entity_id::text, h.product_id::text, d.dims, h.qty::text, h.released_qty::text,
		h.requested_qty::text, h.whole_lot, h.reason, h.source_type, h.source_ref, coalesce(h.note, ''), h.state, h.allocation_no, h.version,
		h.created_at, h.released_at
		from inventory_hold h join dimension_set d on d.tenant_id = h.tenant_id and d.hash = h.dimension_set_hash
		where `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []holdRow
	for rows.Next() {
		var hr holdRow
		var raw []byte
		var qty, released, requested string
		if err := rows.Scan(&hr.ID, &hr.LegalEntityID, &hr.ProductID, &raw, &qty, &released, &requested, &hr.WholeLot, &hr.Reason, &hr.SourceType,
			&hr.SourceRef, &hr.Note, &hr.State, &hr.AllocationNo, &hr.Version, &hr.CreatedAt, &hr.ReleasedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &hr.Dims); err != nil {
			return nil, err
		}
		hr.Qty, hr.ReleasedQty, hr.requested = decimal.RequireFromString(qty), decimal.RequireFromString(released), decimal.RequireFromString(requested)
		out = append(out, hr)
	}
	return out, rows.Err()
}

func holdData(h domain.Hold) invevents.HoldData {
	return invevents.HoldData{HoldID: h.ID, LegalEntityID: h.LegalEntityID, ProductID: h.ProductID, DimensionSet: map[string]string(h.Dims),
		Qty: h.Qty.String(), ReleasedQty: h.ReleasedQty.String(), Reason: h.Reason, SourceType: h.SourceType, SourceRef: h.SourceRef, State: h.State}
}
