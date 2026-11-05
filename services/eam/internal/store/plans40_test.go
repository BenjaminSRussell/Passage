package store

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// plans40 is testdata/eam-plans-40/fixture.json (see its comment for the
// simulated day).
type plans40 struct {
	HistoryStart string   `json:"historyStart"`
	SimStart     string   `json:"simStart"`
	Days         int      `json:"days"`
	Assets       []string `json:"assets"`
	Counters     []struct {
		Name          string  `json:"name"`
		Asset         string  `json:"asset"`
		Initial       float64 `json:"initial"`
		ReadEveryDays int     `json:"readEveryDays"`
		WeekdaysOnly  bool    `json:"weekdaysOnly"`
		WeekendsOnly  bool    `json:"weekendsOnly"`
		Profile       []struct {
			From string  `json:"from"`
			Rate float64 `json:"rate"`
		} `json:"profile"`
	} `json:"counters"`
	Plans []struct {
		Code                 string     `json:"code"`
		Asset                string     `json:"asset"`
		Kind                 string     `json:"kind"`
		IntervalValue        *int       `json:"intervalValue"`
		IntervalUnit         *string    `json:"intervalUnit"`
		Counter              string     `json:"counter"`
		CounterInterval      *float64   `json:"counterInterval"`
		EarlyTolerancePct    *int       `json:"earlyTolerancePct"`
		HorizonDays          *int       `json:"horizonDays"`
		StartAt              *time.Time `json:"startAt"`
		LastCompletedAt      *time.Time `json:"lastCompletedAt"`
		LastCompletedReading *float64   `json:"lastCompletedReading"`
		CompleteOffsetDays   int        `json:"completeOffsetDays"`
	} `json:"plans"`
}

type plans40Order struct {
	GeneratedAt string   `json:"generatedAt"`
	DueAt       string   `json:"dueAt"`
	DueReading  *float64 `json:"dueReading"`
}

type plans40Final struct {
	LastCompletedAt *string  `json:"lastCompletedAt"`
	NextDueAt       *string  `json:"nextDueAt"`
	NextDueReading  *float64 `json:"nextDueReading"`
}

