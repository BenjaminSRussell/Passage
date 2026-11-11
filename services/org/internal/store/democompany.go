package store

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

// CodeDemoCompanyInUse refuses a demo company reset because records that the demo did not create hang off it.
const CodeDemoCompanyInUse = "ORG-DEMO-IN-USE"

// DemoRemoval counts what RemoveDemoCompany deleted and names the organization records that went with it.
type DemoRemoval struct {
	LegalEntityIDs   []string `json:"legalEntityIds"`
	OperatingUnitIDs []string `json:"operatingUnitIds"`
	SiteIDs          []string `json:"siteIds"`
	WarehouseIDs     []string `json:"warehouseIds"`
	LocationIDs      []string `json:"locationIds"`
	HierarchyTypes   int      `json:"hierarchyTypes"`
	TeamTypes        int      `json:"teamTypes"`
	Teams            int      `json:"teams"`
	Addresses        int      `json:"addresses"`
}

// Removed is the number of records deleted, counted the way the demo company load counts what it creates.
func (d DemoRemoval) Removed() int {
	return len(d.LegalEntityIDs) + len(d.OperatingUnitIDs) + len(d.SiteIDs) + len(d.WarehouseIDs) + len(d.LocationIDs) +
		d.HierarchyTypes + d.TeamTypes + d.Teams + d.Addresses
}

// demoSet is the ids the demo company created, found by the idempotency key namespace its loader writes.
type demoSet struct {
	legalEntities, operatingUnits, sites, warehouses, locations, hierarchyTypes, teamTypes, teams []string
}

func (d demoSet) owners() []string {
	out := make([]string, 0, len(d.legalEntities)+len(d.operatingUnits)+len(d.sites)+len(d.warehouses)+len(d.locations))
	for _, ids := range [][]string{d.legalEntities, d.operatingUnits, d.sites, d.warehouses, d.locations} {
		out = append(out, ids...)
	}
	return out
}

