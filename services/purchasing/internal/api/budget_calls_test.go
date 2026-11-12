package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/budget"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type fakeBudget struct {
	check     budget.Check
	checkErr  error
	checked   []store.Requisition
	releases  []string
	releaseOK bool
	converts  int
}

func (f *fakeBudget) CheckRequisition(_ context.Context, r store.Requisition) (budget.Check, error) {
	f.checked = append(f.checked, r)
	return f.check, f.checkErr
}

func (f *fakeBudget) ReleaseLine(_ context.Context, _, _, lineID string) error {
	f.releases = append(f.releases, lineID)
	if !f.releaseOK {
		return errors.New("budgeting is down")
	}
	return nil
}

func (f *fakeBudget) Convert(context.Context, string, string, string, string) error {
	f.converts++
	return nil
}

func budgetHarness(t *testing.T, b *fakeBudget) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	h.api.WithBudget(b, h.fake)
	id := ids.New()
	h.fake.requisitions[id] = store.Requisition{ID: id, Number: "PR-1", LegalEntityID: leA, RequesterID: userA, Status: store.RequisitionInReview, Version: 4, Currency: "USD",
		Total: decimal.NewFromInt(900), ApproverChain: []string{domain.RoleManager}, Lines: []store.RequisitionLine{
			{ID: ids.New(), LineNo: 1, Status: "OPEN", Amount: decimal.NewFromInt(900), GLAccount: "6100", CostCenter: "CC-20"}}}
	signingLimit(h, userApprover, domain.DocRequisition, "1000", "10000", "2026-12-31")
	return h, id
}

func TestApproveRequisition_TheLastLevelRunsTheBudgetCheckFirst(t *testing.T) {
	b := &fakeBudget{check: budget.Check{Result: "PASS"}}
	h, id := budgetHarness(t, b)
	if code := decide(h, id, "approve", userApprover, []string{dutyRequisitionApprove}, ""); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	if len(b.checked) != 1 || b.checked[0].ID != id || h.fake.approval.calls != 1 {
		t.Fatalf("checked %d times, store acted %d times; want one check then the approval", len(b.checked), h.fake.approval.calls)
	}
	// A first level of two is not the last: nothing is checked yet.
	b2 := &fakeBudget{check: budget.Check{Result: "PASS"}}
	h2, id2 := budgetHarness(t, b2)
	r := h2.fake.requisitions[id2]
	r.ApproverChain = []string{domain.RoleManager, domain.RoleCFO}
	h2.fake.requisitions[id2] = r
	if code := decide(h2, id2, "approve", userApprover, []string{dutyRequisitionApprove}, ""); code != http.StatusOK || len(b2.checked) != 0 {
		t.Fatalf("first of two levels: %d with %d checks; want 200 and none", code, len(b2.checked))
	}
}

func TestApproveRequisition_OverBudgetIsRefusedAndNothingIsApproved(t *testing.T) {
	b := &fakeBudget{}
	h, id := budgetHarness(t, b)
	b.check = budget.Check{Result: "FAIL", Lines: []budget.LineCheck{{LineID: h.fake.requisitions[id].Lines[0].ID, Result: "FAIL", OverPct: decimal.RequireFromString("12.5")}}}
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: []string{dutyRequisitionApprove},
		headers: map[string]string{"Idempotency-Key": "k-b", "If-Match": `"4"`}})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeRequisitionOverBudget || !strings.Contains(rec.Body.String(), "12.50%") {
		t.Fatalf("over budget: %d %s; want 422 %s naming the line", rec.Code, rec.Body, CodeRequisitionOverBudget)
	}
	if h.fake.approval.calls != 0 {
		t.Fatal("an over-budget requisition reached the store")
	}
	b.check, b.checkErr = budget.Check{}, errors.New("connection refused")
	rec = h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: []string{dutyRequisitionApprove},
		headers: map[string]string{"Idempotency-Key": "k-b2", "If-Match": `"4"`}})
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != CodeBudgetUnavailable || h.fake.approval.calls != 0 {
		t.Fatalf("budget down: %d %s; want 503 %s and no approval", rec.Code, rec.Body, CodeBudgetUnavailable)
	}
	b.checkErr, b.check = nil, budget.Check{Result: "NOT_CONTROLLED"}
	if code := decide(h, id, "approve", userApprover, []string{dutyRequisitionApprove}, ""); code != http.StatusOK {
		t.Fatalf("a requisition no rule covers must be approved: %d", code)
	}
}

