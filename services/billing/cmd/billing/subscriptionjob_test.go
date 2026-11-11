package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/api"
)

type recordingBiller struct {
	mu       sync.Mutex
	entities map[string][]string
	runs     []tenant.Principal
	asOf     []time.Time
}

func (r *recordingBiller) SubscriptionLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	return r.entities[p.TenantID], nil
}

func (r *recordingBiller) BillDueSubscriptions(ctx context.Context, _ string, asOf time.Time, _ int) (api.DueResult, error) {
	p, _ := tenant.FromContext(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, p)
	r.asOf = append(r.asOf, asOf)
	return api.DueResult{Billed: 1}, nil
}

func TestSubscriptionJob_RunsEachEntityOfEachTenantScopedAndDatedToday(t *testing.T) {
	tenantA, tenantB, leA, leB := ids.New(), ids.New(), ids.New(), ids.New()
	billing := &recordingBiller{entities: map[string][]string{tenantA: {leA}, tenantB: {leB}}}
	job, err := newSubscriptionJob(env(map[string]string{"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": tenantA + "," + tenantB + "," + tenantA}), billing)
	if err != nil || job == nil {
		t.Fatalf("newSubscriptionJob: %v %v", job, err)
	}
	job.now = func() time.Time { return time.Date(2027, 3, 31, 3, 0, 0, 0, time.UTC) }
	job.once(context.Background())
	if len(billing.runs) != 2 {
		t.Fatalf("%d runs, want one per tenant entity", len(billing.runs))
	}
	own := map[string]string{tenantA: leA, tenantB: leB}
	for i, p := range billing.runs {
		if p.LegalEntityID != own[p.TenantID] || p.UserID != subscriptionJobActor || !billing.asOf[i].Equal(time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("run %d: principal %+v as of %s", i, p, billing.asOf[i])
		}
	}
}

func TestSubscriptionJob_Config(t *testing.T) {
	if j, err := newSubscriptionJob(env(nil), nil); j != nil || err != nil {
		t.Fatalf("unset: %v %v", j, err)
	}
	good := ids.New()
	for name, m := range map[string]map[string]string{
		"bad tenant":   {"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": "nope"},
		"nil tenant":   {"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": ids.Nil},
		"short period": {"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": good, "BILLING_SUBSCRIPTION_JOB_INTERVAL": "1s"},
		"bad period":   {"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": good, "BILLING_SUBSCRIPTION_JOB_INTERVAL": "soon"},
	} {
		if _, err := newSubscriptionJob(env(m), nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSubscriptionJob_DailyScheduleConfigAndNextRun(t *testing.T) {
	good := ids.New()
	job, err := newSubscriptionJob(env(map[string]string{"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": good, "BILLING_SUBSCRIPTION_JOB_AT": "03:00"}), nil)
	if err != nil || job == nil || job.at != 180 {
		t.Fatalf("03:00 = %+v, %v", job, err)
	}
	if j, err := newSubscriptionJob(env(map[string]string{"BILLING_SUBSCRIPTION_JOB_TENANT_IDS": good}), nil); err != nil || j.at != -1 {
		t.Fatalf("no schedule keeps the interval: %+v, %v", j, err)
	}
	for name, m := range map[string]map[string]string{
		"not a time":          {"BILLING_SUBSCRIPTION_JOB_AT": "3am"},
		"out of range":        {"BILLING_SUBSCRIPTION_JOB_AT": "25:00"},
		"schedule + interval": {"BILLING_SUBSCRIPTION_JOB_AT": "03:00", "BILLING_SUBSCRIPTION_JOB_INTERVAL": "2h"},
	} {
		m["BILLING_SUBSCRIPTION_JOB_TENANT_IDS"] = good
		if _, err := newSubscriptionJob(env(m), nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	for _, c := range []struct{ now, want string }{
		{"2027-03-31T02:59:59Z", "2027-03-31T03:00:00Z"},
		{"2027-03-31T03:00:00Z", "2027-04-01T03:00:00Z"},
		{"2027-03-31T23:00:00Z", "2027-04-01T03:00:00Z"},
		{"2027-12-31T10:00:00+02:00", "2028-01-01T03:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := nextDaily(now, 180).Format(time.RFC3339); got != c.want {
			t.Errorf("after %s the next run is %s, want %s", c.now, got, c.want)
		}
	}
}

func TestSubscriptionJob_DailyLoopWaitsForItsTimeAndStopsOnCancel(t *testing.T) {
	billing := &recordingBiller{entities: map[string][]string{}}
	job := &subscriptionJob{interval: time.Hour, at: 180, tenants: []string{ids.New()}, billing: billing, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { job.loop(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the daily loop ignored cancellation")
	}
	if len(billing.runs) != 0 {
		t.Errorf("the daily loop ran %d times at start, want none before its time", len(billing.runs))
	}
}
