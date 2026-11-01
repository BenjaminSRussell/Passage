package store

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// ReserveInput is a reservation request after API validation. Dims is the
// exact dimension set for MANUAL and a filter (a subset of dimensions) for
// the policies.
type ReserveInput struct {
	LegalEntityID       string
	ProductID           string
	Qty                 decimal.Decimal
	DemandType          domain.DemandType
	DemandID            string
	Policy              domain.ReservationPolicy
	Dims                domain.Dimensions
	RequiredDate        time.Time
	ShelfLifeMarginDays int
	AllowPartial        bool
	IdempotencyKey      string
	// Segment is the segment dimension value of the demand ("" when
	// unsegmented) and SegmentFallback lets a segmented demand take
	// unsegmented stock (FR-16.20, BR-16.17).
	Segment         string
	SegmentFallback bool
}

// ReserveResult lists the allocations and the quantity that could not be
// reserved (non-zero only with AllowPartial). Replayed is true when the
// request repeated a stored Idempotency-Key and nothing new was written.
type ReserveResult struct {
	Reservations []domain.Reservation
	Shortfall    decimal.Decimal
	Replayed     bool
}

// ReservationFilter selects reservations for the inquiry. Empty fields do not
// filter. Cursor is the id of the last row of the previous page (ids are
// UUIDv7, so id order is creation order); pages run newest first.
type ReservationFilter struct {
	ProductID string
	Status    domain.ReservationStatus
	DemandID  string
	Cursor    string
	Limit     int
}

// Reserve creates reservations (FR-16.7). Every on-hand row of the product in
// the legal entity is locked in ascending dimension-set order first (the
// order postings use, BR-16.4), so the availability read and the
// reserved_physical update are atomic. MANUAL reserves one exact dimension
// set; FIFO, FEFO and LIFO spread the quantity over the unrestricted rows
// matching the dimension filter in policy order. Over-reservation is
// INV-RES-001 unless AllowPartial, which returns the shortfall.
func (s *Postgres) Reserve(ctx context.Context, in ReserveInput) (ReserveResult, error) {
	filter, err := in.Dims.Normalize()
	if err != nil {
		return ReserveResult{}, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
	}
	if err := domain.ValidateReservable(filter); err != nil {
		return ReserveResult{}, domainProblem(err)
	}
	if in.Policy == domain.PolicyManual && filter[domain.DimSite] == "" {
		return ReserveResult{}, domainProblem(&domain.Error{Code: domain.CodeDimensionMandatory, Detail: "a MANUAL reservation names the dimension set, including its site"})
	}
	if in.Policy == domain.PolicyManual {
		// The named stock must be eligible for the demand's segment (BR-16.17).
		if err := domain.CheckSegment(filter[domain.DimSegment], in.Segment, in.SegmentFallback); err != nil {
			return ReserveResult{}, domainProblem(err)
		}
	} else if filter[domain.DimSegment] != "" {
		return ReserveResult{}, domainProblem(&domain.Error{Code: domain.CodeInvalid,
			Detail: "a policy reservation takes the demand's segment (segmentType, segmentRef), not a segment dimension filter"})
	}
	p, _ := tenant.FromContext(ctx)
	var out ReserveResult
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockKey(ctx, tx, "reservation", p.TenantID, in.IdempotencyKey); err != nil {
			return err
		}
		prior, err := queryReservations(ctx, tx, `r.tenant_id = $1 and r.idempotency_key = $2 order by r.allocation_no`, p.TenantID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if len(prior) > 0 {
			if !samePriorRequest(prior, in) {
				return problem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = ReserveResult{Shortfall: prior[0].requested, Replayed: true}
			for _, pr := range prior {
				out.Reservations = append(out.Reservations, pr.Reservation)
				out.Shortfall = out.Shortfall.Sub(pr.Qty)
			}
			return nil
		}
		rows, err := lockProductRows(ctx, tx, p.TenantID, in.LegalEntityID, in.ProductID)
		if err != nil {
			return err
		}
		var allocs []domain.Allocation
		shortfall := decimal.Zero
		if in.Policy == domain.PolicyManual {
			key := rowKey(in.ProductID, filter.Hash())
			avail := decimal.Zero
			if r, ok := rows[key]; ok {
				avail = r.oh.AvailPhysical()
			}
			if err := domain.CheckReservable(in.Qty, avail); err != nil {
				return domainProblem(err)
			}
			allocs = []domain.Allocation{{Key: key, Qty: in.Qty}}
		} else {
			cands, err := policyCandidates(ctx, tx, p.TenantID, in, rows, filter)
			if err != nil {
				return err
			}
			sel, err := domain.PlanDemandPolicy(in.Policy, cands, in.Qty, in.RequiredDate, in.ShelfLifeMarginDays, in.AllowPartial,
				in.Segment, in.SegmentFallback)
			if err != nil {
				return domainProblem(err)
			}
			allocs, shortfall = sel.Allocations, sel.Shortfall
		}
		for i, a := range allocs {
			r := rows[a.Key]
			r.oh.ReservedPhysical = r.oh.ReservedPhysical.Add(a.Qty)
			if err := saveRow(ctx, tx, p.TenantID, in.LegalEntityID, r, 0); err != nil {
				return err
			}
			res := domain.Reservation{ID: ids.New(), LegalEntityID: in.LegalEntityID, ProductID: in.ProductID, Dims: r.dims, Qty: a.Qty,
				DemandType: in.DemandType, DemandID: in.DemandID, DemandSegment: in.Segment, Policy: in.Policy, Status: domain.ReservationActive,
				AllocationNo: i + 1}
			if err := tx.QueryRow(ctx, `insert into inventory_reservation (tenant_id, legal_entity_id, id, idempotency_key, allocation_no,
				product_id, dimension_set_hash, qty, requested_qty, demand_type, demand_id, policy, status, created_by, demand_segment)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'ACTIVE', $13, $14) returning created_at`,
				p.TenantID, in.LegalEntityID, res.ID, in.IdempotencyKey, res.AllocationNo, in.ProductID, r.hash,
				a.Qty.String(), in.Qty.String(), string(in.DemandType), in.DemandID, string(in.Policy), optionalText(p.UserID), in.Segment).Scan(&res.CreatedAt); err != nil {
				return scopeProblem(err)
			}
			if err := enqueue(ctx, tx, "reservation", res.ID, invevents.ReservationCreated, p.TenantID, reservationData(res)); err != nil {
				return err
			}
			if err := enqueueOnHand(ctx, tx, p.TenantID, in.LegalEntityID, r, invevents.BucketReservedPhysical, a.Qty); err != nil {
				return err
			}
			out.Reservations = append(out.Reservations, res)
		}
		out.Shortfall = shortfall
		return nil
	})
	return out, err
}

