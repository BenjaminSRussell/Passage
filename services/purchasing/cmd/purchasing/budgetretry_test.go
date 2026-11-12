package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/budget"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const (
	retryTenantA = "00000000-0000-0000-0000-0000000000a1"
	retryTenantB = "00000000-0000-0000-0000-0000000000a2"
	retryLE1     = "00000000-0000-0000-0000-0000000000c1"
	retryLE2     = "00000000-0000-0000-0000-0000000000c2"
)

type memQueue struct {
	calls map[string][]*store.BudgetCall // by tenant
}

func (m *memQueue) tenantOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

func (m *memQueue) BudgetCallsFor(context.Context, string, string) ([]store.BudgetCall, error) {
	return nil, nil
}

func (m *memQueue) ListBudgetCalls(ctx context.Context, status string, limit int) ([]store.BudgetCall, error) {
	var out []store.BudgetCall
	for _, c := range m.calls[m.tenantOf(ctx)] {
		if c.Status == status && len(out) < limit {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (m *memQueue) RecordBudgetCall(ctx context.Context, id string, callErr error) error {
	for _, c := range m.calls[m.tenantOf(ctx)] {
		if c.ID == id {
			c.Attempts++
			if callErr == nil {
				c.Status = store.BudgetCallDone
			}
		}
	}
	return nil
}

type recordingPort struct {
	budget.Port
	seen []string // "<legal entity in the principal>|<call>"
	fail map[string]bool
}

func (r *recordingPort) call(ctx context.Context, kind, le, line string) error {
	p, _ := tenant.FromContext(ctx)
	r.seen = append(r.seen, p.LegalEntityID+"|"+kind+"|"+line)
	if r.fail[line] {
		return errors.New("budgeting is down")
	}
	return nil
}

func (r *recordingPort) Convert(ctx context.Context, le, line, _, _ string) error {
	return r.call(ctx, "convert", le, line)
}

func (r *recordingPort) ReleaseLine(ctx context.Context, le, _, line string) error {
	return r.call(ctx, "release", le, line)
}

func TestRetryTenants_MakesEveryPendingCallAsItsOwnLegalEntity(t *testing.T) {
	q := &memQueue{calls: map[string][]*store.BudgetCall{
		retryTenantA: {
			{ID: "1", LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: "l1", Status: store.BudgetCallPending},
			{ID: "2", LegalEntityID: retryLE2, Kind: store.BudgetCallRelease, RequisitionLineID: "l2", Status: store.BudgetCallPending},
			{ID: "3", LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: "l3", Status: store.BudgetCallDone},
		},
		retryTenantB: {{ID: "4", LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: "l4", Status: store.BudgetCallPending}},
	}}
	port := &recordingPort{}
	if err := retryTenants(context.Background(), q, port, []string{retryTenantA, retryTenantB}); err != nil {
		t.Fatal(err)
	}
	want := []string{retryLE1 + "|convert|l1", retryLE2 + "|release|l2", retryLE1 + "|convert|l4"}
	if strings.Join(port.seen, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", port.seen, want)
	}
	for tn, calls := range q.calls {
		for _, c := range calls {
			if c.Status != store.BudgetCallDone {
				t.Errorf("tenant %s call %s is %s", tn, c.ID, c.Status)
			}
		}
	}
}

func TestRetryTenants_AFailingCallFailsTheRunButNotTheOthers(t *testing.T) {
	q := &memQueue{calls: map[string][]*store.BudgetCall{retryTenantA: {
		{ID: "1", LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: "bad", Status: store.BudgetCallPending},
		{ID: "2", LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: "good", Status: store.BudgetCallPending},
	}}}
	port := &recordingPort{fail: map[string]bool{"bad": true}}
	err := retryTenants(context.Background(), q, port, []string{retryTenantA})
	if err == nil || !strings.Contains(err.Error(), "1 tenants still have") {
		t.Fatalf("err = %v", err)
	}
	if q.calls[retryTenantA][1].Status != store.BudgetCallDone || q.calls[retryTenantA][0].Status != store.BudgetCallPending || q.calls[retryTenantA][0].Attempts != 1 {
		t.Fatalf("calls = %+v %+v", q.calls[retryTenantA][0], q.calls[retryTenantA][1])
	}
}

func TestBudgetCallsRetry_RejectsBadInput(t *testing.T) {
	for name, args := range map[string][]string{"none": nil, "bad id": {"x"}} {
		if err := budgetCallsRetry(context.Background(), func(string) string { return "" }, args); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	env := map[string]string{"DATABASE_URL": "postgres://x"}
	if err := budgetCallsRetry(context.Background(), func(k string) string { return env[k] }, []string{retryTenantA}); err == nil || !strings.Contains(err.Error(), "BUDGETING_URL") {
		t.Fatalf("no budgeting url: %v", err)
	}
}

func TestRetryTenants_CallsThatKeepFailingDoNotStarveNewerOnes(t *testing.T) {
	var calls []*store.BudgetCall
	port := &recordingPort{fail: map[string]bool{}}
	for i := 0; i < 150; i++ {
		line := fmt.Sprintf("bad%d", i)
		port.fail[line] = true
		calls = append(calls, &store.BudgetCall{ID: line, LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: line, Status: store.BudgetCallPending})
	}
	calls = append(calls, &store.BudgetCall{ID: "new", LegalEntityID: retryLE1, Kind: store.BudgetCallConvert, RequisitionLineID: "new", Status: store.BudgetCallPending})
	q := &memQueue{calls: map[string][]*store.BudgetCall{retryTenantA: calls}}
	if err := retryTenants(context.Background(), q, port, []string{retryTenantA}); err == nil {
		t.Fatal("150 calls budgeting refused must fail the run")
	}
	if last := calls[len(calls)-1]; last.Status != store.BudgetCallDone {
		t.Fatalf("the newest call is %s: failing calls starved it", last.Status)
	}
}
