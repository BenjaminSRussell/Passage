package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// RunQuotas returns the quota arrangements of the given items at siteID whose
// validity overlaps from..to, with their sources and the quantity already
// allocated to each, for a plan run to allocate its planned orders (FR-22.17).
// Each arrangement also carries the firmed and approved planned orders of
// operational versions allocated to its sources inside its validity, by
// delivery date, so the run counts them against a per-period maximum. With
// planEntity set, only arrangements of that legal entity (or without one) are
// read. itemIDs must be UUIDs.
func (s *Postgres) RunQuotas(ctx context.Context, siteID, planEntity string, itemIDs []string, from, to time.Time) ([]domain.RunQuota, error) {
	if len(itemIDs) == 0 {
		return nil, nil
	}
	p, _ := tenant.FromContext(ctx)
	var out []domain.RunQuota
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, quotaSelect+` where tenant_id = $1 and site_id = $2::uuid and item_id = any($3::uuid[])
			and valid_from <= $5::date and valid_to >= $4::date
			and ($6::text = '' or legal_entity_id is null or legal_entity_id::text = $6::text)
			order by item_id, valid_from, id`, p.TenantID, siteID, itemIDs, from, to, planEntity)
		if err != nil {
			return err
		}
		var list []QuotaArrangement
		for rows.Next() {
			a, err := scanQuota(rows)
			if err != nil {
				rows.Close()
				return err
			}
			list = append(list, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := loadQuotaSources(ctx, tx, p.TenantID, list); err != nil {
			return err
		}
		booked, err := quotaBookings(ctx, tx, p.TenantID, list)
		if err != nil {
			return err
		}
		for _, a := range list {
			out = append(out, domain.RunQuota{ArrangementID: a.ID, Spec: a.Spec, Booked: booked[a.ID]})
		}
		return nil
	})
	return out, err
}

// quotaBookings reads, per arrangement, the firmed and approved planned orders
// allocated to its capped sources and delivered inside its validity. Sources
// without a per-period maximum need none.
func quotaBookings(ctx context.Context, tx pgx.Tx, tenantID string, list []QuotaArrangement) (map[string][]domain.QuotaBooking, error) {
	out := map[string][]domain.QuotaBooking{}
	var sourceIDs []string
	for _, a := range list {
		for _, s := range a.Spec.Sources {
			if s.MaxQtyPerPeriod != nil {
				sourceIDs = append(sourceIDs, s.ID)
			}
		}
	}
	if len(sourceIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `select a.id::text, o.quota_source_id::text, o.delivery_date, o.qty::text
		from planned_order o
		join plan_version v on v.tenant_id = o.tenant_id and v.id = o.plan_version_id
		join quota_arrangement a on a.tenant_id = o.tenant_id and a.id = o.quota_arrangement_id
		where o.tenant_id = $1 and o.quota_source_id = any($2::uuid[]) and o.status in ('firmed', 'approved')
			and v.kind <> 'simulation' and o.delivery_date between a.valid_from and a.valid_to
		order by a.id, o.delivery_date, o.id`, tenantID, sourceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var arrangementID, qty string
		var b domain.QuotaBooking
		if err := rows.Scan(&arrangementID, &b.SourceID, &b.Day, &qty); err != nil {
			return nil, err
		}
		if b.Qty, err = decimal.NewFromString(qty); err != nil {
			return nil, err
		}
		out[arrangementID] = append(out[arrangementID], b)
	}
	return out, rows.Err()
}

// quotaRef is the arrangement and source a planned order was allocated to.
type quotaRef struct{ arrangementID, sourceID string }

// bookQuota books the quantity of each of the given planned orders that
// carries a quota source against that source's allocated quantity, as the
// orders become firmed (FR-22.17, BR-22.12), and returns the reference of each
// such order. It runs in the firming transaction, once per order: an order is
// firmed once, so a source is never booked twice for it. The arrangement's
// version is bumped with the booking, so an update sent with the ETag read
// before it (whose sources carry the old allocated quantities) is a 412 and
// cannot write the booking back out.
func bookQuota(ctx context.Context, tx pgx.Tx, tenantID string, orderIDs []string) (map[string]quotaRef, error) {
	refs := map[string]quotaRef{}
	if len(orderIDs) == 0 {
		return refs, nil
	}
	rows, err := tx.Query(ctx, `select id::text, quota_arrangement_id::text, quota_source_id::text from planned_order
		where tenant_id = $1 and id = any($2::uuid[]) and quota_source_id is not null`, tenantID, orderIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var r quotaRef
		if err := rows.Scan(&id, &r.arrangementID, &r.sourceID); err != nil {
			rows.Close()
			return nil, err
		}
		refs[id] = r
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return refs, nil
	}
	if _, err := tx.Exec(ctx, `update quota_source s set allocated_qty = s.allocated_qty + t.qty
		from (select quota_source_id, sum(qty) as qty from planned_order
			where tenant_id = $1 and id = any($2::uuid[]) and quota_source_id is not null group by quota_source_id) t
		where s.tenant_id = $1 and s.id = t.quota_source_id`, tenantID, orderIDs); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `update quota_arrangement a set version = a.version + 1, updated_at = now()
		where a.tenant_id = $1 and a.id in (select s.arrangement_id from quota_source s join planned_order o
			on o.tenant_id = s.tenant_id and o.quota_source_id = s.id where o.tenant_id = $1 and o.id = any($2::uuid[]))`,
		tenantID, orderIDs)
	return refs, err
}

func (r quotaRef) arrangement() *string { return optional(r.arrangementID) }
func (r quotaRef) source() *string      { return optional(r.sourceID) }