// ReleaseReservation releases an ACTIVE reservation and returns its quantity
// to availability. Releasing a reservation that is no longer active returns it
// unchanged (DELETE is idempotent).
func (s *Postgres) ReleaseReservation(ctx context.Context, id string) (domain.Reservation, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Reservation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		found, err := queryReservations(ctx, tx, `r.tenant_id = $1 and r.id = $2 for update of r`, p.TenantID, id)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			return problem(http.StatusNotFound, domain.CodeReservationNotFound, "reservation not found")
		}
		res := found[0].Reservation
		if res.Status != domain.ReservationActive {
			out = res
			return nil
		}
		dims, _ := res.Dims.Normalize()
		r := &row{product: res.ProductID, dims: dims, hash: dims.Hash()}
		if err := lockRow(ctx, tx, p.TenantID, res.LegalEntityID, "", r); err != nil {
			return err
		}
		r.oh.ReservedPhysical = r.oh.ReservedPhysical.Sub(res.Qty)
		if err := saveRow(ctx, tx, p.TenantID, res.LegalEntityID, r, 0); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `update inventory_reservation set status = 'RELEASED', released_at = now()
			where tenant_id = $1 and id = $2 returning released_at`, p.TenantID, id).Scan(&res.ReleasedAt); err != nil {
			return err
		}
		res.Status = domain.ReservationReleased
		if err := enqueue(ctx, tx, "reservation", res.ID, invevents.ReservationReleased, p.TenantID, reservationData(res)); err != nil {
			return err
		}
		out = res
		return enqueueOnHand(ctx, tx, p.TenantID, res.LegalEntityID, r, invevents.BucketReservedPhysical, res.Qty.Neg())
	})
	return out, err
}