func TestCancelApprovedRequisition_ReleasesAndAFailedReleaseStaysQueued(t *testing.T) {
	b := &fakeBudget{}
	h, id := budgetHarness(t, b)
	r := h.fake.requisitions[id]
	r.Status = store.RequisitionApproved
	h.fake.requisitions[id] = r
	lineID := r.Lines[0].ID
	h.fake.budgetCalls = []store.BudgetCall{{ID: ids.New(), LegalEntityID: leA, Kind: store.BudgetCallRelease, RequisitionID: id, RequisitionLineID: lineID, Status: store.BudgetCallPending}}
	if code := decide(h, id, "cancel", userA, []string{dutyRequisitionCreate}, ""); code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	if len(b.releases) != 1 || b.releases[0] != lineID || h.fake.budgetCalls[0].Status != store.BudgetCallPending || h.fake.budgetCalls[0].Attempts != 1 {
		t.Fatalf("releases %v, call %+v; want one failed attempt that stays pending", b.releases, h.fake.budgetCalls[0])
	}
	rec := h.do(call{method: "GET", path: "/v1/procurement/budget-calls?status=pending", user: userA, duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), lineID) || !strings.Contains(rec.Body.String(), "budgeting is down") {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	b.releaseOK = true
	rec = h.do(call{method: "POST", path: "/v1/procurement/budget-calls:retry", user: userA, duties: []string{dutyPurchaseOrderMaintain}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"done":1`) || h.fake.budgetCalls[0].Status != store.BudgetCallDone {
		t.Fatalf("retry: %d %s (%+v)", rec.Code, rec.Body, h.fake.budgetCalls[0])
	}
	rec = h.do(call{method: "POST", path: "/v1/procurement/budget-calls:retry", user: userA, duties: []string{dutyPurchaseOrderInquire}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("retry with the inquiry duty: %d, want 403", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: "/v1/procurement/budget-calls?status=odd", user: userA, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status: %d, want 422", rec.Code)
	}
}

func TestApproveRequisition_ARepeatApproverIsRefusedBeforeTheBudgetCheckReserves(t *testing.T) {
	b := &fakeBudget{check: budget.Check{Result: "PASS"}}
	h, id := budgetHarness(t, b)
	r := h.fake.requisitions[id]
	r.ApproverChain, r.ApprovedSteps = []string{domain.RoleManager, domain.RoleManager}, 1
	// An approval from an earlier round does not count; the one after the latest submission does.
	r.History = []store.RequisitionHistory{
		{Action: store.HistorySubmitted, RequisitionVersion: 1},
		{Action: store.HistoryApproved, ActorID: userApprover, RequisitionVersion: 2},
		{Action: store.HistorySubmitted, RequisitionVersion: 3},
	}
	h.fake.requisitions[id] = r
	if code := decide(h, id, "approve", userApprover, []string{dutyRequisitionApprove}, ""); code != http.StatusOK || len(b.checked) != 1 {
		t.Fatalf("approval from an earlier round: %d with %d checks; want 200 and one check", code, len(b.checked))
	}
	b2 := &fakeBudget{check: budget.Check{Result: "PASS"}}
	h2, id2 := budgetHarness(t, b2)
	r2 := h2.fake.requisitions[id2]
	r2.ApproverChain, r2.ApprovedSteps = []string{domain.RoleManager, domain.RoleManager}, 1
	r2.History = []store.RequisitionHistory{
		{Action: store.HistorySubmitted, RequisitionVersion: 3},
		{Action: store.HistoryApproved, ActorID: userApprover, RequisitionVersion: 4},
	}
	h2.fake.requisitions[id2] = r2
	rec := h2.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id2 + ":approve", user: userApprover, duties: []string{dutyRequisitionApprove},
		headers: map[string]string{"Idempotency-Key": "k-r", "If-Match": `"4"`}})
	if rec.Code != http.StatusForbidden || problemCode(t, rec) != store.CodeRequisitionRepeatApprover {
		t.Fatalf("repeat approver: %d %s; want 403 %s", rec.Code, rec.Body, store.CodeRequisitionRepeatApprover)
	}
	if len(b2.checked) != 0 || h2.fake.approval.calls != 0 {
		t.Fatalf("a refused approval ran %d budget checks (reserving funds) and %d store calls; want none", len(b2.checked), h2.fake.approval.calls)
	}
}

// The check reserved the funds. An approval the store then refuses must NOT release them: budgeting refuses a new reservation
// for a line whose pre-encumbrance was released (BUD-0011), so a release here would make the requisition unapprovable, or
// approved with nothing encumbered after a replayed PASS. The re-approval checks again and keeps the reservation.
func TestApproveRequisition_AFailedApprovalKeepsTheReservationAndAReApprovalSucceeds(t *testing.T) {
	b := &fakeBudget{check: budget.Check{Result: "PASS"}, releaseOK: true}
	h, id := budgetHarness(t, b)
	h.fake.approval.err = httpx.NewProblem(http.StatusConflict, "PRC-REQ-VERSION", "changed meanwhile")
	if code := decide(h, id, "approve", userApprover, []string{dutyRequisitionApprove}, ""); code != http.StatusConflict {
		t.Fatalf("approve: %d, want the store's 409", code)
	}
	if len(b.checked) != 1 || len(b.releases) != 0 || len(h.fake.budgetCalls) != 0 {
		t.Fatalf("checked %d, released %v, queued %+v; want the reservation left alone", len(b.checked), b.releases, h.fake.budgetCalls)
	}
	h.fake.approval.err = nil
	if code := decide(h, id, "approve", userApprover, []string{dutyRequisitionApprove}, ""); code != http.StatusOK {
		t.Fatalf("re-approval: %d", code)
	}
	if len(b.checked) != 2 || len(b.releases) != 0 {
		t.Fatalf("checked %d times, released %v; want a second check and still no release", len(b.checked), b.releases)
	}
}

func TestApproveRequisition_ACurrencyMismatchIsItsOwnRefusalNotAnOutage(t *testing.T) {
	b := &fakeBudget{checkErr: &budget.Refusal{Code: budget.CurrencyMismatchCode, Detail: "the budget control rule CTL-1 is in EUR but the requisition is in USD"}}
	h, id := budgetHarness(t, b)
	rec := h.do(call{method: "POST", path: "/v1/procurement/requisitions/" + id + ":approve", user: userApprover, duties: []string{dutyRequisitionApprove},
		headers: map[string]string{"Idempotency-Key": "k-c", "If-Match": `"4"`}})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != CodeBudgetCurrency || !strings.Contains(rec.Body.String(), "EUR") || h.fake.approval.calls != 0 {
		t.Fatalf("currency mismatch: %d %s; want 422 %s naming both currencies and no approval", rec.Code, rec.Body, CodeBudgetCurrency)
	}
}
