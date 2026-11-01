package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	engevents "github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

const (
	dateLayout      = "2006-01-02"
	calendarColumns = `id::text, code, name, template, closed_dates, generated_from, generated_to, version`
	resourceColumns = `id::text, code, name, type, site_id, coalesce(group_id::text, ''), coalesce(calendar_id::text, ''), efficiency_pct::text, capacity_per_day_h::text, exclusive, coalesce(vendor_party_id, ''), version`
	upsertDay       = `insert into calendar_day (tenant_id, calendar_id, date, open, shifts, total_h) values ($1, $2::uuid, $3, $4, $5::jsonb, $6::numeric)
		on conflict (tenant_id, calendar_id, date) do update set open = excluded.open, shifts = excluded.shifts, total_h = excluded.total_h`
)

type Calendar struct {
	ID string
	domain.WorkCalendar
	Version       int
	GeneratedFrom time.Time
	GeneratedTo   time.Time
}

type CalendarGeneration struct {
	OpenDays   int
	TotalHours decimal.Decimal
}

type ResourceRecord struct {
	Resource            domain.Resource
	EffectiveCalendarID string
}

func jsonOf(v any) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}

func emptyToNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func execBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch) error {
	results := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	return results.Close()
}

func notFound(code, detail string) error {
	return httpx.NewProblem(http.StatusNotFound, code, detail)
}

func scanCalendar(row pgx.Row) (c Calendar, err error) {
	var from, to *time.Time
	if err = row.Scan(&c.ID, &c.Code, &c.Name, &c.Template, &c.ClosedDates, &from, &to, &c.Version); err == nil {
		c.GeneratedFrom, c.GeneratedTo = valueOrZero(from), valueOrZero(to)
	}
	return c, err
}

func getCalendar(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Calendar, error) {
	query := `select ` + calendarColumns + ` from work_calendar where tenant_id = $1 and id = $2::uuid`
	if lock {
		query += ` for update`
	}
	c, err := scanCalendar(tx.QueryRow(ctx, query, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, notFound(CodeCalNotFound, "calendar not found")
	}
	return c, err
}

func requireCalendar(ctx context.Context, tx pgx.Tx, tenantID, id string) (err error) {
	if id == "" {
		return nil
	}
	_, err = getCalendar(ctx, tx, tenantID, id, false)
	return err
}

func (s *Postgres) createOnce(ctx context.Context, idem Idem, id string, insert func(tx pgx.Tx, p tenant.Principal) error, load func(tx pgx.Tx, tenantID, id string) error) error {
	p, _ := tenant.FromContext(ctx)
	return conflictOnDuplicate(dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		stored, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if !found {
			if err := insert(tx, p); err != nil {
				return err
			}
			if err := remember(ctx, tx, p.TenantID, idem, id); err != nil {
				return err
			}
			stored = id
		}
		return load(tx, p.TenantID, stored)
	}))
}

func (s *Postgres) CreateCalendar(ctx context.Context, c Calendar, idem Idem) (created Calendar, warnings []string, err error) {
	err = s.createOnce(ctx, idem, c.ID, func(tx pgx.Tx, p tenant.Principal) error {
		if _, err := domain.ValidateCalendar(c.WorkCalendar); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into work_calendar (tenant_id, id, code, name, template, closed_dates) values ($1, $2, $3, $4, $5::jsonb, coalesce($6::date[], '{}'))`,
			p.TenantID, c.ID, c.Code, c.Name, jsonOf(c.Template), c.ClosedDates)
		return err
	}, func(tx pgx.Tx, tenantID, id string) (err error) {
		if created, err = getCalendar(ctx, tx, tenantID, id, false); err == nil {
			warnings, err = domain.ValidateCalendar(created.WorkCalendar)
		}
		return err
	})
	return created, warnings, err
}

func (s *Postgres) GenerateCalendar(ctx context.Context, id string, from, to time.Time) (CalendarGeneration, error) {
	p, _ := tenant.FromContext(ctx)
	var out CalendarGeneration
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = CalendarGeneration{}
		cal, err := getCalendar(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		days, err := domain.Generate(cal.WorkCalendar, from, to)
		if err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, d := range days {
			out.TotalHours = out.TotalHours.Add(d.TotalH)
			if d.Open {
				out.OpenDays++
			}
			batch.Queue(upsertDay, p.TenantID, id, d.Date, d.Open, jsonOf(d.Shifts), d.TotalH.String())
		}
		if err := execBatch(ctx, tx, batch); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update work_calendar set generated_from = least(coalesce(generated_from, $3::date), $3::date), generated_to = greatest(coalesce(generated_to, $4::date), $4::date)
			where tenant_id = $1 and id = $2::uuid`, p.TenantID, id, from, to); err != nil {
			return err
		}
		return emit(ctx, tx, p, id, engevents.CalendarGenerated, aggregateCalendar, id,
			engevents.CalendarGeneratedData{CalendarID: id, From: from.Format(dateLayout), To: to.Format(dateLayout), OpenDays: out.OpenDays})
	})
	return out, err
}

