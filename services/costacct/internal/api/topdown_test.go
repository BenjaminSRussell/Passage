package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

func (f *fakeStore) RunTopDown(_ context.Context, _, _ string, in store.TopDownRequest) (int, []byte, error) {
	f.calls++
	f.topDown = in
	return http.StatusCreated, []byte(`{"id":"run"}`), nil
}

func (f *fakeStore) ListTopDownRuns(_ context.Context, le string) ([]store.TopDownRun, error) {
	f.calls++
	f.filter = le
	return nil, nil
}

func (f *fakeStore) GetTopDownRun(context.Context, string) (store.TopDownRun, error) {
	f.calls++
	return store.TopDownRun{}, httpx.NewProblem(http.StatusNotFound, store.CodeTopDownRunNotFound, "top-down run not found")
}

func (f *fakeStore) SettleInternalOrder(_ context.Context, _, _ string, in store.SettlementRequest) (int, []byte, error) {
	f.calls++
	f.settle = in
	return 0, nil, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeOverSettlement, "Settlement 600.00 exceeds collected cost 500.00")
}

func (f *fakeStore) ListSettlements(_ context.Context, orderID, period string) ([]store.Settlement, error) {
	f.calls++
	f.filter = orderID + "/" + period
	return []store.Settlement{}, nil
}

const objC = "0196a6a4-0000-7000-8000-000000000015"

func topDownBodyWith(amount, segments string) string {
	return `{"legalEntityId":"` + entityID + `","period":"2026-09","referencePeriod":"2026-08","currency":"USD","sourceCostObjectId":"` + objA +
		`","costElementId":"` + elemID + `","amount":"` + amount + `","segments":` + segments + `}`
}

func TestRunTopDown_DutyAndForwarding(t *testing.T) {
	body := topDownBodyWith("12000.00", `[{"costObjectId":"`+objB+`","basis":"300000"},{"costObjectId":"`+objC+`","basis":"150000"}]`)
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/top-down-runs", body, DutyMaintain, "k"); rec.Code != http.StatusForbidden {
		t.Fatalf("without controlling.run: status %d, want 403", rec.Code)
	}
	rec := serve(t, st, http.MethodPost, "/v1/controlling/top-down-runs", body, DutyRun, "k")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	if st.topDown.ReferencePeriod != "2026-08" || len(st.topDown.Segments) != 2 || !st.topDown.Amount.Equal(dec("12000")) {
		t.Fatalf("store got %+v", st.topDown)
	}
}

func TestRunTopDown_Validation(t *testing.T) {
	segs := `[{"costObjectId":"` + objB + `","basis":"1"}]`
	cases := map[string]string{
		"amount finer than cents":  topDownBodyWith("10.005", segs),
		"zero amount":              topDownBodyWith("0", segs),
		"no segments":              topDownBodyWith("10", `[]`),
		"source as a segment":      topDownBodyWith("10", `[{"costObjectId":"`+objA+`","basis":"1"}]`),
		"duplicate segment":        topDownBodyWith("10", `[{"costObjectId":"`+objB+`","basis":"1"},{"costObjectId":"`+objB+`","basis":"2"}]`),
		"negative basis":           topDownBodyWith("10", `[{"costObjectId":"`+objB+`","basis":"-1"}]`),
		"unknown field in segment": topDownBodyWith("10", `[{"costObjectId":"`+objB+`","basis":"1","weight":2}]`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			rec := serve(t, st, http.MethodPost, "/v1/controlling/top-down-runs", body, DutyRun, "k")
			if rec.Code != http.StatusBadRequest || problemCode(t, rec) != codeValidation || st.calls != 0 {
				t.Fatalf("status %d code %s calls %d", rec.Code, problemCode(t, rec), st.calls)
			}
		})
	}
}

func TestTopDownReads(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/top-down-runs?legalEntityId="+entityID, "", DutyView, ""); rec.Code != http.StatusOK || st.filter != entityID {
		t.Fatalf("list: status %d filter %q", rec.Code, st.filter)
	}
	rec := serve(t, st, http.MethodGet, "/v1/controlling/top-down-runs/"+objA, "", DutyView, "")
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeTopDownRunNotFound {
		t.Fatalf("get missing: status %d", rec.Code)
	}
}

func TestSettle_RelaysOverSettlement(t *testing.T) {
	body := `{"period":"2026-09","currency":"USD","receiverCostObjectId":"` + objB + `","secondaryElementId":"` + elemID + `","amount":"600.00"}`
	st := &fakeStore{}
	path := "/v1/controlling/internal-orders/" + objA + ":settle"
	if rec := serve(t, st, http.MethodPost, path, body, DutyView, "k"); rec.Code != http.StatusForbidden {
		t.Fatalf("without controlling.run: status %d", rec.Code)
	}
	rec := serve(t, st, http.MethodPost, path, body, DutyRun, "k")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeOverSettlement {
		t.Fatalf("status %d code %s", rec.Code, problemCode(t, rec))
	}
	if st.settle.OrderID != objA || st.settle.ReceiverID != objB || !st.settle.Amount.Equal(dec("600")) {
		t.Fatalf("store got %+v", st.settle)
	}
}

func TestSettle_Validation(t *testing.T) {
	cases := map[string]string{
		"receiver is the order": `{"period":"2026-09","currency":"USD","receiverCostObjectId":"` + objA + `","secondaryElementId":"` + elemID + `","amount":"1"}`,
		"bad period":            `{"period":"2026-13","currency":"USD","receiverCostObjectId":"` + objB + `","secondaryElementId":"` + elemID + `","amount":"1"}`,
		"negative amount":       `{"period":"2026-09","currency":"USD","receiverCostObjectId":"` + objB + `","secondaryElementId":"` + elemID + `","amount":"-1"}`,
		"yen with decimals":     `{"period":"2026-09","currency":"JPY","receiverCostObjectId":"` + objB + `","secondaryElementId":"` + elemID + `","amount":"1.5"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			st := &fakeStore{}
			rec := serve(t, st, http.MethodPost, "/v1/controlling/internal-orders/"+objA+":settle", body, DutyRun, "k")
			if rec.Code != http.StatusBadRequest || st.calls != 0 {
				t.Fatalf("status %d calls %d (%s)", rec.Code, st.calls, rec.Body.String())
			}
		})
	}
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/internal-orders/"+objA+"/settlements?period=2026-09", "", DutyView, ""); rec.Code != http.StatusOK || st.filter != objA+"/2026-09" {
		t.Fatalf("list settlements: status %d filter %q", rec.Code, st.filter)
	}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
