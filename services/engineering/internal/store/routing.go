package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const (
	routeVersionColumns   = `id::text, product_id, site_id, route_code, version_no, effective_from, effective_to, status`
	routeOperationColumns = `operation_no, operation_id, next_no, link_type, priority, setup_h::text, run_h::text, process_qty::text,
		queue_before_h::text, queue_after_h::text, transit_h::text, efficiency_pct::text, overlap_qty::text, scrap_percent::text`
	insertRouteOperation = `insert into route_operation (tenant_id, route_version_id, operation_no, operation_id, next_no, link_type, priority, setup_h, run_h, process_qty,
		queue_before_h, queue_after_h, transit_h, efficiency_pct, overlap_qty, scrap_percent)
		values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13::numeric, $14::numeric, $15::numeric, $16::numeric)`
)

type RouteVersionFilter struct {
	ProductID, SiteID, Status string
	AsOf                      time.Time
}

type RouteVersionHeader struct {
	Version    domain.RouteVersion
	Operations int
}

func scanRouteVersion(row pgx.Row, extra ...any) (v domain.RouteVersion, err error) {
	var to *time.Time
	if err = row.Scan(append([]any{&v.ID, &v.ProductID, &v.SiteID, &v.RouteCode, &v.VersionNo, &v.EffectiveFrom, &to, &v.Status}, extra...)...); err == nil {
		v.EffectiveTo = valueOrZero(to)
	}
	return v, err
}

func scanRouteOperation(row pgx.CollectableRow) (domain.VersionOperation, error) {
	var o domain.VersionOperation
	var amounts [9]string
	if err := row.Scan(&o.No, &o.OperationID, &o.NextNo, &o.LinkType, &o.Priority, &amounts[0], &amounts[1], &amounts[2], &amounts[3], &amounts[4], &amounts[5], &amounts[6], &amounts[7], &amounts[8]); err != nil {
		return o, err
	}
	var failure error
	for i, target := range []*decimal.Decimal{&o.SetupH, &o.RunH, &o.ProcessQty, &o.QueueBeforeH, &o.QueueAfterH, &o.TransitH, &o.EfficiencyPct, &o.OverlapQty, &o.ScrapPercent} {
		*target = parseDecimal(amounts[i], &failure)
	}
	return o, failure
}