// ListReservations is the reservation inquiry (PRD 3.16.14), newest first.
func (s *Postgres) ListReservations(ctx context.Context, f ReservationFilter) ([]domain.Reservation, error) {
	limit := f.Limit
	if limit <= 0 || limit > maxListLimit {
		limit = 100
	}
	p, _ := tenant.FromContext(ctx)
	var cursor *string
	if f.Cursor != "" {
		cursor = &f.Cursor
	}
	var found []reservationRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		found, err = queryReservations(ctx, tx, `r.tenant_id = $1 and ($2 = '' or r.product_id::text = $2) and ($3 = '' or r.status = $3)
			and ($4 = '' or r.demand_id = $4) and ($5::uuid is null or r.id < $5::uuid)
			order by r.id desc limit $6`, p.TenantID, f.ProductID, string(f.Status), f.DemandID, cursor, limit)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Reservation, 0, len(found))
	for _, r := range found {
		out = append(out, r.Reservation)
	}
	return out, nil
}

// UpsertBatchExpiry sets the expiry of a batch (FEFO input). A nil expiry
// means the batch never expires.
func (s *Postgres) UpsertBatchExpiry(ctx context.Context, productID, batchID string, expiry *time.Time) error {
	p, _ := tenant.FromContext(ctx)
	var date *string
	if expiry != nil {
		v := expiry.Format(dateLayout)
		date = &v
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into batch_expiry (tenant_id, product_id, batch_id, expiry_date)
			values ($1, $2, $3, $4::date)
			on conflict (tenant_id, product_id, batch_id) do update set expiry_date = excluded.expiry_date, updated_at = now()`,
			p.TenantID, productID, batchID, date)
		return err
	})
}

// lockProductRows locks every on-hand row of a product in a legal entity, in
// ascending dimension-set order, and returns them by rowKey.
func lockProductRows(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID, productID string) (map[string]*row, error) {
	rows, err := tx.Query(ctx, `select o.dimension_set_hash, d.dims, o.posted_qty::text, o.posted_value::text, o.received::text, o.registered::text,
		o.arrived::text, o.ordered::text, o.deducted::text, o.picked::text, o.reserved_physical::text, o.held_physical::text, o.reserved_ordered::text, o.on_order::text,
		coalesce(o.currency::text, ''), o.last_txn_seq
		from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
		where o.tenant_id = $1 and o.legal_entity_id = $2 and o.product_id = $3
		order by o.dimension_set_hash for update of o`, tenantID, legalEntityID, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*row{}
	for rows.Next() {
		r := &row{product: productID}
		var raw []byte
		var b [12]string
		dest := []any{&r.hash, &raw}
		for i := range b {
			dest = append(dest, &b[i])
		}
		dest = append(dest, &r.currency, &r.seq)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r.dims); err != nil {
			return nil, err
		}
		r.before = onHandFrom(b)
		r.oh = r.before
		out[rowKey(productID, r.hash)] = r
	}
	return out, rows.Err()
}

// policyCandidates turns the locked rows that match the filter into policy
// candidates: available quantity, batch expiry (batch_expiry) and receipt date
// (earliest receipt transaction of the dimension set). Restricted rows carry
// their status so selection skips them.
func policyCandidates(ctx context.Context, tx pgx.Tx, tenantID string, in ReserveInput, rows map[string]*row, filter domain.Dimensions) ([]domain.BatchStock, error) {
	expiry := map[string]time.Time{}
	q, err := tx.Query(ctx, `select batch_id, expiry_date from batch_expiry
		where tenant_id = $1 and product_id = $2 and expiry_date is not null`, tenantID, in.ProductID)
	if err != nil {
		return nil, err
	}
	for q.Next() {
		var batch string
		var at time.Time
		if err := q.Scan(&batch, &at); err != nil {
			q.Close()
			return nil, err
		}
		expiry[batch] = at
	}
	q.Close()
	if err := q.Err(); err != nil {
		return nil, err
	}
	receipts := map[string]time.Time{}
	q, err = tx.Query(ctx, `select dimension_set_hash, min(physical_date) from inventory_transaction
		where tenant_id = $1 and legal_entity_id = $2 and product_id = $3 and receipt_status is not null
		group by dimension_set_hash`, tenantID, in.LegalEntityID, in.ProductID)
	if err != nil {
		return nil, err
	}
	for q.Next() {
		var hash []byte
		var at time.Time
		if err := q.Scan(&hash, &at); err != nil {
			q.Close()
			return nil, err
		}
		receipts[string(hash)] = at
	}
	q.Close()
	if err := q.Err(); err != nil {
		return nil, err
	}
	var out []domain.BatchStock
	for key, r := range rows {
		// Vendor-owned stock is consumed only after an ownership change (FR-16.17).
		if !matchesFilter(r.dims, filter) || domain.VendorOwned(r.dims) {
			continue
		}
		status := r.dims[domain.DimStatus]
		if status == "" {
			status = domain.StockAvailable
		}
		c := domain.BatchStock{BatchID: r.dims[domain.DimBatch], Key: key, Available: r.oh.AvailPhysical(), Status: status, ReceiptDate: in.RequiredDate,
			Segment: r.dims[domain.DimSegment]}
		if e, ok := expiry[c.BatchID]; ok && c.BatchID != "" {
			c.Expiry = &e
		}
		if at, ok := receipts[string(r.hash)]; ok {
			c.ReceiptDate = at
		}
		out = append(out, c)
	}
	return out, nil
}

func matchesFilter(dims, filter domain.Dimensions) bool {
	for name, v := range filter {
		if dims[name] != v {
			return false
		}
	}
	return true
}

// reservationRow is a stored allocation plus the quantity its request asked for.
type reservationRow struct {
	domain.Reservation
	requested decimal.Decimal
}

// queryReservations reads reservations (alias r) joined to their dimension
// sets; where is the rest of the statement after "where".
func queryReservations(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]reservationRow, error) {
	rows, err := tx.Query(ctx, `select r.id::text, r.legal_entity_id::text, r.product_id::text, d.dims, r.qty::text, r.requested_qty::text,
		r.demand_type, r.demand_id, r.demand_segment, r.policy, r.status, r.allocation_no, r.created_at, r.released_at
		from inventory_reservation r join dimension_set d on d.tenant_id = r.tenant_id and d.hash = r.dimension_set_hash
		where `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []reservationRow
	for rows.Next() {
		var rr reservationRow
		var raw []byte
		var qty, requested, demand, policy, status string
		if err := rows.Scan(&rr.ID, &rr.LegalEntityID, &rr.ProductID, &raw, &qty, &requested, &demand, &rr.DemandID, &rr.DemandSegment, &policy, &status,
			&rr.AllocationNo, &rr.CreatedAt, &rr.ReleasedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &rr.Dims); err != nil {
			return nil, err
		}
		rr.Qty, rr.requested = decimal.RequireFromString(qty), decimal.RequireFromString(requested)
		rr.DemandType, rr.Policy, rr.Status = domain.DemandType(demand), domain.ReservationPolicy(policy), domain.ReservationStatus(status)
		out = append(out, rr)
	}
	return out, rows.Err()
}

// samePriorRequest reports whether a replayed request matches what was stored.
func samePriorRequest(prior []reservationRow, in ReserveInput) bool {
	first := prior[0]
	return first.LegalEntityID == in.LegalEntityID && first.ProductID == in.ProductID && first.DemandType == in.DemandType &&
		first.DemandID == in.DemandID && first.DemandSegment == in.Segment && first.Policy == in.Policy && first.requested.Equal(in.Qty)
}

func reservationData(r domain.Reservation) invevents.ReservationData {
	return invevents.ReservationData{ReservationID: r.ID, LegalEntityID: r.LegalEntityID, ProductID: r.ProductID, DimensionSet: map[string]string(r.Dims),
		Qty: r.Qty.String(), DemandType: string(r.DemandType), DemandID: r.DemandID, DemandSegment: r.DemandSegment, Policy: string(r.Policy),
		Status: string(r.Status)}
}