// RemoveDemoCompany deletes, in one transaction, the organization records whose idempotency key starts with
// keyPrefix (the namespace only the demo company loader writes), plus each site's default warehouse, which the
// site creates in the same transaction without a key of its own. Details that belong to those records alone go
// with them: name history, translations, addresses, rounding rules and team memberships. Anything else that
// points at a demo record (a site, warehouse, location, operating unit or team someone added under it, a
// hierarchy version or node, a legal entity copy) stops the reset with 409 ORG-DEMO-IN-USE before anything is
// deleted, so records the demo did not create are never touched. One outbox event carries the actor and the
// removed ids, and is what the audit log records.
func (s *Postgres) RemoveDemoCompany(ctx context.Context, keyPrefix, actor string) (DemoRemoval, error) {
	if keyPrefix == "" || !strings.HasPrefix(keyPrefix, "seed:") {
		return DemoRemoval{}, fmt.Errorf("demo key prefix %q is not a seed namespace", keyPrefix)
	}
	p, _ := tenant.FromContext(ctx)
	pattern := likePrefix(keyPrefix)
	var out DemoRemoval
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('org-demo-company:' || $1::text, 0))`, p.TenantID); err != nil {
			return err
		}
		set, err := findDemoSet(ctx, tx, p.TenantID, pattern)
		if err != nil {
			return err
		}
		if err := refuseIfInUse(ctx, tx, p.TenantID, set); err != nil {
			return err
		}
		if out, err = deleteDemoSet(ctx, tx, p.TenantID, set); err != nil {
			return err
		}
		if out.Removed() == 0 {
			return nil
		}
		return enqueue(ctx, tx, "democompany", p.TenantID, orgevents.DemoCompanyRemoved, p.TenantID, orgevents.DemoCompanyRemovedData{
			Actor: actor, LegalEntityIDs: out.LegalEntityIDs, OperatingUnitIDs: out.OperatingUnitIDs, SiteIDs: out.SiteIDs,
			WarehouseIDs: out.WarehouseIDs, LocationIDs: out.LocationIDs, Removed: out.Removed(),
		})
	})
	if err != nil {
		return DemoRemoval{}, err
	}
	return out, nil
}

// likePrefix escapes LIKE wildcards so the prefix matches literally.
func likePrefix(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix) + "%"
}

func collectIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

func findDemoSet(ctx context.Context, tx pgx.Tx, tenantID, pattern string) (demoSet, error) {
	var d demoSet
	keyed := func(table string) ([]string, error) {
		return collectIDs(ctx, tx, `select id::text from `+table+` where tenant_id = $1 and idempotency_key like $2 order by id`, tenantID, pattern)
	}
	var err error
	if d.legalEntities, err = keyed("legal_entity"); err != nil {
		return d, err
	}
	if d.operatingUnits, err = keyed("operating_unit"); err != nil {
		return d, err
	}
	if d.sites, err = keyed("site"); err != nil {
		return d, err
	}
	// A site's default warehouse is inserted by the site's own transaction with no key, so it shares the site's
	// created_at (now() is the transaction start); nothing else can.
	if d.warehouses, err = collectIDs(ctx, tx, `select w.id::text from warehouse w
		where w.tenant_id = $1 and (w.idempotency_key like $2 or (w.idempotency_key is null and exists (
			select 1 from site s where s.tenant_id = w.tenant_id and s.id = w.site_id and s.idempotency_key like $2 and s.created_at = w.created_at)))
		order by w.id`, tenantID, pattern); err != nil {
		return d, err
	}
	if d.locations, err = keyed("location"); err != nil {
		return d, err
	}
	if d.hierarchyTypes, err = keyed("hierarchy_type"); err != nil {
		return d, err
	}
	if d.teamTypes, err = keyed("team_type"); err != nil {
		return d, err
	}
	d.teams, err = keyed("team")
	return d, err
}

type blocker struct {
	label string
	sql   string
	args  func(d demoSet) []any
}

var demoBlockers = []blocker{
	{"site", `select count(*) from site where tenant_id = $1 and legal_entity_id::text = any($2) and not id::text = any($3)`,
		func(d demoSet) []any { return []any{d.legalEntities, d.sites} }},
	{"warehouse", `select count(*) from warehouse where tenant_id = $1 and site_id::text = any($2) and not id::text = any($3)`,
		func(d demoSet) []any { return []any{d.sites, d.warehouses} }},
	{"location", `select count(*) from location where tenant_id = $1 and warehouse_id::text = any($2) and not id::text = any($3)`,
		func(d demoSet) []any { return []any{d.warehouses, d.locations} }},
	{"operating unit", `select count(*) from operating_unit where tenant_id = $1 and default_legal_entity_id::text = any($2) and not id::text = any($3)`,
		func(d demoSet) []any { return []any{d.legalEntities, d.operatingUnits} }},
	{"team", `select count(*) from team where tenant_id = $1 and (team_type_id::text = any($2) or operating_unit_id::text = any($3)) and not id::text = any($4)`,
		func(d demoSet) []any { return []any{d.teamTypes, d.operatingUnits, d.teams} }},
	{"hierarchy version", `select count(*) from hierarchy_version where tenant_id = $1 and hierarchy_type_id::text = any($2)`,
		func(d demoSet) []any { return []any{d.hierarchyTypes} }},
	{"hierarchy node", `select count(*) from hierarchy_node n join hierarchy_version v on v.tenant_id = n.tenant_id and v.id = n.version_id
		where n.tenant_id = $1 and n.org_unit_id::text = any($2) and not v.hierarchy_type_id::text = any($3)`,
		func(d demoSet) []any {
			return []any{append(append([]string{}, d.legalEntities...), d.operatingUnits...), d.hierarchyTypes}
		}},
	{"legal entity copy", `select count(*) from legal_entity_copy where tenant_id = $1 and (source_id::text = any($2) or target_id::text = any($2))`,
		func(d demoSet) []any { return []any{d.legalEntities} }},
}

func refuseIfInUse(ctx context.Context, tx pgx.Tx, tenantID string, d demoSet) error {
	var found []string
	for _, b := range demoBlockers {
		var n int
		if err := tx.QueryRow(ctx, b.sql, append([]any{tenantID}, b.args(d)...)...).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			label := b.label
			if n > 1 {
				label += "s"
			}
			found = append(found, fmt.Sprintf("%d %s", n, label))
		}
	}
	if len(found) == 0 {
		return nil
	}
	return httpx.NewProblem(http.StatusConflict, CodeDemoCompanyInUse,
		"records that the demo company did not create are attached to it ("+strings.Join(found, ", ")+"); remove them, or keep the demo company as it is")
}

func deleteDemoSet(ctx context.Context, tx pgx.Tx, tenantID string, d demoSet) (DemoRemoval, error) {
	out := DemoRemoval{LegalEntityIDs: d.legalEntities, OperatingUnitIDs: d.operatingUnits, SiteIDs: d.sites, WarehouseIDs: d.warehouses, LocationIDs: d.locations}
	exec := func(sql string, args ...any) (int, error) {
		tag, err := tx.Exec(ctx, sql, append([]any{tenantID}, args...)...)
		return int(tag.RowsAffected()), err
	}
	owners := d.owners()
	steps := []struct {
		sql   string
		args  []any
		count *int
	}{
		{`delete from team_member where tenant_id = $1 and team_id::text = any($2)`, []any{d.teams}, nil},
		{`delete from team where tenant_id = $1 and id::text = any($2)`, []any{d.teams}, &out.Teams},
		{`delete from team_type where tenant_id = $1 and id::text = any($2)`, []any{d.teamTypes}, &out.TeamTypes},
		{`delete from hierarchy_type where tenant_id = $1 and id::text = any($2)`, []any{d.hierarchyTypes}, &out.HierarchyTypes},
		{`delete from address_purpose where tenant_id = $1 and owner_id::text = any($2)`, []any{owners}, nil},
		{`delete from postal_address where tenant_id = $1 and owner_id::text = any($2) and owner_type <> 'party'`, []any{owners}, &out.Addresses},
		{`delete from org_unit_translation where tenant_id = $1 and owner_id::text = any($2)`, []any{owners}, nil},
		{`delete from location where tenant_id = $1 and id::text = any($2)`, []any{d.locations}, nil},
		{`update site set default_warehouse_id = null where tenant_id = $1 and id::text = any($2)`, []any{d.sites}, nil},
		{`delete from warehouse where tenant_id = $1 and id::text = any($2)`, []any{d.warehouses}, nil},
		{`delete from site where tenant_id = $1 and id::text = any($2)`, []any{d.sites}, nil},
		{`delete from currency_rounding where tenant_id = $1 and legal_entity_id::text = any($2)`, []any{d.legalEntities}, nil},
		{`delete from operating_unit where tenant_id = $1 and id::text = any($2)`, []any{d.operatingUnits}, nil},
		{`delete from legal_entity_name where tenant_id = $1 and legal_entity_id::text = any($2)`, []any{d.legalEntities}, nil},
		{`delete from legal_entity where tenant_id = $1 and id::text = any($2)`, []any{d.legalEntities}, nil},
	}
	for _, step := range steps {
		n, err := exec(step.sql, step.args...)
		if err != nil {
			return DemoRemoval{}, err
		}
		if step.count != nil {
			*step.count = n
		}
	}
	return out, nil
}
