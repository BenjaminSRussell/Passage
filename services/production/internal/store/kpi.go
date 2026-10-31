package store

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// KPI group-by values.
const (
	KPIGroupNone    = ""
	KPIGroupSite    = "site"
	KPIGroupProduct = "product"
)

// KPIFilter narrows the order KPI inquiry (PRD 4.21.14). From and To bound the
// UTC date the order ended (empty does not bound); AsOf (default: today, UTC) is
// the date open orders are compared with their delivery date.
type KPIFilter struct {
	LegalEntityID string
	SiteID        string
	ProductID     string
	From          string
	To            string
	AsOf          string
	GroupBy       string
}

// KPIRow is one group of the report; Key is the site or product id, empty without grouping.
type KPIRow struct {
	Key string
	domain.KPIGroup
}

// KPIReport is the groups ordered by orders ended (most first, then key) and
// the totals over all of them.
type KPIReport struct {
	Groups []KPIRow
	Totals domain.KPIGroup
}

// KPIs answers the order KPI inquiry: orders ended in the period, on-time
// share against the delivery date, mean cycle days, the scrap share of the
// reported quantities, and the open and overdue orders as of a date. All dates
// come from the status log: the period is the UTC date of the Ended entry; an
// order finished at its last ReportedAsFinished entry (the Ended one when it has
// none), so a report reset and reported again counts on the later report; on
// time is finished on or before the delivery date, and cycle days run from the
// first Started entry to the finish. End is the cost close, which may come
// days after the goods were finished, so it is not the date on time is judged by.
func (s *Postgres) KPIs(ctx context.Context, f KPIFilter) (KPIReport, error) {
	p, _ := tenant.FromContext(ctx)
	if f.AsOf == "" {
		f.AsOf = time.Now().UTC().Format(time.DateOnly)
	}
	groups := map[string]*domain.KPIGroup{}
	get := func(k string) *domain.KPIGroup {
		if groups[k] == nil {
			groups[k] = &domain.KPIGroup{}
		}
		return groups[k]
	}
	const key = `case $5 when 'site' then o.site_id::text when 'product' then o.product_id::text else '' end`
	const scope = `o.tenant_id = $1
	  and ($2 = '' or o.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or o.product_id = nullif($4, '')::uuid)`
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with ended as (
			select `+key+` as k, o.delivery_date, o.qty_raf, o.qty_error,
				(select max(l.occurred_at) from production_status_log l where l.tenant_id = o.tenant_id and l.order_id = o.id and l.to_status = 'Ended') as ended_at,
				(select max(l.occurred_at) from production_status_log l where l.tenant_id = o.tenant_id and l.order_id = o.id and l.to_status = 'ReportedAsFinished') as raf_at,
				(select min(l.occurred_at) from production_status_log l where l.tenant_id = o.tenant_id and l.order_id = o.id and l.to_status = 'Started') as started_at
			from production_order o where `+scope+` and o.status = 'Ended'),
			measured as (select *, coalesce(raf_at, ended_at) as finished_at from ended)
			select k, count(*),
				count(*) filter (where delivery_date is not null),
				count(*) filter (where delivery_date is not null and (finished_at at time zone 'UTC')::date <= delivery_date),
				count(*) filter (where started_at is not null and finished_at >= started_at),
				coalesce(sum(extract(epoch from finished_at - started_at) / 86400) filter (where started_at is not null and finished_at >= started_at), 0)::text,
				coalesce(sum(qty_raf), 0)::text, coalesce(sum(qty_error), 0)::text
			from measured
			where ($6 = '' or (ended_at at time zone 'UTC')::date >= nullif($6, '')::date)
			  and ($7 = '' or (ended_at at time zone 'UTC')::date <= nullif($7, '')::date)
			group by k`, p.TenantID, f.LegalEntityID, f.SiteID, f.ProductID, f.GroupBy, f.From, f.To)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k, cycle, good, bad string
			var g domain.KPIGroup
			if err := rows.Scan(&k, &g.Ended, &g.Dated, &g.OnTime, &g.Cycled, &cycle, &good, &bad); err != nil {
				rows.Close()
				return err
			}
			g.CycleDaysSum, g.QtyFinished, g.QtyError = dec(cycle), dec(good), dec(bad)
			*get(k) = g
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		open, err := tx.Query(ctx, `select `+key+`, count(*), count(*) filter (where o.delivery_date < nullif($6, '')::date)
			from production_order o where `+scope+` and o.status not in ('Ended', 'Cancelled') group by 1`,
			p.TenantID, f.LegalEntityID, f.SiteID, f.ProductID, f.GroupBy, f.AsOf)
		if err != nil {
			return err
		}
		defer open.Close()
		for open.Next() {
			var k string
			var n, late int
			if err := open.Scan(&k, &n, &late); err != nil {
				return err
			}
			g := get(k)
			g.Open, g.Overdue = n, late
		}
		return open.Err()
	})
	if err != nil {
		return KPIReport{}, err
	}
	out := KPIReport{Groups: make([]KPIRow, 0, len(groups))}
	for k, g := range groups {
		out.Groups = append(out.Groups, KPIRow{Key: k, KPIGroup: *g})
		out.Totals.Add(*g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		a, b := out.Groups[i], out.Groups[j]
		if a.Ended != b.Ended {
			return a.Ended > b.Ended
		}
		return a.Key < b.Key
	})
	return out, nil
}
