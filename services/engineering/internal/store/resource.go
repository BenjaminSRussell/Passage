package store

import (
	"context"
	"encoding/json"
	"errors"
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
	resourceColumns = `id::text, code, name, type, site_id, coalesce(group_id::text, ''), coalesce(calendar_id::text, ''), efficiency_pct::text, capacity_per_day_h::text, exclusive, coalesce(vendor_party_id, '')`
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

func getCalendar(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Calendar, error) {
	query := `select ` + calendarColumns + ` from work_calendar where tenant_id = $1 and id = $2::uuid`
	if lock {
		query += ` for update`
	}
	var c Calendar
	var from, to *time.Time
	err := tx.QueryRow(ctx, query, tenantID, id).Scan(&c.ID, &c.Code, &c.Name, &c.Template, &c.ClosedDates, &from, &to, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, notFound(CodeCalNotFound, "calendar not found")
	}
	c.GeneratedFrom, c.GeneratedTo = valueOrZero(from), valueOrZero(to)
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

func getResource(ctx context.Context, tx pgx.Tx, tenantID, id string) (ResourceRecord, error) {
	var r domain.Resource
	var efficiency, capacity string
	err := tx.QueryRow(ctx, `select `+resourceColumns+` from resource where tenant_id = $1 and id = $2::uuid`, tenantID, id).
		Scan(&r.ID, &r.Code, &r.Name, &r.Type, &r.SiteID, &r.GroupID, &r.CalendarID, &efficiency, &capacity, &r.Exclusive, &r.VendorPartyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ResourceRecord{}, notFound(CodeResNotFound, "resource not found")
	}
	if err != nil {
		return ResourceRecord{}, err
	}
	var failure error
	r.EfficiencyPct, r.CapacityPerDayH = parseDecimal(efficiency, &failure), parseDecimal(capacity, &failure)
	if failure != nil {
		return ResourceRecord{}, failure
	}
	rows, err := tx.Query(ctx, `select capability_id::text, level, effective_from, effective_to from resource_capability where tenant_id = $1 and resource_id = $2::uuid order by capability_id, effective_from`, tenantID, id)
	if err != nil {
		return ResourceRecord{}, err
	}
	r.Capabilities, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (c domain.ResourceCapability, err error) {
		var to *time.Time
		if err = row.Scan(&c.CapabilityID, &c.Level, &c.EffectiveFrom, &to); err == nil {
			c.EffectiveTo = valueOrZero(to)
		}
		return c, err
	})
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

func insertResource(ctx context.Context, tx pgx.Tx, p tenant.Principal, r domain.Resource) error {
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
	efficiency := r.EfficiencyPct
	if efficiency.IsZero() {
		efficiency = decimal.NewFromInt(100)
	}
	if _, err := tx.Exec(ctx, `insert into resource (tenant_id, id, code, name, type, site_id, group_id, calendar_id, efficiency_pct, capacity_per_day_h, exclusive, vendor_party_id)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11, $12)`,
		p.TenantID, r.ID, r.Code, r.Name, r.Type, r.SiteID, emptyToNil(r.GroupID), emptyToNil(r.CalendarID), efficiency.String(), r.CapacityPerDayH.String(), r.Exclusive, emptyToNil(r.VendorPartyID)); err != nil {
		return err
	}
	batch := &pgx.Batch{}
	for _, c := range r.Capabilities {
		batch.Queue(`insert into resource_capability (tenant_id, resource_id, capability_id, effective_from, level, effective_to) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, r.ID, c.CapabilityID, c.EffectiveFrom, c.Level, zeroToNil(c.EffectiveTo))
	}
	if err := execBatch(ctx, tx, batch); err != nil {
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
