package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

const (
	calendarGenerated = "dev.passage.engineering.calendar.generated.v1"
	resourceChanged   = "dev.passage.engineering.resource.changed.v1"
)

type calendarFixture struct {
	From, To                       string
	Template                       map[string][]domain.Shift
	ClosedDates                    []string
	ExpectedDays, ExpectedOpenDays int
	ExpectedTotalHours             string
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func loadFixture(t *testing.T) (calendarFixture, domain.WorkCalendar) {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/calendars.json")
	if err != nil {
		t.Fatal(err)
	}
	var f calendarFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	cal := domain.WorkCalendar{Code: "FIX", Name: "Fixture"}
	for wd := 0; wd < 5; wd++ {
		cal.Template[wd] = f.Template["mon-fri"]
	}
	for _, c := range f.ClosedDates {
		cal.ClosedDates = append(cal.ClosedDates, day(t, c))
	}
	return f, cal
}

func newCalendar(t *testing.T, s *store.Postgres, ctx context.Context, wc domain.WorkCalendar) store.Calendar {
	t.Helper()
	created, _, err := s.CreateCalendar(ctx, store.Calendar{ID: ids.New(), WorkCalendar: wc}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func count(t *testing.T, pool *pgxpool.Pool, ctx context.Context, query string, args ...any) (n int) {
	t.Helper()
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query, args...).Scan(&n) }); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgres_GenerateCalendar_FixtureRangeUpsertsAndEmitsOncePerCall(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	f, wc := loadFixture(t)
	cal := newCalendar(t, s, ctx, wc)
	from, to := day(t, f.From), day(t, f.To)

	got, err := s.GenerateCalendar(ctx, cal.ID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if got.OpenDays != f.ExpectedOpenDays || !got.TotalHours.Equal(decimal.RequireFromString(f.ExpectedTotalHours)) {
		t.Fatalf("generation = %+v; want %d open days and %s hours", got, f.ExpectedOpenDays, f.ExpectedTotalHours)
	}
	rows := func() int {
		return count(t, pool, ctx, `select count(*) from calendar_day where calendar_id = $1::uuid`, cal.ID)
	}
	if _, err := s.GenerateCalendar(ctx, cal.ID, from, to); err != nil || rows() != f.ExpectedDays {
		t.Fatalf("calendar_day rows after regenerating = %d, %v; want %d", rows(), err, f.ExpectedDays)
	}
	if types := outboxTypes(t, pool, ctx, cal.ID); !slices.Equal(types, []string{calendarGenerated, calendarGenerated}) {
		t.Fatalf("outbox = %v; want one calendar.generated per call", types)
	}

	later := day(t, "2029-01-31")
	if _, err := s.GenerateCalendar(ctx, cal.ID, later.AddDate(0, 0, -30), later); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, ctx, `select count(*) from work_calendar where id = $1::uuid and generated_from = $2 and generated_to = $3`, cal.ID, from, later); n != 1 {
		t.Fatalf("generated range was not widened to %s..%s", from, later)
	}
	days, err := s.ListCalendarDays(ctx, cal.ID, day(t, "2026-01-01"), day(t, "2026-01-05"))
	if err != nil || len(days) != 5 || days[0].Open || !days[1].Open || len(days[1].Shifts) != 2 || !days[1].TotalH.Equal(decimal.NewFromInt(8)) || days[2].Open {
		t.Fatalf("days = %+v, %v; want closed holiday, open Friday with two shifts, closed Saturday", days, err)
	}
}

func TestPostgres_GenerateCalendar_RejectsBadInput(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	_, wc := loadFixture(t)
	cal := newCalendar(t, s, ctx, wc)
	start := day(t, "2026-01-01")
	for _, c := range []struct {
		id, code string
		to       time.Time
	}{
		{cal.ID, domain.CodeCalRange, start.AddDate(0, 0, -1)},
		{cal.ID, domain.CodeCalRange, start.AddDate(5, 1, 0)},
		{ids.New(), store.CodeCalNotFound, start},
	} {
		_, genErr := s.GenerateCalendar(ctx, c.id, start, c.to)
		_, listErr := s.ListCalendarDays(ctx, c.id, start, c.to)
		if problemCode(genErr) != c.code && !domain.HasCode(genErr, c.code) || problemCode(listErr) != c.code && !domain.HasCode(listErr, c.code) {
			t.Fatalf("to %s: generate = %v, list = %v; want %s", c.to.Format("2006-01-02"), genErr, listErr, c.code)
		}
	}
	if n := len(outboxTypes(t, pool, ctx, cal.ID)); n != 0 {
		t.Fatalf("rejected ranges must not emit, got %d outbox rows", n)
	}
}

func TestPostgres_CreateCalendar_DuplicateInvalidAndIdempotent(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	_, wc := loadFixture(t)
	idem := store.Idem{Key: "cal-key", Hash: "h1"}
	first, _, err := s.CreateCalendar(ctx, store.Calendar{ID: ids.New(), WorkCalendar: wc}, idem)
	if err != nil {
		t.Fatal(err)
	}
	replayed, _, err := s.CreateCalendar(ctx, store.Calendar{ID: ids.New(), WorkCalendar: domain.WorkCalendar{Code: "OTHER"}}, idem)
	if err != nil || replayed.ID != first.ID || replayed.Code != wc.Code {
		t.Fatalf("replay = %+v, %v; want the first calendar", replayed, err)
	}
	overlap := domain.WorkCalendar{Code: "BAD"}
	overlap.Template[0] = []domain.Shift{{Start: "06:00", End: "10:00"}, {Start: "09:00", End: "12:00"}}
	for name, c := range map[string]struct {
		wc   domain.WorkCalendar
		idem store.Idem
		code string
	}{
		"reused key":     {wc, store.Idem{Key: "cal-key", Hash: "other"}, store.CodeIdempotencyReuse},
		"duplicate code": {wc, store.Idem{}, store.CodeDuplicate},
		"overlap":        {overlap, store.Idem{}, domain.CodeCalShift},
	} {
		_, _, err := s.CreateCalendar(ctx, store.Calendar{ID: ids.New(), WorkCalendar: c.wc}, c.idem)
		if problemCode(err) != c.code && !domain.HasCode(err, c.code) {
			t.Fatalf("%s: err = %v; want %s", name, err, c.code)
		}
	}
}

func TestPostgres_CreateGroupAndCapability_DuplicateCodeAndUnknownCalendar(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	group := domain.ResourceGroup{ID: ids.New(), Code: "CNC", Name: "CNC cell", SiteID: "S1"}
	skill := domain.Capability{ID: ids.New(), Code: "WELD", Name: "Welding"}
	idem := store.Idem{Key: "cap-key", Hash: "h"}
	_, groupErr := s.CreateResourceGroup(ctx, group, store.Idem{})
	_, skillErr := s.CreateCapability(ctx, skill, idem)
	if err := errors.Join(groupErr, skillErr); err != nil {
		t.Fatal(err)
	}
	if replayed, err := s.CreateCapability(ctx, domain.Capability{ID: ids.New(), Code: "X"}, idem); err != nil || replayed != skill {
		t.Fatalf("capability replay = %+v, %v; want %+v", replayed, err, skill)
	}
	group.ID, skill.ID = ids.New(), ids.New()
	_, groupErr = s.CreateResourceGroup(ctx, group, store.Idem{})
	_, skillErr = s.CreateCapability(ctx, skill, store.Idem{})
	if problemCode(groupErr) != store.CodeDuplicate || problemCode(skillErr) != store.CodeDuplicate {
		t.Fatalf("duplicate codes: group = %v, capability = %v; want %s", groupErr, skillErr, store.CodeDuplicate)
	}
	group.Code, group.CalendarID = "CNC2", ids.New()
	if _, err := s.CreateResourceGroup(ctx, group, store.Idem{}); problemCode(err) != store.CodeCalNotFound {
		t.Fatalf("unknown group calendar = %v; want %s", err, store.CodeCalNotFound)
	}
}

func machine(code, site string) domain.Resource {
	return domain.Resource{ID: ids.New(), Code: code, Name: code, Type: domain.TypeMachine, SiteID: site, CapacityPerDayH: decimal.NewFromInt(8)}
}

func TestPostgres_CreateResource_PersistsCapabilitiesAndEmitsOnce(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	skill := domain.Capability{ID: ids.New(), Code: "WELD", Name: "Welding"}
	if _, err := s.CreateCapability(ctx, skill, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	r := machine("M1", "S1")
	r.Capabilities = []domain.ResourceCapability{{CapabilityID: skill.ID, Level: 3, EffectiveFrom: day(t, "2026-01-01")}}
	rec, err := s.CreateResource(ctx, r, store.Idem{Key: "res-key", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Resource.Capabilities) != 1 || rec.Resource.Capabilities[0].Level != 3 || !rec.Resource.EfficiencyPct.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("resource = %+v; want one level-3 capability and efficiency defaulted to 100", rec.Resource)
	}
	if types := outboxTypes(t, pool, ctx, r.ID); !slices.Equal(types, []string{resourceChanged}) {
		t.Fatalf("outbox = %v; want exactly one resource.changed", types)
	}
	wantFields := `["code","name","type","siteId","efficiencyPct","capacityPerDayH","exclusive","capabilities"]`
	if n := count(t, pool, ctx, `select count(*) from outbox where aggregate_id = $1 and payload->'data'->'changedFields' = $2::jsonb and payload->'data'->>'efficiencyPct' = '100'`, r.ID, wantFields); n != 1 {
		t.Fatalf("resource.changed must list changedFields %s with efficiency 100", wantFields)
	}
}

func TestPostgres_CreateResource_RejectsInvalidReferences(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	group := domain.ResourceGroup{ID: ids.New(), Code: "G", Name: "G", SiteID: "S1"}
	if _, err := s.CreateResourceGroup(ctx, group, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	vendor := machine("V1", "S1")
	vendor.Type = domain.TypeVendor
	otherSite, unknownCalendar, unknownGroup, unknownCapability := machine("R2", "S2"), machine("R3", "S1"), machine("R4", "S1"), machine("R5", "S1")
	otherSite.GroupID, unknownCalendar.CalendarID, unknownGroup.GroupID = group.ID, ids.New(), ids.New()
	unknownCapability.Capabilities = []domain.ResourceCapability{{CapabilityID: ids.New(), Level: 1, EffectiveFrom: day(t, "2026-01-01")}}
	if _, err := s.CreateResource(ctx, machine("DUP", "S1"), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		r    domain.Resource
		code string
	}{
		{"vendor without party", vendor, domain.CodeResVendor},
		{"group on another site", otherSite, domain.CodeResSite},
		{"unknown calendar", unknownCalendar, store.CodeCalNotFound},
		{"unknown group", unknownGroup, store.CodeResNotFound},
		{"unknown capability", unknownCapability, store.CodeResNotFound},
		{"duplicate code", machine("DUP", "S1"), store.CodeDuplicate},
	}
	for _, c := range cases {
		_, err := s.CreateResource(ctx, c.r, store.Idem{})
		if problemCode(err) != c.code && !domain.HasCode(err, c.code) {
			t.Fatalf("%s: err = %v; want %s", c.name, err, c.code)
		}
		if n := count(t, pool, ctx, `select (select count(*) from resource where id::text = $1) + (select count(*) from outbox where aggregate_id = $1)`, c.r.ID); n != 0 {
			t.Fatalf("%s: rejected resource left %d rows", c.name, n)
		}
	}
}

func TestPostgres_GetResource_EffectiveCalendarAndNotFound(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	_, wc := loadFixture(t)
	groupCal, ownCal := newCalendar(t, s, ctx, wc), newCalendar(t, s, ctx, domain.WorkCalendar{Code: "OWN", Name: "Own"})
	group := domain.ResourceGroup{ID: ids.New(), Code: "G", Name: "G", SiteID: "S1", CalendarID: groupCal.ID}
	if _, err := s.CreateResourceGroup(ctx, group, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	inherits, owns := machine("INH", "S1"), machine("OWN", "S1")
	inherits.GroupID, owns.GroupID, owns.CalendarID = group.ID, group.ID, ownCal.ID
	_, inheritsErr := s.CreateResource(ctx, inherits, store.Idem{})
	_, ownsErr := s.CreateResource(ctx, owns, store.Idem{})
	if err := errors.Join(inheritsErr, ownsErr); err != nil {
		t.Fatal(err)
	}
	for r, want := range map[string]string{inherits.ID: groupCal.ID, owns.ID: ownCal.ID} {
		rec, err := s.GetResource(ctx, r)
		if err != nil || rec.EffectiveCalendarID != want {
			t.Fatalf("effective calendar = %q, %v; want %q", rec.EffectiveCalendarID, err, want)
		}
	}
	if _, err := s.GetResource(ctx, ids.New()); problemCode(err) != store.CodeResNotFound {
		t.Fatalf("unknown resource = %v; want %s", err, store.CodeResNotFound)
	}
}

func TestPostgres_RLS_ResourceTables(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	tables := []string{"work_calendar", "calendar_day", "resource_group", "capability", "resource", "resource_capability"}
	for _, table := range tables {
		var enabled, forced bool
		if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where oid = $1::regclass`, table).Scan(&enabled, &forced); err != nil || !enabled || !forced {
			t.Fatalf("%s: row security enabled=%v forced=%v, %v", table, enabled, forced, err)
		}
	}

	s := store.New(app)
	owner, intruder := principalContext(1), principalContext(2)
	_, wc := loadFixture(t)
	cal := newCalendar(t, s, owner, wc)
	skill, group := domain.Capability{ID: ids.New(), Code: "WELD", Name: "Welding"}, domain.ResourceGroup{ID: ids.New(), Code: "G", Name: "G", SiteID: "S1"}
	r := machine("M1", "S1")
	r.GroupID, r.Capabilities = group.ID, []domain.ResourceCapability{{CapabilityID: skill.ID, Level: 1, EffectiveFrom: day(t, "2026-01-01")}}
	_, generateErr := s.GenerateCalendar(owner, cal.ID, day(t, "2026-01-01"), day(t, "2026-01-07"))
	_, skillErr := s.CreateCapability(owner, skill, store.Idem{})
	_, groupErr := s.CreateResourceGroup(owner, group, store.Idem{})
	_, resourceErr := s.CreateResource(owner, r, store.Idem{})
	if err := errors.Join(generateErr, skillErr, groupErr, resourceErr); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetResource(intruder, r.ID); problemCode(err) != store.CodeResNotFound {
		t.Fatalf("tenant 2 reading tenant 1 resource = %v; want %s", err, store.CodeResNotFound)
	}
	for _, table := range tables {
		if n := count(t, app, intruder, `select count(*) from `+table); n != 0 {
			t.Fatalf("tenant 2 sees %d %s rows", n, table)
		}
		if n := count(t, app, owner, `select count(*) from `+table); n == 0 {
			t.Fatalf("tenant 1 sees no %s rows", table)
		}
	}
}
