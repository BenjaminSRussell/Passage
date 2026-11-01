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

// schedLog records what the delivery schedule store methods received; err, when set, is what
// they answer.
type schedLog struct {
	puts, confirms, deletes int
	poID                    string
	lineNo, seq, ifMatch    int
	in                      []domain.ScheduleInput
	date                    domain.Day
	err                     error
}

func (f *fakeStore) PutDeliverySchedule(_ context.Context, poID string, lineNo, ifMatch int, in []domain.ScheduleInput) (store.ScheduleResult, error) {
	f.schedLog.puts++
	f.schedLog.poID, f.schedLog.lineNo, f.schedLog.ifMatch, f.schedLog.in = poID, lineNo, ifMatch, in
	if f.schedLog.err != nil {
		return store.ScheduleResult{}, f.schedLog.err
	}
	return store.ScheduleResult{DeliverySchedule: domain.DeliverySchedule{PurchaseOrderID: poID, LineNo: lineNo, Rows: []domain.ScheduleRow{}}, PORowVersion: ifMatch + 1}, nil
}

func (f *fakeStore) GetDeliverySchedule(_ context.Context, poID string, lineNo int) (store.ScheduleResult, error) {
	if f.schedLog.err != nil {
		return store.ScheduleResult{}, f.schedLog.err
	}
	return store.ScheduleResult{DeliverySchedule: domain.DeliverySchedule{PurchaseOrderID: poID, LineNo: lineNo, Rows: []domain.ScheduleRow{}}, PORowVersion: 7}, nil
}

func (f *fakeStore) ListDeliverySchedules(_ context.Context, poID string) ([]domain.DeliverySchedule, error) {
	f.schedLog.poID = poID
	return []domain.DeliverySchedule{{PurchaseOrderID: poID, LineNo: 2, Rows: []domain.ScheduleRow{}}}, f.schedLog.err
}

func (f *fakeStore) DeleteDeliverySchedule(_ context.Context, poID string, lineNo, ifMatch int) (int, error) {
	f.schedLog.deletes++
	f.schedLog.poID, f.schedLog.lineNo, f.schedLog.ifMatch = poID, lineNo, ifMatch
	return ifMatch + 1, f.schedLog.err
}

func (f *fakeStore) ConfirmDeliveryDate(_ context.Context, poID string, lineNo, seq, ifMatch int, date domain.Day) (store.ScheduleResult, error) {
	f.schedLog.confirms++
	f.schedLog.poID, f.schedLog.lineNo, f.schedLog.seq, f.schedLog.ifMatch, f.schedLog.date = poID, lineNo, seq, ifMatch, date
	if f.schedLog.err != nil {
		return store.ScheduleResult{}, f.schedLog.err
	}
	return store.ScheduleResult{DeliverySchedule: domain.DeliverySchedule{PurchaseOrderID: poID, LineNo: lineNo, Rows: []domain.ScheduleRow{}}, PORowVersion: ifMatch + 1}, nil
}

const schedPath = "/v1/procurement/purchase-orders/" + planPO + "/lines/1/delivery-schedule"

func TestPutDeliverySchedule_ReachesTheStore(t *testing.T) {
	h := newHarness(t)
	body := `{"rows":[{"qty":"40","deliveryDate":"2027-03-01"},{"qty":"60.5","deliveryDate":"2027-02-01"}]}`
	rec := h.do(call{method: "PUT", path: schedPath, duties: maintainDuty, body: body, headers: ifMatch4})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.schedLog
	if got.poID != planPO || got.lineNo != 1 || got.ifMatch != 4 || len(got.in) != 2 ||
		!got.in[1].Qty.Equal(decimal.RequireFromString("60.5")) || got.in[0].DeliveryDate.String() != "2027-03-01" {
		t.Errorf("store got %+v", got)
	}
}

func TestPutDeliverySchedule_Rejections(t *testing.T) {
	row := `{"qty":"10","deliveryDate":"2027-02-01"}`
	tests := []struct {
		name, path, body, field string
		headers                 map[string]string
		duties                  []string
		status                  int
	}{
		{"no rows", schedPath, `{"rows":[]}`, "rows", ifMatch4, maintainDuty, 422},
		{"qty not a decimal", schedPath, `{"rows":[{"qty":"ten","deliveryDate":"2027-02-01"}]}`, "rows[0].qty", ifMatch4, maintainDuty, 422},
		{"qty zero", schedPath, `{"rows":[{"qty":"0","deliveryDate":"2027-02-01"}]}`, "rows[0].qty", ifMatch4, maintainDuty, 422},
		{"date missing", schedPath, `{"rows":[` + row + `,{"qty":"1"}]}`, "rows[1].deliveryDate", ifMatch4, maintainDuty, 422},
		{"date invalid", schedPath, `{"rows":[{"qty":"1","deliveryDate":"2027-13-01"}]}`, "rows[0].deliveryDate", ifMatch4, maintainDuty, 422},
		{"line number not a number", "/v1/procurement/purchase-orders/" + planPO + "/lines/x/delivery-schedule", `{"rows":[` + row + `]}`, "lineNo", ifMatch4, maintainDuty, 422},
		{"no If-Match", schedPath, `{"rows":[` + row + `]}`, "", nil, maintainDuty, http.StatusPreconditionRequired},
		{"inquire duty only", schedPath, `{"rows":[` + row + `]}`, "", ifMatch4, []string{dutyPurchaseOrderInquire}, http.StatusForbidden},
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
			if h.fake.schedLog.puts != 0 {
				t.Error("a rejected schedule reached the store")
			}
		})
	}
}

