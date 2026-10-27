package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// planLog records what the invoicing plan store methods received; err, when
// set, is what they answer.
type planLog struct {
	puts    int
	poID    string
	lineNo  int
	ifMatch int
	in      store.InvoicingPlanInput
	deleted bool
	err     error
}

func (f *fakeStore) PutInvoicingPlan(_ context.Context, poID string, lineNo, ifMatch int, in store.InvoicingPlanInput) (store.PlanResult, error) {
	f.planLog.puts++
	f.planLog.poID, f.planLog.lineNo, f.planLog.ifMatch, f.planLog.in = poID, lineNo, ifMatch, in
	if f.planLog.err != nil {
		return store.PlanResult{}, f.planLog.err
	}
	return store.PlanResult{InvoicingPlan: domain.InvoicingPlan{PurchaseOrderID: poID, LineNo: lineNo, Kind: in.Kind,
		Rows: []domain.PlanRow{}}, PORowVersion: ifMatch + 1}, nil
}

func (f *fakeStore) GetInvoicingPlan(_ context.Context, poID string, lineNo int) (store.PlanResult, error) {
	if f.planLog.err != nil {
		return store.PlanResult{}, f.planLog.err
	}
	return store.PlanResult{InvoicingPlan: domain.InvoicingPlan{PurchaseOrderID: poID, LineNo: lineNo, Kind: domain.PlanPartial,
		Rows: []domain.PlanRow{}}, PORowVersion: 7}, nil
}

func (f *fakeStore) ListInvoicingPlans(_ context.Context, poID string) ([]domain.InvoicingPlan, error) {
	f.planLog.poID = poID
	return []domain.InvoicingPlan{{PurchaseOrderID: poID, LineNo: 2, Kind: domain.PlanPeriodic, Rows: []domain.PlanRow{}}}, f.planLog.err
}

func (f *fakeStore) DeleteInvoicingPlan(_ context.Context, poID string, lineNo, ifMatch int) (int, error) {
	f.planLog.deleted, f.planLog.poID, f.planLog.lineNo, f.planLog.ifMatch = true, poID, lineNo, ifMatch
	return ifMatch + 1, f.planLog.err
}

const (
	planPO   = "00000000-0000-7000-8000-00000000c001"
	planPath = "/v1/procurement/purchase-orders/" + planPO + "/lines/1/invoicing-plan"
)

var (
	maintainDuty = []string{dutyPurchaseOrderMaintain}
	ifMatch4     = map[string]string{"If-Match": `"4"`}
)

func TestPutInvoicingPlan_PeriodicReachesTheStore(t *testing.T) {
	h := newHarness(t)
	body := `{"kind":"PERIODIC","start":"2027-01-01","frequency":"MONTHLY","periods":12,"billingDay":1,"amountPerPeriod":"1000.00"}`
	rec := h.do(call{method: "PUT", path: planPath, duties: maintainDuty, body: body, headers: ifMatch4})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("ETag") != `"5"` {
		t.Errorf("ETag %q, want the new PO row version \"5\"", rec.Header().Get("ETag"))
	}
	got := h.fake.planLog
	if got.poID != planPO || got.lineNo != 1 || got.ifMatch != 4 {
		t.Errorf("store got po %s line %d If-Match %d", got.poID, got.lineNo, got.ifMatch)
	}
	spec := got.in.Periodic
	if got.in.Kind != domain.PlanPeriodic || spec.Start.String() != "2027-01-01" || spec.Frequency != domain.FreqMonthly ||
		spec.Periods != 12 || spec.BillingDay != 1 || !spec.AmountPerPeriod.Equal(decimal.NewFromInt(1000)) {
		t.Errorf("periodic spec = %+v", spec)
	}
	if !got.in.SettleWithoutReceipt {
		t.Error("settleWithoutReceipt must default to true (FR-17.21)")
	}
}

func TestPutInvoicingPlan_PartialMilestones(t *testing.T) {
	h := newHarness(t)
	body := `{"kind":"PARTIAL","settleWithoutReceipt":false,"milestones":[{"date":"2027-02-01","percent":"30"},{"date":"2027-03-01","percent":"70"}]}`
	if rec := h.do(call{method: "PUT", path: planPath, duties: maintainDuty, body: body, headers: ifMatch4}); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	in := h.fake.planLog.in
	if in.SettleWithoutReceipt || len(in.Milestones) != 2 || in.Milestones[1].Percent == nil || !in.Milestones[1].Percent.Equal(decimal.NewFromInt(70)) ||
		in.Milestones[0].Amount != nil || in.Milestones[0].Date.String() != "2027-02-01" {
		t.Errorf("partial input = %+v", in)
	}
}