func (s *Postgres) ListCalendarDays(ctx context.Context, id string, from, to time.Time) ([]domain.CalendarDay, error) {
	p, _ := tenant.FromContext(ctx)
	if _, err := domain.Generate(domain.WorkCalendar{}, from, to); err != nil {
		return nil, err
	}
	var days []domain.CalendarDay
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getCalendar(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select date, open, shifts, total_h::text from calendar_day where tenant_id = $1 and calendar_id = $2::uuid and date between $3 and $4 order by date`, p.TenantID, id, from, to)
		if err != nil {
			return err
		}
		days, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (d domain.CalendarDay, err error) {
			var total string
			if err = row.Scan(&d.Date, &d.Open, &d.Shifts, &total); err == nil {
				var failure error
				d.TotalH, err = parseDecimal(total, &failure), failure
			}
			return d, err
		})
		return err
	})
	return days, err
}

func getGroup(ctx context.Context, tx pgx.Tx, tenantID, id string) (domain.ResourceGroup, error) {
	var g domain.ResourceGroup
	err := tx.QueryRow(ctx, `select id::text, code, name, site_id, coalesce(calendar_id::text, '') from resource_group where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&g.ID, &g.Code, &g.Name, &g.SiteID, &g.CalendarID)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, notFound(CodeResNotFound, "resource group not found")
	}
	return g, err
}

func (s *Postgres) CreateResourceGroup(ctx context.Context, g domain.ResourceGroup, idem Idem) (created domain.ResourceGroup, err error) {
	err = s.createOnce(ctx, idem, g.ID, func(tx pgx.Tx, p tenant.Principal) error {
		if err := requireCalendar(ctx, tx, p.TenantID, g.CalendarID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into resource_group (tenant_id, id, code, name, site_id, calendar_id) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, g.ID, g.Code, g.Name, g.SiteID, emptyToNil(g.CalendarID))
		return err
	}, func(tx pgx.Tx, tenantID, id string) (err error) {
		created, err = getGroup(ctx, tx, tenantID, id)
		return err
	})
	return created, err
}

func (s *Postgres) CreateCapability(ctx context.Context, c domain.Capability, idem Idem) (created domain.Capability, err error) {
	err = s.createOnce(ctx, idem, c.ID, func(tx pgx.Tx, p tenant.Principal) error {
		_, err := tx.Exec(ctx, `insert into capability (tenant_id, id, code, name) values ($1, $2, $3, $4)`, p.TenantID, c.ID, c.Code, c.Name)
		return err
	}, func(tx pgx.Tx, tenantID, id string) error {
		return tx.QueryRow(ctx, `select id::text, code, name from capability where tenant_id = $1 and id = $2::uuid`, tenantID, id).Scan(&created.ID, &created.Code, &created.Name)
	})
	return created, err
}

func scanResource(row pgx.Row, extra ...any) (r domain.Resource, err error) {
	var efficiency, capacity string
	if err = row.Scan(append([]any{&r.ID, &r.Code, &r.Name, &r.Type, &r.SiteID, &r.GroupID, &r.CalendarID, &efficiency, &capacity, &r.Exclusive, &r.VendorPartyID, &r.Version}, extra...)...); err != nil {
		return r, err
	}
	var failure error
	r.EfficiencyPct, r.CapacityPerDayH = parseDecimal(efficiency, &failure), parseDecimal(capacity, &failure)
	return r, failure
}

func scanResourceCapability(row pgx.Row, extra ...any) (c domain.ResourceCapability, err error) {
	var to *time.Time
	if err = row.Scan(append(append([]any{}, extra...), &c.CapabilityID, &c.Level, &c.EffectiveFrom, &to)...); err == nil {
		c.EffectiveTo = valueOrZero(to)
	}
	return c, err
}