type plans40Expected struct {
	Plans map[string]struct {
		Orders []plans40Order `json:"orders"`
		Final  plans40Final   `json:"final"`
	} `json:"plans"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func isoZ(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

func optISO(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := isoZ(*t)
	return &s
}

func eqStr(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func eqNum(a, b *float64) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func fmtPtr[T any](p *T) string {
	if p == nil {
		return "null"
	}
	return fmt.Sprint(*p)
}

// TestPlans40_SimulatedYear covers AC-44.2 (ticket Proof 1): the 40 plan
// fixtures of testdata/eam-plans-40 (TIME, COUNTER, TIME_OR_COUNTER and
// TIME_AND_COUNTER lines on 14 counters with constant, weekday-only,
// weekend-only, seasonal, ramping, idle and weekly-read usage, month ends and
// a leap day) run through the database for 365 simulated days: readings at
// 00:00, completion of due orders at 12:00, the nightly sweep at 23:00. Every
// generated work order (when it appeared, its due date and due reading) and
// every line's final state must equal reference_plans.py (python3 -I, exact
// rationals) within 0 days and 0 units.
func TestPlans40_SimulatedYear(t *testing.T) {
	var fx plans40
	var want plans40Expected
	readJSON(t, "../../testdata/eam-plans-40/fixture.json", &fx)
	readJSON(t, "../../testdata/eam-plans-40/expected.json", &want)

	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "eam.work.plan", "eam.plan.admin", "eam.counter.register")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var now time.Time
	s := NewWithClock(pool, func() time.Time { return now })
	tenant := testkit.TenantID(1)
	parseDay := func(v string) time.Time {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			t.Fatal(err)
		}
		return d.UTC()
	}
	hist, sim := parseDay(fx.HistoryStart), parseDay(fx.SimStart)
	end := sim.AddDate(0, 0, fx.Days)
	now = hist

	assets := map[string]string{}
	for _, name := range fx.Assets {
		a, err := s.CreateAsset(ctx, Asset{Name: name, AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 3}, "asset-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
			t.Fatal(err)
		}
		assets[name] = a.ID
	}
	counters := map[string]string{}
	values := map[string]float64{}
	names := []string{}
	for _, c := range fx.Counters {
		got, err := s.CreateCounter(ctx, assets[c.Asset], Counter{Name: c.Name, Unit: "h", Cumulative: true, Source: "MANUAL"}, "counter-"+c.Name)
		if err != nil {
			t.Fatal(err)
		}
		counters[c.Name] = got.ID
		values[c.Name] = c.Initial
		names = append(names, c.Name)
	}
	sort.Strings(names)
	spec := map[string]int{}
	for i, c := range fx.Counters {
		spec[c.Name] = i
	}
	offset := map[string]int{}
	for _, p := range fx.Plans {
		offset[p.Code] = p.CompleteOffsetDays
	}

	type order struct {
		id, code, status string
		due              time.Time
		reading          *float64
		version          int
	}
	snapshot := func() []order {
		var out []order
		err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `select w.id::text, p.code, w.status, w.due_at, pl.next_due_reading::float8, w.version
			  from work_order w
			  join plan_line pl on pl.tenant_id = w.tenant_id and pl.id = w.plan_line_id
			  join maintenance_plan p on p.tenant_id = pl.tenant_id and p.id = pl.plan_id
			  where w.tenant_id = $1 order by p.code, w.due_at`, tenant)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var o order
				if err := rows.Scan(&o.id, &o.code, &o.status, &o.due, &o.reading, &o.version); err != nil {
					return err
				}
				out = append(out, o)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	seen := map[string]bool{}
	got := map[string][]plans40Order{}
	observe := func(at time.Time) []order {
		all := snapshot()
		for _, o := range all {
			if seen[o.id] {
				continue
			}
			seen[o.id] = true
			got[o.code] = append(got[o.code], plans40Order{GeneratedAt: isoZ(at), DueAt: isoZ(o.due), DueReading: o.reading})
		}
		return all
	}

	var open []order
	for d := hist; d.Before(end); d = d.AddDate(0, 0, 1) {
		now = d
		for _, name := range names {
			c := fx.Counters[spec[name]]
			if int(d.Sub(hist).Hours()/24)%c.ReadEveryDays != 0 {
				continue
			}
			if _, _, err := s.RecordReading(ctx, counters[name], ReadingInput{Value: values[name], ReadingAt: d, Source: "MANUAL"}, ""); err != nil {
				t.Fatalf("%s reading %s: %v", name, d.Format("2006-01-02"), err)
			}
		}
		if d.Equal(sim) {
			for _, p := range fx.Plans {
				line := PlanLineInput{AssetID: assets[p.Asset], Kind: p.Kind, IntervalValue: p.IntervalValue, IntervalUnit: p.IntervalUnit,
					CounterInterval: p.CounterInterval, JobType: "PM_" + p.Code, EarlyTolerancePct: p.EarlyTolerancePct,
					HorizonDays: p.HorizonDays, StartAt: p.StartAt, LastCompletedAt: p.LastCompletedAt, LastCompletedReading: p.LastCompletedReading}
				if p.Counter != "" {
					id := counters[p.Counter]
					line.CounterID = &id
				}
				if _, err := s.CreatePlan(ctx, PlanInput{Code: p.Code, Name: "Plan " + p.Code, Currency: "USD", Lines: []PlanLineInput{line}}, "plan-"+p.Code); err != nil {
					t.Fatalf("plan %s: %v", p.Code, err)
				}
			}
		}
		if !d.Before(sim) {
			open = observe(d)
			noon := d.Add(12 * time.Hour)
			now = noon
			begin := d.Add(10 * time.Hour)
			for _, o := range open {
				if o.status == "COMPLETED" {
					continue
				}
				dueDay := time.Date(o.due.UTC().Year(), o.due.UTC().Month(), o.due.UTC().Day(), 0, 0, 0, 0, time.UTC)
				if dueDay.AddDate(0, 0, offset[o.code]).After(d) {
					continue
				}
				v := o.version
				for _, st := range []domain.Status{domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress} {
					wo, err := s.TransitionWorkOrder(ctx, o.id, Transition{To: st, Version: v}, "")
					if err != nil {
						t.Fatalf("%s %s to %s: %v", o.code, d.Format("2006-01-02"), st, err)
					}
					v = wo.Version
				}
				if _, err := s.TransitionWorkOrder(ctx, o.id, Transition{To: domain.StatusCompleted, Version: v,
					ActualStart: &begin, ActualEnd: &noon}, ""); err != nil {
					t.Fatalf("%s %s complete: %v", o.code, d.Format("2006-01-02"), err)
				}
			}
			observe(noon)
			now = d.Add(23 * time.Hour)
			if _, err := s.SchedulePlans(ctx); err != nil {
				t.Fatal(err)
			}
			observe(now)
		}
		for _, name := range names {
			c := fx.Counters[spec[name]]
			rate := 0.0
			for _, step := range c.Profile {
				if !parseDay(step.From).After(d) {
					rate = step.Rate
				}
			}
			weekend := d.Weekday() == time.Saturday || d.Weekday() == time.Sunday
			if (c.WeekdaysOnly && weekend) || (c.WeekendsOnly && !weekend) {
				rate = 0
			}
			values[name] += rate
		}
	}

	finals := map[string]plans40Final{}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select p.code, pl.last_completed_at, pl.next_due_at, pl.next_due_reading::float8
		  from plan_line pl join maintenance_plan p on p.tenant_id = pl.tenant_id and p.id = pl.plan_id
		  where pl.tenant_id = $1`, tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			var last, next *time.Time
			var reading *float64
			if err := rows.Scan(&code, &last, &next, &reading); err != nil {
				return err
			}
			finals[code] = plans40Final{LastCompletedAt: optISO(last), NextDueAt: optISO(next), NextDueReading: reading}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(want.Plans) != 40 || len(fx.Plans) != 40 {
		t.Fatalf("fixture has %d plans and %d expectations, want 40", len(fx.Plans), len(want.Plans))
	}
	total, matched, exactPlans := 0, 0, 0
	for _, p := range fx.Plans {
		exp := want.Plans[p.Code]
		g := got[p.Code]
		total += len(exp.Orders)
		bad := 0
		for i := 0; i < len(exp.Orders) || i < len(g); i++ {
			switch {
			case i >= len(g):
				t.Errorf("%s order %d: missing, want generated %s due %s reading %s", p.Code, i+1,
					exp.Orders[i].GeneratedAt, exp.Orders[i].DueAt, fmtPtr(exp.Orders[i].DueReading))
			case i >= len(exp.Orders):
				t.Errorf("%s order %d: unexpected, generated %s due %s reading %s", p.Code, i+1,
					g[i].GeneratedAt, g[i].DueAt, fmtPtr(g[i].DueReading))
			case g[i].GeneratedAt != exp.Orders[i].GeneratedAt || g[i].DueAt != exp.Orders[i].DueAt ||
				!eqNum(g[i].DueReading, exp.Orders[i].DueReading):
				t.Errorf("%s order %d: generated %s due %s reading %s, want generated %s due %s reading %s", p.Code, i+1,
					g[i].GeneratedAt, g[i].DueAt, fmtPtr(g[i].DueReading),
					exp.Orders[i].GeneratedAt, exp.Orders[i].DueAt, fmtPtr(exp.Orders[i].DueReading))
			default:
				matched++
				continue
			}
			if bad++; bad == 3 {
				break
			}
		}
		f := finals[p.Code]
		finalOK := eqStr(f.LastCompletedAt, exp.Final.LastCompletedAt) && eqStr(f.NextDueAt, exp.Final.NextDueAt) &&
			eqNum(f.NextDueReading, exp.Final.NextDueReading)
		if !finalOK {
			t.Errorf("%s final: last completed %s next due %s reading %s, want %s %s %s", p.Code,
				fmtPtr(f.LastCompletedAt), fmtPtr(f.NextDueAt), fmtPtr(f.NextDueReading),
				fmtPtr(exp.Final.LastCompletedAt), fmtPtr(exp.Final.NextDueAt), fmtPtr(exp.Final.NextDueReading))
		}
		if bad == 0 && finalOK && len(g) == len(exp.Orders) {
			exactPlans++
		}
	}
	t.Logf("eam-plans-40: %d/40 plans exact, %d/%d work orders match (generated at, due date, due reading)", exactPlans, matched, total)
}