func TestPutDeliverySchedule_DomainRuleIs422WithItsCode(t *testing.T) {
	h := newHarness(t)
	h.fake.schedLog.err = &domain.Error{Code: domain.CodeScheduleSum, Detail: "delivery schedule quantities add up to 9, not the line quantity 10"}
	rec := h.do(call{method: "PUT", path: schedPath, duties: maintainDuty, body: `{"rows":[{"qty":"9","deliveryDate":"2027-02-01"}]}`, headers: ifMatch4})
	if rec.Code != 422 || problemCode(t, rec) != domain.CodeScheduleSum {
		t.Fatalf("status %d code %s, want 422 %s", rec.Code, problemCode(t, rec), domain.CodeScheduleSum)
	}
}

func TestDeliverySchedule_ReadAndDelete(t *testing.T) {
	h := newHarness(t)
	inquire := []string{dutyPurchaseOrderInquire}
	rec := h.do(call{method: "GET", path: schedPath, duties: inquire})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"7"` {
		t.Fatalf("get: %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	rec = h.do(call{method: "GET", path: "/v1/procurement/purchase-orders/" + planPO + "/delivery-schedules", duties: inquire})
	var page struct{ Items []domain.DeliverySchedule }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || len(page.Items) != 1 || page.Items[0].LineNo != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "DELETE", path: schedPath, duties: inquire, headers: ifMatch4}); rec.Code != http.StatusForbidden || h.fake.schedLog.deletes != 0 {
		t.Fatalf("delete with the inquire duty: %d", rec.Code)
	}
	rec = h.do(call{method: "DELETE", path: schedPath, duties: maintainDuty, headers: ifMatch4})
	if rec.Code != http.StatusNoContent || rec.Header().Get("ETag") != `"5"` || h.fake.schedLog.ifMatch != 4 {
		t.Fatalf("delete: %d ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	h.fake.schedLog.err = httpx.NewProblem(http.StatusNotFound, store.CodeDeliveryScheduleNotFound, "line 1 has no delivery schedule")
	if rec := h.do(call{method: "GET", path: schedPath, duties: inquire}); rec.Code != 404 || problemCode(t, rec) != store.CodeDeliveryScheduleNotFound {
		t.Fatalf("missing schedule: %d %s", rec.Code, rec.Body)
	}
}

func TestConfirmDeliveryDate(t *testing.T) {
	h := newHarness(t)
	path := schedPath + "/rows/2/confirmation"
	rec := h.do(call{method: "PUT", path: path, duties: maintainDuty, body: `{"confirmedDate":"2027-02-10"}`, headers: ifMatch4})
	if rec.Code != 200 || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.schedLog
	if got.seq != 2 || got.lineNo != 1 || got.ifMatch != 4 || got.date.String() != "2027-02-10" {
		t.Errorf("store got %+v", got)
	}
	for name, c := range map[string]call{
		"bad date":    {method: "PUT", path: path, duties: maintainDuty, body: `{"confirmedDate":"soon"}`, headers: ifMatch4},
		"no date":     {method: "PUT", path: path, duties: maintainDuty, body: `{}`, headers: ifMatch4},
		"bad seq":     {method: "PUT", path: schedPath + "/rows/0/confirmation", duties: maintainDuty, body: `{"confirmedDate":"2027-02-10"}`, headers: ifMatch4},
		"no If-Match": {method: "PUT", path: path, duties: maintainDuty, body: `{"confirmedDate":"2027-02-10"}`},
		"wrong duty":  {method: "PUT", path: path, duties: []string{dutyPurchaseOrderInquire}, body: `{"confirmedDate":"2027-02-10"}`, headers: ifMatch4},
	} {
		before := h.fake.schedLog.confirms
		if rec := h.do(c); rec.Code < 400 || h.fake.schedLog.confirms != before {
			t.Errorf("%s: status %d, store calls %d -> %d", name, rec.Code, before, h.fake.schedLog.confirms)
		}
	}
	h.fake.schedLog.err = &domain.Error{Code: domain.CodeScheduleTarget, Detail: "not CONFIRMED"}
	if rec := h.do(call{method: "PUT", path: path, duties: maintainDuty, body: `{"confirmedDate":"2027-02-10"}`, headers: ifMatch4}); rec.Code != 422 || problemCode(t, rec) != domain.CodeScheduleTarget {
		t.Fatalf("target rule: %d %s", rec.Code, rec.Body)
	}
}
