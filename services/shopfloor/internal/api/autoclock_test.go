package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// autoClockFake records the sweep and review calls on top of the base fake.
type autoClockFake struct {
	*fakeStore
	now        time.Time
	afterHours int
	limit      int
	worker     string
	closed     []store.AutoClosed
	review     []store.AutoClockOutReview
}

func (f *autoClockFake) AutoClockOut(_ context.Context, now time.Time, afterHours, limit int) ([]store.AutoClosed, error) {
	f.now, f.afterHours, f.limit = now, afterHours, limit
	return f.closed, nil
}

func (f *autoClockFake) ListAutoClockOuts(_ context.Context, worker string, _, _ time.Time, limit int) ([]store.AutoClockOutReview, error) {
	f.worker, f.limit = worker, limit
	return f.review, nil
}

func (f *fakeStore) AutoClockOut(context.Context, time.Time, int, int) ([]store.AutoClosed, error) {
	return nil, nil
}

func (f *fakeStore) ListAutoClockOuts(context.Context, string, time.Time, time.Time, int) ([]store.AutoClockOutReview, error) {
	return nil, nil
}

func TestAutoClockOut_DutyDefaultsAndValidation(t *testing.T) {
	now := time.Date(2026, 10, 21, 1, 0, 0, 0, time.UTC)
	f := &autoClockFake{fakeStore: &fakeStore{}}
	h := newRouterAt(f, func() time.Time { return now })
	path := "/v1/shopfloor/registrations:auto-clock-out"
	if rec := call(t, h, "POST", path, "", "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	rec := call(t, h, "POST", path, "", DutyAutoClockOut)
	if rec.Code != http.StatusOK {
		t.Fatalf("sweep: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		AfterHours int               `json:"afterHours"`
		Items      []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.AfterHours != 16 || out.Items == nil || len(out.Items) != 0 {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	if f.afterHours != 16 || f.limit != 100 || !f.now.Equal(now) {
		t.Fatalf("store called with %v %d %d", f.now, f.afterHours, f.limit)
	}
	if rec := call(t, h, "POST", path+"?afterHours=12&limit=5", "", DutyAutoClockOut); rec.Code != http.StatusOK || f.afterHours != 12 || f.limit != 5 {
		t.Fatalf("custom: %d %d %d", rec.Code, f.afterHours, f.limit)
	}
	if rec := call(t, h, "POST", path+"?afterHours=2", "", DutyAutoClockOut); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "SF-0130") {
		t.Fatalf("too small: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, "POST", path+"?afterHours=soon", "", DutyAutoClockOut); rec.Code != http.StatusBadRequest {
		t.Fatalf("not a number: %d", rec.Code)
	}
	if rec := call(t, h, "POST", path+"?limit=0", "", DutyAutoClockOut); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit: %d", rec.Code)
	}
}

func TestListAutoClockOuts_DutyRangeAndBody(t *testing.T) {
	at := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	in := at.Add(-16 * time.Hour)
	f := &autoClockFake{fakeStore: &fakeStore{}, review: []store.AutoClockOutReview{{RegistrationID: regID, WorkerID: workerID, WorkerName: "Ana", ClockedInAt: &in, ClockedOutAt: at, ClosedAt: at}}}
	h := newRouter(f)
	path := "/v1/shopfloor/auto-clock-outs?from=2026-10-20&to=2026-10-22&workerId=" + workerID
	if rec := call(t, h, "GET", path, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	rec := call(t, h, "GET", path, "", "shopfloor.view")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"workerName":"Ana"`) || f.worker != workerID {
		t.Fatalf("list: %d %s (%s)", rec.Code, rec.Body, f.worker)
	}
	if rec := call(t, h, "GET", "/v1/shopfloor/auto-clock-outs?from=bad&to=2026-10-22", "", "shopfloor.view"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad range: %d", rec.Code)
	}
}