func getResource(ctx context.Context, tx pgx.Tx, tenantID, id string) (ResourceRecord, error) {
	r, err := scanResource(tx.QueryRow(ctx, `select `+resourceColumns+` from resource where tenant_id = $1 and id = $2::uuid`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ResourceRecord{}, notFound(CodeResNotFound, "resource not found")
	}
	if err != nil {
		return ResourceRecord{}, err
	}
	rows, err := tx.Query(ctx, `select capability_id::text, level, effective_from, effective_to from resource_capability where tenant_id = $1 and resource_id = $2::uuid order by capability_id, effective_from`, tenantID, id)
	if err != nil {
		return ResourceRecord{}, err
	}
	r.Capabilities, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.ResourceCapability, error) { return scanResourceCapability(row) })
	if err != nil {
		return ResourceRecord{}, err
	}
	var g domain.ResourceGroup
	if r.GroupID != "" {
		if g, err = getGroup(ctx, tx, tenantID, r.GroupID); err != nil {
			return ResourceRecord{}, err
		}
	}
	return ResourceRecord{Resource: r, EffectiveCalendarID: domain.EffectiveCalendar(r, g)}, nil
}

func changedFields(r domain.Resource) []string {
	fields := []string{"code", "name", "type", "siteId", "efficiencyPct", "capacityPerDayH", "exclusive"}
	for _, f := range []struct {
		name string
		set  bool
	}{{"groupId", r.GroupID != ""}, {"calendarId", r.CalendarID != ""}, {"vendorPartyId", r.VendorPartyID != ""}, {"capabilities", len(r.Capabilities) > 0}} {
		if f.set {
			fields = append(fields, f.name)
		}
	}
	return fields
}

// checkResourceRefs validates a resource and the rows it points at: its group (same site), calendar
// and capabilities.
func checkResourceRefs(ctx context.Context, tx pgx.Tx, p tenant.Principal, r domain.Resource) error {
	if err := domain.ValidateResource(r); err != nil {
		return err
	}
	if r.GroupID != "" {
		g, err := getGroup(ctx, tx, p.TenantID, r.GroupID)
		if err != nil {
			return err
		}
		if g.SiteID != r.SiteID {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeResSite, "resource site must equal the site of its resource group")
		}
	}
	if err := requireCalendar(ctx, tx, p.TenantID, r.CalendarID); err != nil {
		return err
	}
	capabilityIDs := make([]string, len(r.Capabilities))
	for i, c := range r.Capabilities {
		capabilityIDs[i] = c.CapabilityID
	}
	var missing int
	if err := tx.QueryRow(ctx, `select count(*) from unnest($2::uuid[]) as wanted(id) where not exists (select 1 from capability where tenant_id = $1 and id = wanted.id)`, p.TenantID, capabilityIDs).Scan(&missing); err != nil {
		return err
	}
	if missing > 0 {
		return notFound(CodeResNotFound, "a referenced capability does not exist")
	}
	return nil
}

func insertResource(ctx context.Context, tx pgx.Tx, p tenant.Principal, r domain.Resource) error {
	if err := checkResourceRefs(ctx, tx, p, r); err != nil {
		return err
	}
	efficiency := r.EfficiencyPct
	if efficiency.IsZero() {
		efficiency = decimal.NewFromInt(100)
	}
	if _, err := tx.Exec(ctx, `insert into resource (tenant_id, id, code, name, type, site_id, group_id, calendar_id, efficiency_pct, capacity_per_day_h, exclusive, vendor_party_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11, $12)`,
		p.TenantID, r.ID, r.Code, r.Name, r.Type, r.SiteID, emptyToNil(r.GroupID), emptyToNil(r.CalendarID), efficiency.String(), r.CapacityPerDayH.String(), r.Exclusive, emptyToNil(r.VendorPartyID)); err != nil {
		return err
	}
	if err := insertResourceCapabilities(ctx, tx, p.TenantID, r); err != nil {
		return err
	}
	data := engevents.ResourceChangedData{ResourceID: r.ID, SiteID: r.SiteID, CalendarID: r.CalendarID, EfficiencyPct: efficiency.String(), ChangedFields: changedFields(r)}
	return emit(ctx, tx, p, r.ID, engevents.ResourceChanged, aggregateResource, r.ID, data)
}

func (s *Postgres) CreateResource(ctx context.Context, r domain.Resource, idem Idem) (rec ResourceRecord, err error) {
	err = s.createOnce(ctx, idem, r.ID, func(tx pgx.Tx, p tenant.Principal) error {
		return insertResource(ctx, tx, p, r)
	}, func(tx pgx.Tx, tenantID, id string) (err error) {
		rec, err = getResource(ctx, tx, tenantID, id)
		return err
	})
	return rec, err
}

