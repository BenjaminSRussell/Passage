package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// HousekeepingOrder is an order one of the housekeeping jobs selects (PRD 4.21.16).
type HousekeepingOrder struct {
	OrderID     string        `json:"orderId"`
	OrderNumber string        `json:"orderNumber"`
	SiteID      string        `json:"siteId"`
	ProductID   string        `json:"productId"`
	Status      domain.Status `json:"status"`
	Quantity    string        `json:"quantity"`
	QtyRAF      string        `json:"qtyReportedFinished"`
	Version     int           `json:"version"`
	CreatedAt   time.Time     `json:"createdAt"`
	// AgeDays is the whole days since the order was created, at the time of the scan.
	AgeDays int `json:"ageDays"`
}

// effectiveColumn picks one parameter column of the order's site row, else its All row,
// else the service default ($3).
func effectiveColumn(col string) string {
	return `coalesce((select pp.` + col + ` from production_parameters pp
		where pp.tenant_id = o.tenant_id and pp.legal_entity_id = o.legal_entity_id and pp.site_key in (o.site_id::text, 'All')
		order by (pp.site_key = 'All') limit 1), $3)`
}

const housekeepingColumns = `o.id::text, o.order_number, o.site_id::text, o.product_id::text, o.status, o.quantity::text, o.qty_raf::text,
	o.version, o.created_at, floor(extract(epoch from (now() - o.created_at)) / 86400)::int`

// HousekeepingScan narrows a housekeeping scan to a legal entity and, when OrderIDs is set, to
// the orders a caller reviewed in a dry run, so a run acts on no order the caller did not see.
type HousekeepingScan struct {
	LegalEntityID string
	OrderIDs      []string
	Limit         int
}

// StaleCreatedOrders lists the orders cancel-stale-created-orders would cancel (PRD 4.21.16):
// orders still Created that were never estimated (no status-log row ever moved them to
// Estimated, so an order reset back to Created stays out), older than their site's
// staleOrderDays (the site's row, else the All row, else defaultDays; 0 never cancels), whose
// origin is not a planned order (MRP owns those). Oldest first, at most Limit; total counts them all.
func (s *Postgres) StaleCreatedOrders(ctx context.Context, in HousekeepingScan, defaultDays int) ([]HousekeepingOrder, int, error) {
	days := effectiveColumn("stale_order_days")
	return s.housekeepingScan(ctx, in, defaultDays,
		`o.status = 'Created' and o.origin_type <> 'PlannedOrder' and `+days+` > 0
		 and o.created_at < now() - (`+days+` * interval '1 day')
		 and not exists (select 1 from production_status_log l where l.tenant_id = o.tenant_id and l.order_id = o.id and l.to_status = 'Estimated')`,
		`o.created_at, o.id`)
}

// FullyReportedOrders lists the orders auto-end-fully-reported would end (PRD 4.21.16): orders
// ReportedAsFinished whose reported quantity covers the order quantity, with no draft journal,
// at sites that opted in (autoEndEnabled; the default is off).
func (s *Postgres) FullyReportedOrders(ctx context.Context, in HousekeepingScan) ([]HousekeepingOrder, int, error) {
	return s.housekeepingScan(ctx, in, false,
		`o.status = 'ReportedAsFinished' and o.qty_raf >= o.quantity and `+effectiveColumn("auto_end_enabled")+`
		 and not exists (select 1 from production_journal j where j.tenant_id = o.tenant_id and j.order_id = o.id and not j.posted)`,
		`o.updated_at, o.id`)
}

// housekeepingScan runs a scan; param is $3, the service default of the parameter the filter
// reads (days for the stale scan, off for auto-end).
func (s *Postgres) housekeepingScan(ctx context.Context, in HousekeepingScan, param any, where, order string) ([]HousekeepingOrder, int, error) {
	p, _ := tenant.FromContext(ctx)
	var only []string
	if len(in.OrderIDs) > 0 {
		only = in.OrderIDs
	}
	out := []HousekeepingOrder{}
	total := 0
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		base := ` from production_order o where o.tenant_id = $1 and o.legal_entity_id = $2::uuid
			and ($4::uuid[] is null or o.id = any($4::uuid[])) and ` + where
		if err := tx.QueryRow(ctx, `select count(*)`+base, p.TenantID, in.LegalEntityID, param, only).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+housekeepingColumns+base+` order by `+order+` limit $5`, p.TenantID, in.LegalEntityID, param, only, in.Limit)
		if err != nil {
			return err
		}
		out, err = scanHousekeeping(rows)
		return err
	})
	return out, total, err
}

// HousekeepingReplay returns the orders an earlier run under idempotency key moved: the orders
// whose status log holds `<key>:<orderId>`. A key first used for another job, command or legal
// entity is refused (422 idempotency_key_reuse); none found means the key is new.
func (s *Postgres) HousekeepingReplay(ctx context.Context, legalEntityID, key, requestHash string) ([]HousekeepingOrder, error) {
	p, _ := tenant.FromContext(ctx)
	var out []HousekeepingOrder
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The range on the text_pattern_ops index of 0016 is every key that starts with "<key>:".
		match := `from production_status_log l where l.tenant_id = $1 and l.idempotency_key ~>=~ ($2 || ':') and l.idempotency_key ~<~ ($2 || ';')
			and l.idempotency_key = $2 || ':' || l.order_id::text`
		var foreign bool
		if err := tx.QueryRow(ctx, `select exists (select 1 `+match+` and (l.request_hash <> $3 or l.legal_entity_id <> $4::uuid))`,
			p.TenantID, key, requestHash, legalEntityID).Scan(&foreign); err != nil {
			return err
		}
		if foreign {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used for another job, command or legal entity")
		}
		rows, err := tx.Query(ctx, `select `+housekeepingColumns+` from production_order o
			where o.tenant_id = $1 and o.id in (select l.order_id `+match+`) order by o.created_at, o.id`, p.TenantID, key)
		if err != nil {
			return err
		}
		out, err = scanHousekeeping(rows)
		return err
	})
	return out, err
}

func scanHousekeeping(rows pgx.Rows) ([]HousekeepingOrder, error) {
	defer rows.Close()
	out := []HousekeepingOrder{}
	for rows.Next() {
		var o HousekeepingOrder
		var status string
		if err := rows.Scan(&o.OrderID, &o.OrderNumber, &o.SiteID, &o.ProductID, &status, &o.Quantity, &o.QtyRAF, &o.Version, &o.CreatedAt, &o.AgeDays); err != nil {
			return nil, err
		}
		o.Status, o.Quantity, o.QtyRAF = domain.Status(status), dec(o.Quantity).String(), dec(o.QtyRAF).String()
		out = append(out, o)
	}
	return out, rows.Err()
}
