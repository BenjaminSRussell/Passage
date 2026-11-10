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