func (s *Postgres) GetResource(ctx context.Context, id string) (ResourceRecord, error) {
	p, _ := tenant.FromContext(ctx)
	var rec ResourceRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		rec, err = getResource(ctx, tx, p.TenantID, id)
		return err
	})
	return rec, err
}

type ResourceFilter struct{ SiteID, Type, GroupID, CapabilityID string }

func keysetAfter(table, afterID string, args []any) (string, []any) {
	if afterID == "" {
		return "", args
	}
	args = append(args, afterID)
	return fmt.Sprintf(` and (code, id) > (select code, id from %s where tenant_id = $1 and id = $%d::uuid)`, table, len(args)), args
}

func (s *Postgres) ListResources(ctx context.Context, f ResourceFilter, afterID string, limit int) (Page[ResourceRecord], error) {
	p, _ := tenant.FromContext(ctx)
	args := []any{p.TenantID}
	query := `select ` + resourceColumns + `, coalesce((select g.calendar_id::text from resource_group g where g.tenant_id = resource.tenant_id and g.id = resource.group_id), '')
		from resource where tenant_id = $1`
	for _, c := range []struct{ value, clause string }{
		{f.SiteID, `site_id = $%d`},
		{f.Type, `type = $%d`},
		{f.GroupID, `group_id = $%d::uuid`},
		{f.CapabilityID, `exists (select 1 from resource_capability rc where rc.tenant_id = resource.tenant_id and rc.resource_id = resource.id and rc.capability_id = $%d::uuid)`},
	} {
		if c.value != "" {
			args = append(args, c.value)
			query += ` and ` + fmt.Sprintf(c.clause, len(args))
		}
	}
	keyset, args := keysetAfter("resource", afterID, args)
	query += keyset + fmt.Sprintf(` order by code, id limit $%d`, len(args)+1)
	var items []ResourceRecord
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, append(args, limit+1)...)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (rec ResourceRecord, err error) {
			var groupCalendar string
			if rec.Resource, err = scanResource(row, &groupCalendar); err == nil {
				rec.EffectiveCalendarID = domain.EffectiveCalendar(rec.Resource, domain.ResourceGroup{CalendarID: groupCalendar})
			}
			return rec, err
		})
		if err != nil || len(items) == 0 {
			return err
		}
		return attachCapabilities(ctx, tx, p.TenantID, items[:min(len(items), limit)])
	})
	return trimPage(items, limit, func(r ResourceRecord) string { return r.Resource.ID }), err
}

func attachCapabilities(ctx context.Context, tx pgx.Tx, tenantID string, items []ResourceRecord) error {
	at := make(map[string]int, len(items))
	resourceIDs := make([]string, len(items))
	for i, rec := range items {
		at[rec.Resource.ID], resourceIDs[i] = i, rec.Resource.ID
	}
	rows, err := tx.Query(ctx, `select resource_id::text, capability_id::text, level, effective_from, effective_to from resource_capability
		where tenant_id = $1 and resource_id = any($2::uuid[]) order by resource_id, capability_id, effective_from`, tenantID, resourceIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var resourceID string
		c, err := scanResourceCapability(rows, &resourceID)
		if err != nil {
			return err
		}
		r := &items[at[resourceID]].Resource
		r.Capabilities = append(r.Capabilities, c)
	}
	return rows.Err()
}

func listByCode[T any](ctx context.Context, s *Postgres, table, columns, siteID, afterID string, limit int, scan func(pgx.Row) (T, error), id func(T) string) (Page[T], error) {
	p, _ := tenant.FromContext(ctx)
	args := []any{p.TenantID}
	query := `select ` + columns + ` from ` + table + ` where tenant_id = $1`
	if siteID != "" {
		args = append(args, siteID)
		query += fmt.Sprintf(` and site_id = $%d`, len(args))
	}
	keyset, args := keysetAfter(table, afterID, args)
	query += keyset + fmt.Sprintf(` order by code, id limit $%d`, len(args)+1)
	var items []T
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, append(args, limit+1)...)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (T, error) { return scan(row) })
		return err
	})
	return trimPage(items, limit, id), err
}