func getRouteVersion(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.RouteVersion, error) {
	v, err := scanRouteVersion(tx.QueryRow(ctx, `select `+routeVersionColumns+` from route_version where tenant_id = $1 and id = $2::uuid`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return v, notFound(CodeRouteNotFound, "route version not found")
	}
	if err != nil {
		return v, err
	}
	rows, err := tx.Query(ctx, `select `+routeOperationColumns+` from route_operation where tenant_id = $1 and route_version_id = $2::uuid order by operation_no`, tenantID, id)
	if err != nil {
		return v, err
	}
	if v.Operations, err = pgx.CollectRows(rows, scanRouteOperation); err != nil {
		return v, err
	}
	rows, err = tx.Query(ctx, `select operation_no, kind, ref_id, level, quantity from operation_requirement where tenant_id = $1 and route_version_id = $2::uuid order by operation_no, req_index`, tenantID, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	at := make(map[int]int, len(v.Operations))
	for i, o := range v.Operations {
		at[o.No] = i
	}
	for rows.Next() {
		var no int
		var r domain.OperationRequirement
		if err := rows.Scan(&no, &r.Kind, &r.RefID, &r.Level, &r.Quantity); err != nil {
			return v, err
		}
		v.Operations[at[no]].Requirements = append(v.Operations[at[no]].Requirements, r)
	}
	return v, rows.Err()
}

func insertRouteVersion(ctx context.Context, tx pgx.Tx, tenantID string, v domain.RouteVersion) error {
	if v.VersionNo == 0 && v.ProductID != "" && v.SiteID != "" && v.RouteCode != "" {
		// The product lock serialises concurrent creates so two drafts never read the same max(version_no).
		if err := lockProducts(ctx, tx, tenantID, v.ProductID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select coalesce(max(version_no), 0) + 1 from route_version where tenant_id = $1 and product_id = $2 and site_id = $3 and route_code = $4`,
			tenantID, v.ProductID, v.SiteID, v.RouteCode).Scan(&v.VersionNo); err != nil {
			return err
		}
	}
	if err := domain.ValidateRouteVersion(v); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into route_version (tenant_id, id, product_id, site_id, route_code, version_no, effective_from, effective_to, status)
		values ($1, $2, $3, $4, $5, $6, $7, $8, 'draft')`,
		tenantID, v.ID, v.ProductID, v.SiteID, v.RouteCode, v.VersionNo, v.EffectiveFrom, zeroToNil(v.EffectiveTo)); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, o := range v.Operations {
		linkType := o.LinkType
		if linkType == "" {
			linkType = domain.LinkHard
		}
		priority := o.Priority
		if priority == "" {
			priority = domain.PriorityPrimary
		}
		batch.Queue(insertRouteOperation, tenantID, v.ID, o.No, o.OperationID, o.NextNo, linkType, priority, o.SetupH.String(), o.RunH.String(), o.ProcessQty.String(),
			o.QueueBeforeH.String(), o.QueueAfterH.String(), o.TransitH.String(), o.EfficiencyPct.String(), o.OverlapQty.String(), o.ScrapPercent.String())
	}
	for _, o := range v.Operations {
		for i, r := range o.Requirements {
			batch.Queue(`insert into operation_requirement (tenant_id, route_version_id, operation_no, req_index, kind, ref_id, level, quantity) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
				tenantID, v.ID, o.No, i, r.Kind, r.RefID, r.Level, r.Quantity)
		}
	}
	return execBatch(ctx, tx, batch)
}

func (s *Postgres) CreateRouteVersion(ctx context.Context, v domain.RouteVersion, idem Idem) (created domain.RouteVersion, err error) {
	err = s.createOnce(ctx, idem, v.ID, func(tx pgx.Tx, p tenant.Principal) error {
		return insertRouteVersion(ctx, tx, p.TenantID, v)
	}, func(tx pgx.Tx, tenantID, id string) (err error) {
		created, err = getRouteVersion(ctx, tx, tenantID, id)
		return err
	})
	return created, err
}

func (s *Postgres) GetRouteVersion(ctx context.Context, id string) (v domain.RouteVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		v, err = getRouteVersion(ctx, tx, p.TenantID, id)
		return err
	})
	return v, err
}

func (s *Postgres) ListRouteVersions(ctx context.Context, f RouteVersionFilter, afterID string, limit int) (Page[RouteVersionHeader], error) {
	p, _ := tenant.FromContext(ctx)
	if afterID == "" {
		afterID = ids.Nil
	}
	query := `select ` + routeVersionColumns + `, (select count(*) from route_operation o where o.tenant_id = v.tenant_id and o.route_version_id = v.id)
		from route_version v where v.tenant_id = $1 and v.id > $2::uuid`
	args := []any{p.TenantID, afterID}
	for _, c := range []struct {
		value  any
		clause string
		set    bool
	}{
		{f.ProductID, `v.product_id = $%d`, f.ProductID != ""},
		{f.SiteID, `v.site_id = $%d`, f.SiteID != ""},
		{f.Status, `v.status = $%d`, f.Status != ""},
		{f.AsOf, `v.effective_from <= $%[1]d and (v.effective_to is null or v.effective_to >= $%[1]d)`, !f.AsOf.IsZero()},
	} {
		if c.set {
			args = append(args, c.value)
			query += ` and ` + fmt.Sprintf(c.clause, len(args))
		}
	}
	query += fmt.Sprintf(` order by v.id limit $%d`, len(args)+1)
	var items []RouteVersionHeader
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, append(args, limit+1)...)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (h RouteVersionHeader, err error) {
			h.Version, err = scanRouteVersion(row, &h.Operations)
			return h, err
		})
		return err
	})
	return trimPage(items, limit, func(h RouteVersionHeader) string { return h.Version.ID }), err
}

func (s *Postgres) ActiveRouteVersion(ctx context.Context, productID, siteID string, asOf time.Time) (v domain.RouteVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `select id::text from route_version where tenant_id = $1 and product_id = $2 and site_id = $3 and status = 'active'
			and effective_from <= $4 and (effective_to is null or effective_to >= $4) order by effective_from desc, id limit 1`, p.TenantID, productID, siteID, asOf).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeRouteNotFound, "no active route version covers the date")
		}
		if err != nil {
			return err
		}
		v, err = getRouteVersion(ctx, tx, p.TenantID, id)
		return err
	})
	return v, err
}

func activeRouteVersions(ctx context.Context, tx pgx.Tx, tenantID string, v domain.RouteVersion) ([]domain.RouteVersion, error) {
	rows, err := tx.Query(ctx, `select `+routeVersionColumns+` from route_version where tenant_id = $1 and product_id = $2 and site_id = $3 and status = 'active'`, tenantID, v.ProductID, v.SiteID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.RouteVersion, error) { return scanRouteVersion(row) })
}

func loadSiteResources(ctx context.Context, tx pgx.Tx, tenantID, siteID string) ([]domain.Resource, error) {
	rows, err := tx.Query(ctx, `select id::text, type, coalesce(group_id::text, '') from resource where tenant_id = $1 and site_id = $2`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	resources, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (r domain.Resource, err error) {
		r.SiteID = siteID
		err = row.Scan(&r.ID, &r.Type, &r.GroupID)
		return r, err
	})
	if err != nil {
		return nil, err
	}
	at := make(map[string]int, len(resources))
	for i, r := range resources {
		at[r.ID] = i
	}
	rows, err = tx.Query(ctx, `select c.resource_id::text, c.capability_id::text, c.level, c.effective_from, c.effective_to from resource_capability c
		join resource r on r.tenant_id = c.tenant_id and r.id = c.resource_id where c.tenant_id = $1 and r.site_id = $2`, tenantID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var resourceID string
		var c domain.ResourceCapability
		var to *time.Time
		if err := rows.Scan(&resourceID, &c.CapabilityID, &c.Level, &c.EffectiveFrom, &to); err != nil {
			return nil, err
		}
		c.EffectiveTo = valueOrZero(to)
		resources[at[resourceID]].Capabilities = append(resources[at[resourceID]].Capabilities, c)
	}
	return resources, rows.Err()
}

func (s *Postgres) ActivateRouteVersion(ctx context.Context, id string) (v domain.RouteVersion, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if v, err = getRouteVersion(ctx, tx, p.TenantID, id); err != nil || v.Status == domain.StatusActive {
			return err
		}
		if err = lockProducts(ctx, tx, p.TenantID, v.ProductID); err != nil {
			return err
		}
		if v, err = getRouteVersion(ctx, tx, p.TenantID, id); err != nil || v.Status == domain.StatusActive {
			return err
		}
		if err = v.EnsureDraft(); err != nil {
			return err
		}
		active, err := activeRouteVersions(ctx, tx, p.TenantID, v)
		if err != nil {
			return err
		}
		if err = domain.CheckRouteNoOverlap(active, v); err != nil {
			return err
		}
		resources, err := loadSiteResources(ctx, tx, p.TenantID, v.SiteID)
		if err != nil {
			return err
		}
		if err = domain.CheckRequirements(v, resources); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update route_version set status = 'active' where tenant_id = $1 and id = $2::uuid`, p.TenantID, id); err != nil {
			return err
		}
		v.Status = domain.StatusActive
		return emit(ctx, tx, p, id, engevents.RouteVersionActivated, aggregateRouteVersion, id, v.ActivatedEvent())
	})
	return v, err
}
