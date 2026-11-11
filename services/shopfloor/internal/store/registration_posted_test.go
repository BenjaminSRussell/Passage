package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// FR-25.4: a closed job-stop's registration.posted names the production order,
// the operation and the worker's cost rate so w21 can post the job-card (BR-25.9).
func TestRegistrationPosted_JobStopCarriesOrderAndCostRate(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(85)
	ctx := testkit.Ctx(tenantID)
	badge := "B-RATE"
	w, err := s.CreateWorker(ctx, Worker{PartyID: ids.New(), Name: "Ana Silva", BadgeID: &badge,
		CostRate: decimal.RequireFromString("26"), Currency: "USD"}, idem("worker-rate-"+ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	orderID := ids.New()
	job, created, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: orderID, OperationRef: "20",
		ResourceID: ids.New(), PlannedQty: decimal.NewFromInt(100), SequenceStart: day(8, 0)})
	if err != nil || !created {
		t.Fatalf("job: created=%v err=%v", created, err)
	}
	stopAt := day(10, 0)
	h := domain.ElapsedHours(day(8, 0), stopAt)
	stop := Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &job.ID,
		StartedAt: day(8, 0), StoppedAt: &stopAt, Hours: &h}
	res := register(t, s, ctx, stop)
	if res[0].Status != StatusAccepted {
		t.Fatalf("stop = %+v", res[0])
	}

	var posted sfevents.RegistrationPostedData
	lastEventData(t, admin, tenantID, sfevents.RegistrationPosted, &posted)
	if posted.RegistrationID != stop.ID || posted.Kind != KindJobStop || posted.OrderRef != orderID ||
		posted.OperationRef != "20" || posted.CostRate == nil || !posted.CostRate.Equal(decimal.RequireFromString("26")) ||
		posted.CostRateCurrency != "USD" || posted.Hours == nil || !posted.Hours.Equal(h) {
		t.Fatalf("registration.posted = %+v, want order %s op 20 rate 26", posted, orderID)
	}
}