func (s *Postgres) ListResourceGroups(ctx context.Context, siteID, afterID string, limit int) (Page[domain.ResourceGroup], error) {
	return listByCode(ctx, s, "resource_group", `id::text, code, name, site_id, coalesce(calendar_id::text, '')`, siteID, afterID, limit,
		func(row pgx.Row) (g domain.ResourceGroup, err error) {
			err = row.Scan(&g.ID, &g.Code, &g.Name, &g.SiteID, &g.CalendarID)
			return g, err
		}, func(g domain.ResourceGroup) string { return g.ID })
}

func (s *Postgres) ListCapabilities(ctx context.Context, afterID string, limit int) (Page[domain.Capability], error) {
	return listByCode(ctx, s, "capability", `id::text, code, name`, "", afterID, limit,
		func(row pgx.Row) (c domain.Capability, err error) {
			err = row.Scan(&c.ID, &c.Code, &c.Name)
			return c, err
		}, func(c domain.Capability) string { return c.ID })
}

func (s *Postgres) GetCalendar(ctx context.Context, id string) (c Calendar, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = getCalendar(ctx, tx, p.TenantID, id, false)
		return err
	})
	return c, err
}

func (s *Postgres) ListCalendars(ctx context.Context, afterID string, limit int) (Page[Calendar], error) {
	return listByCode(ctx, s, "work_calendar", calendarColumns, "", afterID, limit, scanCalendar, func(c Calendar) string { return c.ID })
}

func (s *Postgres) GetResourceGroup(ctx context.Context, id string) (g domain.ResourceGroup, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		g, err = getGroup(ctx, tx, p.TenantID, id)
		return err
	})
	return g, err
}