func TestPutInvoicingPlan_Rejections(t *testing.T) {
	periodic := func(extra string) string {
		return `{"kind":"PERIODIC","start":"2027-01-01","frequency":"MONTHLY","periods":12,"amountPerPeriod":"1000"` + extra + `}`
	}
	tests := []struct {
		name, path, body, field string
		headers                 map[string]string
		duties                  []string
		status                  int
	}{
		{"unknown kind", planPath, `{"kind":"WEEKLY"}`, "kind", ifMatch4, maintainDuty, 422},
		{"periodic without start", planPath, `{"kind":"PERIODIC","frequency":"MONTHLY","periods":1,"amountPerPeriod":"1"}`, "start", ifMatch4, maintainDuty, 422},
		{"start not a date", planPath, `{"kind":"PERIODIC","start":"2027-13-01","frequency":"MONTHLY","periods":1,"amountPerPeriod":"1"}`, "start", ifMatch4, maintainDuty, 422},
		{"weekly frequency", planPath, `{"kind":"PERIODIC","start":"2027-01-01","frequency":"WEEKLY","periods":1,"amountPerPeriod":"1"}`, "frequency", ifMatch4, maintainDuty, 422},
		{"zero periods", planPath, `{"kind":"PERIODIC","start":"2027-01-01","frequency":"MONTHLY","periods":0,"amountPerPeriod":"1"}`, "periods", ifMatch4, maintainDuty, 422},
		{"billing day 32", planPath, periodic(`,"billingDay":32`), "billingDay", ifMatch4, maintainDuty, 422},
		{"amount not a decimal", planPath, `{"kind":"PERIODIC","start":"2027-01-01","frequency":"MONTHLY","periods":1,"amountPerPeriod":"ten"}`, "amountPerPeriod", ifMatch4, maintainDuty, 422},
		{"partial without milestones", planPath, `{"kind":"PARTIAL"}`, "milestones", ifMatch4, maintainDuty, 422},
		{"milestone with percent and amount", planPath, `{"kind":"PARTIAL","milestones":[{"date":"2027-02-01","percent":"100","amount":"5"}]}`, "milestones[0].percent", ifMatch4, maintainDuty, 422},
		{"milestone without a date", planPath, `{"kind":"PARTIAL","milestones":[{"percent":"100"}]}`, "milestones[0].date", ifMatch4, maintainDuty, 422},
		{"line number not a number", "/v1/procurement/purchase-orders/" + planPO + "/lines/one/invoicing-plan", periodic(""), "lineNo", ifMatch4, maintainDuty, 422},
		{"PO id not a UUID", "/v1/procurement/purchase-orders/po-1/lines/1/invoicing-plan", periodic(""), "id", ifMatch4, maintainDuty, 422},
		{"no If-Match", planPath, periodic(""), "", nil, maintainDuty, http.StatusPreconditionRequired},
		{"inquire duty only", planPath, periodic(""), "", ifMatch4, []string{dutyPurchaseOrderInquire}, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(call{method: "PUT", path: tc.path, duties: tc.duties, body: tc.body, headers: tc.headers})
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.field != "" {
				var p httpx.Problem
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if _, ok := p.Errors[tc.field]; !ok {
					t.Errorf("errors %v do not name %s", p.Errors, tc.field)
				}
			}
			if h.fake.planLog.puts != 0 {
				t.Error("a rejected plan reached the store")
			}
		})
	}
}

func TestPutInvoicingPlan_DomainRuleIs422WithItsCode(t *testing.T) {
	h := newHarness(t)
	h.fake.planLog.err = &domain.Error{Code: domain.CodeInvoicingPlan, Detail: "invoicing plan total 12000.00 ≠ line amount 12500.00"}
	body := `{"kind":"PERIODIC","start":"2027-01-01","frequency":"MONTHLY","periods":12,"amountPerPeriod":"1000"}`
	rec := h.do(call{method: "PUT", path: planPath, duties: maintainDuty, body: body, headers: ifMatch4})
	if rec.Code != 422 || problemCode(t, rec) != domain.CodeInvoicingPlan {
		t.Fatalf("status %d code %s, want 422 %s", rec.Code, problemCode(t, rec), domain.CodeInvoicingPlan)
	}
}

func TestInvoicingPlan_ReadAndDelete(t *testing.T) {
	h := newHarness(t)
	inquire := []string{dutyPurchaseOrderInquire}
	rec := h.do(call{method: "GET", path: planPath, duties: inquire})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"7"` {
		t.Fatalf("get: %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["poRowVersion"] != float64(7) || got["lineNo"] != float64(1) {
		t.Errorf("body = %v", got)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/purchase-orders/" + planPO + "/invoicing-plans", duties: inquire})
	if rec.Code != 200 || h.fake.planLog.poID != planPO {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var page struct{ Items []domain.InvoicingPlan }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].LineNo != 2 {
		t.Fatalf("list body = %s", rec.Body)
	}
	if rec := h.do(call{method: "DELETE", path: planPath, duties: inquire, headers: ifMatch4}); rec.Code != http.StatusForbidden || h.fake.planLog.deleted {
		t.Fatalf("delete with the inquire duty: %d", rec.Code)
	}
	rec = h.do(call{method: "DELETE", path: planPath, duties: maintainDuty, headers: ifMatch4})
	if rec.Code != http.StatusNoContent || rec.Header().Get("ETag") != `"5"` || h.fake.planLog.lineNo != 1 || h.fake.planLog.ifMatch != 4 {
		t.Fatalf("delete: %d ETag %q, store got line %d If-Match %d", rec.Code, rec.Header().Get("ETag"), h.fake.planLog.lineNo, h.fake.planLog.ifMatch)
	}
	h.fake.planLog.err = httpx.NewProblem(http.StatusNotFound, store.CodeInvoicingPlanNotFound, "line 1 has no invoicing plan")
	if rec := h.do(call{method: "GET", path: planPath, duties: inquire}); rec.Code != 404 || problemCode(t, rec) != store.CodeInvoicingPlanNotFound {
		t.Fatalf("missing plan: %d %s", rec.Code, rec.Body)
	}
}