func TestRegistrationPosted_PunchCarriesNoCostRateAndRateKeepsWorkerCurrency(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(86)
	ctx := testkit.Ctx(tenantID)
	badge := "B-EUR"
	w, err := s.CreateWorker(ctx, Worker{PartyID: ids.New(), Name: "Ola Berg", BadgeID: &badge,
		CostRate: decimal.RequireFromString("26"), Currency: "EUR"}, idem("worker-eur-"+ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	punch := Registration{ID: ids.New(), WorkerID: w.ID, Kind: domain.KindClockIn, StartedAt: day(8, 0)}
	if res := register(t, s, ctx, punch); res[0].Status != StatusAccepted {
		t.Fatalf("punch = %+v", res[0])
	}
	var posted sfevents.RegistrationPostedData
	lastEventData(t, admin, tenantID, sfevents.RegistrationPosted, &posted)
	if posted.CostRate != nil || posted.CostRateCurrency != "" {
		t.Fatalf("a punch carries the pay rate: %+v", posted)
	}

	job, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: ids.New(), OperationRef: "20",
		ResourceID: ids.New(), PlannedQty: decimal.NewFromInt(10), SequenceStart: day(8, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if res := register(t, s, ctx, Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStart, JobID: &job.ID, StartedAt: day(8, 0)}); res[0].Status != StatusAccepted {
		t.Fatalf("start = %+v", res[0])
	}
	lastEventData(t, admin, tenantID, sfevents.RegistrationPosted, &posted)
	if posted.Kind != KindJobStart || posted.CostRate != nil {
		t.Fatalf("a job-start carries the pay rate: %+v", posted)
	}
	stopAt := day(10, 0)
	h := domain.ElapsedHours(day(8, 0), stopAt)
	stop := Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &job.ID, StartedAt: day(8, 0), StoppedAt: &stopAt, Hours: &h}
	if res := register(t, s, ctx, stop); res[0].Status != StatusAccepted {
		t.Fatalf("stop = %+v", res[0])
	}
	lastEventData(t, admin, tenantID, sfevents.RegistrationPosted, &posted)
	if posted.CostRate == nil || posted.CostRateCurrency != "EUR" {
		t.Fatalf("job-stop rate = %v %q, want 26 EUR", posted.CostRate, posted.CostRateCurrency)
	}
}

func mustRate(t *testing.T, s *Postgres, ctx context.Context, workerID, from, rate string) {
	t.Helper()
	if _, err := s.AddCostRate(ctx, workerID, CostRateInput{EffectiveFrom: from, CostRate: decimal.RequireFromString(rate)}, "sup", idem("rate-"+ids.New())); err != nil {
		t.Fatal(err)
	}
}

// Rounding rule: none. Job-card cost in minor units is rate x hours x 100 and must be a whole
// number; a rate/hours pair that would need rounding fails the test instead of being rounded.
func exactMinorUnits(t *testing.T, rate, hours decimal.Decimal) int64 {
	t.Helper()
	minor := rate.Mul(hours).Mul(decimal.NewFromInt(100))
	if !minor.IsInteger() {
		t.Fatalf("cost %s x %s h = %s minor units needs rounding", rate, hours, minor)
	}
	return minor.IntPart()
}

func stopCost(t *testing.T, s *Postgres, ctx context.Context, admin *pgxpool.Pool, tenantID, workerID string, jobID *string, from, to time.Time) int64 {
	t.Helper()
	h := domain.ElapsedHours(from, to)
	stop := Registration{ID: ids.New(), WorkerID: workerID, Kind: KindJobStop, JobID: jobID, StartedAt: from, StoppedAt: &to, Hours: &h}
	if res := register(t, s, ctx, stop); res[0].Status != StatusAccepted {
		t.Fatalf("stop = %+v", res[0])
	}
	var posted sfevents.RegistrationPostedData
	lastEventData(t, admin, tenantID, sfevents.RegistrationPosted, &posted)
	if posted.RegistrationID != stop.ID || posted.CostRate == nil || posted.Hours == nil {
		t.Fatalf("registration.posted = %+v", posted)
	}
	return exactMinorUnits(t, *posted.CostRate, *posted.Hours)
}

// FR-25.4, BR-25.9, D2a': the job-card rate is the one effective on the day the work was done, not
// the worker's current rate. Two operators on one job each carry their own dated rate (AC1), and a
// raise dated after the work, or set later with a back date, never reprices it.
func TestRegistrationPosted_JobStopUsesRateEffectiveOnWorkDate(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(87)
	ctx := testkit.Ctx(tenantID)
	newWorker := func(badge, rate string) Worker {
		w, err := s.CreateWorker(ctx, Worker{PartyID: ids.New(), Name: badge, BadgeID: &badge,
			CostRate: decimal.RequireFromString(rate), Currency: "USD"}, idem("worker-"+ids.New()))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	ana, ola := newWorker("B-ANA", "26"), newWorker("B-OLA", "40")
	mustRate(t, s, ctx, ana.ID, "2026-10-01", "28")
	mustRate(t, s, ctx, ana.ID, "2026-10-21", "30")
	mustRate(t, s, ctx, ola.ID, "2026-10-20", "44")
	job, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: ids.New(), OperationRef: "20",
		ResourceID: ids.New(), PlannedQty: decimal.NewFromInt(100), SequenceStart: day(8, 0)})
	if err != nil {
		t.Fatal(err)
	}

	anaCost := stopCost(t, s, ctx, admin, tenantID, ana.ID, &job.ID, day(8, 0), day(10, 0))
	olaCost := stopCost(t, s, ctx, admin, tenantID, ola.ID, &job.ID, day(9, 0), day(10, 30))
	if anaCost != 5600 || olaCost != 6600 || anaCost+olaCost != 12200 {
		t.Fatalf("job cost = ana %d + ola %d minor units, want 5600 + 6600 = 12200", anaCost, olaCost)
	}

	mustRate(t, s, ctx, ana.ID, "2026-10-10", "35")
	if got := stopCost(t, s, ctx, admin, tenantID, ana.ID, &job.ID, day(11, 0), day(12, 0)); got != 3500 {
		t.Fatalf("work on 2026-10-20 after a rate backdated to 10-10 = %d, want 3500", got)
	}

	before := time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	if got := stopCost(t, s, ctx, admin, tenantID, ana.ID, &job.ID, before, before.Add(time.Hour)); got != 2600 {
		t.Fatalf("work before the first dated rate = %d, want the worker's base rate 2600", got)
	}
}