func (s *Postgres) GetCapability(ctx context.Context, id string) (c domain.Capability, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id::text, code, name from capability where tenant_id = $1 and id = $2::uuid`, p.TenantID, id).Scan(&c.ID, &c.Code, &c.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(CodeResNotFound, "capability not found")
		}
		return err
	})
	return c, err
}

func insertResourceCapabilities(ctx context.Context, tx pgx.Tx, tenantID string, r domain.Resource) error {
	batch := &pgx.Batch{}
	for _, c := range r.Capabilities {
		batch.Queue(`insert into resource_capability (tenant_id, resource_id, capability_id, effective_from, level, effective_to) values ($1, $2, $3, $4, $5, $6)`,
			tenantID, r.ID, c.CapabilityID, c.EffectiveFrom, c.Level, zeroToNil(c.EffectiveTo))
	}
	return execBatch(ctx, tx, batch)
}

// lockResource takes the resource row FOR UPDATE, so an edit, a delete and a route activation that
// reads it queue behind one another, and returns the stored record.
func lockResource(ctx context.Context, tx pgx.Tx, tenantID, id string) (ResourceRecord, error) {
	var locked string
	if err := tx.QueryRow(ctx, `select id::text from resource where tenant_id = $1 and id = $2::uuid for update`, tenantID, id).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		return ResourceRecord{}, notFound(CodeResNotFound, "resource not found")
	} else if err != nil {
		return ResourceRecord{}, err
	}
	return getResource(ctx, tx, tenantID, id)
}

func sameCapabilities(a, b []domain.ResourceCapability) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(c domain.ResourceCapability) string {
		return fmt.Sprintf("%s|%d|%s|%s", c.CapabilityID, c.Level, c.EffectiveFrom.Format(time.DateOnly), c.EffectiveTo.Format(time.DateOnly))
	}
	seen := map[string]int{}
	for _, c := range a {
		seen[key(c)]++
	}
	for _, c := range b {
		seen[key(c)]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func differingFields(old, next domain.Resource) []string {
	var out []string
	for _, f := range []struct {
		name string
		same bool
	}{
		{"name", old.Name == next.Name}, {"type", old.Type == next.Type}, {"groupId", old.GroupID == next.GroupID}, {"calendarId", old.CalendarID == next.CalendarID},
		{"efficiencyPct", old.EfficiencyPct.Equal(next.EfficiencyPct)}, {"capacityPerDayH", old.CapacityPerDayH.Equal(next.CapacityPerDayH)},
		{"exclusive", old.Exclusive == next.Exclusive}, {"vendorPartyId", old.VendorPartyID == next.VendorPartyID}, {"capabilities", sameCapabilities(old.Capabilities, next.Capabilities)},
	} {
		if !f.same {
			out = append(out, f.name)
		}
	}
	return out
}

// UpdateResource replaces the name, type, group, calendar, efficiency, capacity, exclusivity, vendor
// and capabilities of a resource. The code and site are its identity and stay (the caller refuses a
// change). expectedVersion is the counter the caller edited: another save in between answers
// ENG-RES-VERSION-CONFLICT and nothing is written. A save that changes nothing writes nothing and
// emits nothing; any other adds 1 to version and emits resource.changed naming the changed fields.
// A replayed Idempotency-Key returns the resource as it is now without writing again.
func (s *Postgres) UpdateResource(ctx context.Context, r domain.Resource, expectedVersion int, idem Idem) (rec ResourceRecord, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replay(ctx, tx, p.TenantID, idem)
		if err != nil {
			return err
		}
		if found {
			rec, err = getResource(ctx, tx, p.TenantID, id)
			return err
		}
		stored, err := lockResource(ctx, tx, p.TenantID, r.ID)
		if err != nil {
			return err
		}
		if stored.Resource.Version != expectedVersion {
			return &domain.Error{Code: domain.CodeResConflict, Detail: fmt.Sprintf("the resource is at version %d, you edited version %d; reload it and merge your changes", stored.Resource.Version, expectedVersion)}
		}
		r.Code, r.SiteID = stored.Resource.Code, stored.Resource.SiteID
		if err := checkResourceRefs(ctx, tx, p, r); err != nil {
			return err
		}
		if r.EfficiencyPct.IsZero() {
			r.EfficiencyPct = decimal.NewFromInt(100)
		}
		changed := differingFields(stored.Resource, r)
		if len(changed) > 0 {
			if _, err := tx.Exec(ctx, `update resource set name = $3, type = $4, group_id = $5, calendar_id = $6, efficiency_pct = $7::numeric, capacity_per_day_h = $8::numeric,
				exclusive = $9, vendor_party_id = $10, version = version + 1 where tenant_id = $1 and id = $2::uuid`,
				p.TenantID, r.ID, r.Name, r.Type, emptyToNil(r.GroupID), emptyToNil(r.CalendarID), r.EfficiencyPct.String(), r.CapacityPerDayH.String(), r.Exclusive, emptyToNil(r.VendorPartyID)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `delete from resource_capability where tenant_id = $1 and resource_id = $2::uuid`, p.TenantID, r.ID); err != nil {
				return err
			}
			if err := insertResourceCapabilities(ctx, tx, p.TenantID, r); err != nil {
				return err
			}
			data := engevents.ResourceChangedData{ResourceID: r.ID, SiteID: stored.Resource.SiteID, CalendarID: r.CalendarID, EfficiencyPct: r.EfficiencyPct.String(), ChangedFields: changed}
			if err := emit(ctx, tx, p, r.ID, engevents.ResourceChanged, aggregateResource, r.ID, data); err != nil {
				return err
			}
		}
		if err := remember(ctx, tx, p.TenantID, idem, r.ID); err != nil {
			return err
		}
		rec, err = getResource(ctx, tx, p.TenantID, r.ID)
		return err
	})
	return rec, conflictOnDuplicate(err)
}

// DeleteResource removes a resource and its capabilities (BR-9.11). It is refused with
// ENG-RES-IN-USE while an active route version requires the resource directly; requirements by
// group, capability or type are unaffected. It emits resource.changed with the changed field
// "deleted", so schedulers that cache the resource can drop it.
func (s *Postgres) DeleteResource(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		stored, err := lockResource(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		var route string
		err = tx.QueryRow(ctx, `select v.product_id || ' ' || v.route_code || ' v' || v.version_no from operation_requirement q
			join route_version v on v.tenant_id = q.tenant_id and v.id = q.route_version_id
			where q.tenant_id = $1 and q.kind = 'resource' and q.ref_id = $2 and v.status = 'active' order by v.id limit 1`, p.TenantID, id).Scan(&route)
		if err == nil {
			return &domain.Error{Code: domain.CodeResInUse, Detail: fmt.Sprintf("resource %s is required by active route %s; change the route first", stored.Resource.Code, route)}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from resource_capability where tenant_id = $1 and resource_id = $2::uuid`, p.TenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from resource where tenant_id = $1 and id = $2::uuid`, p.TenantID, id); err != nil {
			return err
		}
		data := engevents.ResourceChangedData{ResourceID: id, SiteID: stored.Resource.SiteID, CalendarID: stored.Resource.CalendarID, EfficiencyPct: stored.Resource.EfficiencyPct.String(), ChangedFields: []string{"deleted"}}
		return emit(ctx, tx, p, id, engevents.ResourceChanged, aggregateResource, id, data)
	})
}
