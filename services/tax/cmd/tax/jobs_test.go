package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

type fakeScheduled struct {
	mu          sync.Mutex
	settleErr   map[string]error
	settleRuns  []tenant.Principal
	periods     []domain.Period
	recommitRun []tenant.Principal
	entities    map[string][]string
}

func (f *fakeScheduled) RunSettlement(ctx context.Context, p domain.Period) (store.Run, error) {
	pr, _ := tenant.FromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.settleRuns = append(f.settleRuns, pr)
	f.periods = append(f.periods, p)
	if err := f.settleErr[pr.LegalEntityID]; err != nil {
		return store.Run{}, err
	}
	return store.Run{ID: "run"}, nil
}

func (f *fakeScheduled) RunRecommit(ctx context.Context, _ int) (store.RecommitSummary, error) {
	pr, _ := tenant.FromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recommitRun = append(f.recommitRun, pr)
	return store.RecommitSummary{Completed: 1}, nil
}

func (f *fakeScheduled) LedgerConfigEntities(ctx context.Context) ([]string, error) {
	pr, _ := tenant.FromContext(ctx)
	return f.entities[pr.TenantID], nil
}
func (f *fakeScheduled) RecommitEntities(ctx context.Context) ([]string, error) {
	return f.LedgerConfigEntities(ctx)
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSettlementJob_SettlesThePreviousMonthPerEntityAndKeepsGoingAfterARefusal(t *testing.T) {
	a, leA, leB := ids.New(), ids.New(), ids.New()
	f := &fakeScheduled{entities: map[string][]string{a: {leA, leB}},
		settleErr: map[string]error{leA: &domain.Error{Code: domain.CodeSettlementMismatch, Detail: "differs"}}}
	job, err := newSettlementJob(env(map[string]string{"TAX_SETTLEMENT_TENANT_IDS": a + "," + a}), f, f)
	if err != nil || job == nil {
		t.Fatalf("job = %v, %v", job, err)
	}
	job.now = func() time.Time { return time.Date(2027, 3, 5, 4, 0, 1, 0, time.UTC) }
	job.once(context.Background())
	if len(f.settleRuns) != 2 {
		t.Fatalf("%d runs, want one per entity even though the first was refused", len(f.settleRuns))
	}
	for i, p := range f.settleRuns {
		if p.TenantID != a || p.UserID != settlementActor || p.LegalEntityID == "" {
			t.Errorf("run %d principal = %+v", i, p)
		}
		if f.periods[i].From.Format("2006-01-02") != "2027-02-01" || f.periods[i].To.Format("2006-01-02") != "2027-02-28" {
			t.Errorf("run %d period = %v", i, f.periods[i])
		}
	}
	// January: leap-year February and a year boundary.
	if p := previousMonth(time.Date(2028, 3, 5, 0, 0, 0, 0, time.UTC)); p.To.Format("2006-01-02") != "2028-02-29" {
		t.Errorf("leap February ends %s", p.To.Format("2006-01-02"))
	}
	if p := previousMonth(time.Date(2028, 1, 9, 0, 0, 0, 0, time.UTC)); p.From.Format("2006-01-02") != "2027-12-01" || p.To.Format("2006-01-02") != "2027-12-31" {
		t.Errorf("December = %v", p)
	}
}

func TestSettlementJob_NextRunAndConfig(t *testing.T) {
	at := 4 * 60
	for _, c := range []struct{ now, want string }{
		{"2027-03-05T03:59:59Z", "2027-03-05T04:00:00Z"},
		{"2027-03-05T04:00:00Z", "2027-04-05T04:00:00Z"},
		{"2027-03-20T00:00:00Z", "2027-04-05T04:00:00Z"},
		{"2027-12-20T00:00:00Z", "2028-01-05T04:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := nextMonthly(now, 5, at).Format(time.RFC3339); got != c.want {
			t.Errorf("after %s: %s, want %s", c.now, got, c.want)
		}
	}
	good := ids.New()
	if j, err := newSettlementJob(env(nil), nil, nil); j != nil || err != nil {
		t.Fatalf("unset: %v %v", j, err)
	}
	if j, err := newSettlementJob(env(map[string]string{"TAX_SETTLEMENT_TENANT_IDS": good, "TAX_SETTLEMENT_DAY": "12", "TAX_SETTLEMENT_AT": "06:30"}), nil, nil); err != nil || j.day != 12 || j.at != 390 {
		t.Fatalf("configured: %+v %v", j, err)
	}
	for name, m := range map[string]map[string]string{
		"bad tenant": {"TAX_SETTLEMENT_TENANT_IDS": "nope"},
		"day 0":      {"TAX_SETTLEMENT_TENANT_IDS": good, "TAX_SETTLEMENT_DAY": "0"},
		"day 31":     {"TAX_SETTLEMENT_TENANT_IDS": good, "TAX_SETTLEMENT_DAY": "31"},
		"bad time":   {"TAX_SETTLEMENT_TENANT_IDS": good, "TAX_SETTLEMENT_AT": "4am"},
	} {
		if _, err := newSettlementJob(env(m), nil, nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestRecommitJob_DrainsEachEntityAndValidatesItsInterval(t *testing.T) {
	a, le := ids.New(), ids.New()
	f := &fakeScheduled{entities: map[string][]string{a: {le}}}
	job, err := newRecommitJob(env(map[string]string{"TAX_RECOMMIT_TENANT_IDS": a}), f, f)
	if err != nil || job == nil || job.interval != 5*time.Minute {
		t.Fatalf("job = %+v, %v", job, err)
	}
	job.once(context.Background())
	if len(f.recommitRun) != 1 || f.recommitRun[0].LegalEntityID != le || f.recommitRun[0].UserID != recommitActor {
		t.Fatalf("runs = %+v", f.recommitRun)
	}
	if j, err := newRecommitJob(env(nil), nil, nil); j != nil || err != nil {
		t.Fatalf("unset: %v %v", j, err)
	}
	if _, err := newRecommitJob(env(map[string]string{"TAX_RECOMMIT_TENANT_IDS": a, "TAX_RECOMMIT_INTERVAL": "5s"}), f, f); err == nil {
		t.Error("a 5s interval was accepted")
	}
	if _, err := newRecommitJob(env(map[string]string{"TAX_RECOMMIT_TENANT_IDS": a, "TAX_RECOMMIT_INTERVAL": "soon"}), f, f); err == nil {
		t.Error("an unreadable interval was accepted")
	}
}