// #543: the work date of a job stop is the local day in the worker's time zone. Work that started at 21:00 in Chicago on
// 20 October is already 21 October in UTC, and a rate that takes effect on 21 October must not price it.
func TestRegistrationPosted_JobStopWorkDateIsLocalToTheWorkersSite(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(88)
	ctx := testkit.Ctx(tenantID)
	newWorker := func(badge, zone string) Worker {
		w, err := s.CreateWorker(ctx, Worker{PartyID: ids.New(), Name: badge, BadgeID: &badge, CostRate: decimal.RequireFromString("26"), Currency: "USD", TimeZone: zone}, idem("worker-"+ids.New()))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	chicago, utc := newWorker("B-CHI", "America/Chicago"), newWorker("B-UTC", "")
	if chicago.TimeZone != "America/Chicago" || utc.TimeZone != "UTC" {
		t.Fatalf("zones %q %q; want the one given and UTC by default", chicago.TimeZone, utc.TimeZone)
	}
	for _, w := range []Worker{chicago, utc} {
		mustRate(t, s, ctx, w.ID, "2026-10-20", "28")
		mustRate(t, s, ctx, w.ID, "2026-10-21", "30")
	}
	job, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: ids.New(), OperationRef: "20", ResourceID: ids.New(), PlannedQty: decimal.NewFromInt(100), SequenceStart: day(8, 0)})
	if err != nil {
		t.Fatal(err)
	}
	// 02:00 UTC on 21 October is 21:00 CDT on 20 October.
	from := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	if got := stopCost(t, s, ctx, admin, tenantID, chicago.ID, &job.ID, from, from.Add(time.Hour)); got != 2800 {
		t.Fatalf("Chicago evening work of 20 October = %d, want the rate of 20 October (2800)", got)
	}
	if got := stopCost(t, s, ctx, admin, tenantID, utc.ID, &job.ID, from, from.Add(time.Hour)); got != 3000 {
		t.Fatalf("the same instant for a UTC worker = %d, want the rate of 21 October (3000)", got)
	}
	// A zone can be changed with the worker's edit; an omitted zone keeps the stored one.
	cur, err := s.GetWorker(ctx, chicago.ID)
	if err != nil {
		t.Fatal(err)
	}
	upd, err := s.UpdateWorker(ctx, chicago.ID, cur.Version, WorkerInput{Name: cur.Name, BadgeID: cur.BadgeID, CostRate: cur.CostRate, Currency: cur.Currency}, "sup", idem("upd-"+ids.New()))
	if err != nil || upd.TimeZone != "America/Chicago" {
		t.Fatalf("an edit without a zone: %+v %v", upd, err)
	}
	upd, err = s.UpdateWorker(ctx, chicago.ID, upd.Version, WorkerInput{Name: cur.Name, BadgeID: cur.BadgeID, CostRate: cur.CostRate, Currency: cur.Currency, TimeZone: "Europe/Berlin"}, "sup", idem("upd-"+ids.New()))
	if err != nil || upd.TimeZone != "Europe/Berlin" {
		t.Fatalf("an edit with a zone: %+v %v", upd, err)
	}
}
