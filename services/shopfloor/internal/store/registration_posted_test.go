package store

import (
	"testing"

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
