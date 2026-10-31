package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type scheduleStore struct {
	fakeStore
	promised *store.PromiseRequest
	version  int
	filter   *store.BackorderFilter
	err      error
}

func (s *scheduleStore) PromiseLine(_ context.Context, id string, version int, req store.PromiseRequest, _ string, _ time.Time) (store.PromiseResult, error) {
	s.promised, s.version = &req, version
	return store.PromiseResult{Order: store.Order{ID: id, Version: version + 1}, Schedule: []store.ScheduleLine{{LineNo: req.LineNo, Quantity: decimal.NewFromInt(40), Status: "CONFIRMED"}}}, s.err
}

func (s *scheduleStore) ListScheduleLines(context.Context, string) ([]store.ScheduleLine, error) {
	return []store.ScheduleLine{}, s.err
}

func (s *scheduleStore) ListBackorders(_ context.Context, f store.BackorderFilter) ([]store.BackorderItem, error) {
	s.filter = &f
	return []store.BackorderItem{}, s.err
}

func TestPromiseLineHandler(t *testing.T) {
	s := &scheduleStore{}
	order := ids.New()
	path := orders + "/" + order + "/lines/2:promise"
	body := `{"completeOnly":true,"periods":[{"date":"2027-03-01","supply":"40","demand":"0"},{"date":"2027-03-04","supply":"5","demand":"0"}]}`
	match := map[string]string{"If-Match": `"3"`}
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRead), match); rec.Code != http.StatusForbidden {
		t.Fatalf("needs sales.order.write: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyWrite), nil); rec.Code != http.StatusPreconditionRequired || s.promised != nil {
		t.Fatalf("If-Match is required: %d", rec.Code)
	}
	for name, bad := range map[string]string{
		"no periods":      `{"periods":[]}`,
		"bad date":        `{"periods":[{"date":"March","supply":"1","demand":"0"}]}`,
		"negative supply": `{"periods":[{"date":"2027-03-01","supply":"-1","demand":"0"}]}`,
		"unordered":       `{"periods":[{"date":"2027-03-04","supply":"1","demand":"0"},{"date":"2027-03-01","supply":"1","demand":"0"}]}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, path, bad, principal(dutyWrite), match); rec.Code != http.StatusBadRequest || s.promised != nil {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := serveBody(t, s, http.MethodPost, orders+"/"+order+"/lines/x:promise", body, principal(dutyWrite), match); rec.Code != http.StatusNotFound {
		t.Fatalf("line number must be numeric: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyWrite), match)
	if rec.Code != http.StatusOK || s.promised == nil || s.promised.LineNo != 2 || !s.promised.CompleteOnly || len(s.promised.Periods) != 2 || s.version != 3 {
		t.Fatalf("promise: %d %s %+v", rec.Code, rec.Body, s.promised)
	}
	if rec.Header().Get("ETag") != `"4"` || !strings.Contains(rec.Body.String(), `"scheduleLines"`) || !strings.Contains(rec.Body.String(), `"order"`) {
		t.Fatalf("response %s %v", rec.Body, rec.Header())
	}
}

func TestBackorderListHandler(t *testing.T) {
	s := &scheduleStore{}
	if rec := serve(t, s, http.MethodGet, backorders+"?legalEntityId="+testLE, principal(), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("needs sales.order.read: %d", rec.Code)
	}
	product := ids.New()
	rec := serve(t, s, http.MethodGet, backorders+"?legalEntityId="+testLE+"&productId="+product+"&promisedTo=2027-03-31&limit=10", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.filter == nil || s.filter.ProductID != product || s.filter.PromisedTo != "2027-03-31" || s.filter.Limit != 10 || s.filter.LegalEntityID != testLE {
		t.Fatalf("list: %d %s %+v", rec.Code, rec.Body, s.filter)
	}
	for _, q := range []string{"productId=P1", "customerId=C1", "promisedTo=soon", "limit=0", "limit=500"} {
		if rec := serve(t, s, http.MethodGet, backorders+"?legalEntityId="+testLE+"&"+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	rec = serve(t, s, http.MethodGet, orders+"/"+ids.New()+"/schedule-lines", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("schedule lines: %d %s", rec.Code, rec.Body)
	}
}
