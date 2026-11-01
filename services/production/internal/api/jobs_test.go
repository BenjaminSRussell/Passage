package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

func (f *fakeStore) ListJobs(_ context.Context, jf store.JobFilter) (store.JobPage, error) {
	f.jobFilters = append(f.jobFilters, jf)
	return f.jobPage, f.getErr
}

func TestSchedule_BodyBecomesParameters(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id := ids.New()
	rec := do(h, commandCall(id, "schedule", `{"method":"jobs","direction":"backward","at":"2026-11-10T16:00:00+01:00"}`, DutyWrite))
	if rec.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", rec.Code, rec.Body.String())
	}
	got := f.transitions[0]
	if got.Schedule == nil || got.Schedule.Method != domain.MethodJobs || got.Schedule.Direction != domain.Backward ||
		got.Schedule.At == nil || !got.Schedule.At.Equal(time.Date(2026, 11, 10, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("parameters: %+v", got.Schedule)
	}
	if got.Now.IsZero() {
		t.Error("the handler passes the clock the default start reads")
	}
}

func TestSchedule_NoBodyLeavesTheDefaultsToTheStore(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	if rec := do(h, commandCall(ids.New(), "schedule", "", DutyWrite)); rec.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", rec.Code, rec.Body.String())
	}
	if f.transitions[0].Schedule != nil {
		t.Fatalf("parameters %+v", f.transitions[0].Schedule)
	}
}

func TestSchedule_HashSeparatesParameters(t *testing.T) {
	f := &fakeStore{}
	_, h := newServer(t, f)
	id := ids.New()
	do(h, commandCall(id, "schedule", `{"direction":"forward"}`, DutyWrite))
	do(h, commandCall(id, "schedule", `{"direction":"backward"}`, DutyWrite))
	do(h, commandCall(id, "estimate", ``, DutyWrite))
	if f.transitions[0].RequestHash == f.transitions[1].RequestHash {
		t.Error("another direction under the same key must not look like a replay")
	}
	if f.transitions[2].RequestHash != commandHash(domain.CmdEstimate, "", f.transitions[2].Good, f.transitions[2].Error) {
		t.Error("a command without schedule parameters keeps its hash")
	}
}

func TestSchedule_RefusesBadParameters(t *testing.T) {
	cases := map[string]struct{ cmd, body, field string }{
		"unknown method":     {"schedule", `{"method":"gantt"}`, "method"},
		"unknown direction":  {"schedule", `{"direction":"sideways"}`, "direction"},
		"on another command": {"release", `{"direction":"forward"}`, "method"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, commandCall(ids.New(), c.cmd, c.body, DutyWrite, DutyRelease)), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[c.field]; !ok || len(f.transitions) != 0 {
				t.Fatalf("errors %v, transitions %d", p.Errors, len(f.transitions))
			}
		})
	}
}

func TestListJobs_FiltersAndPage(t *testing.T) {
	res, order := ids.New(), ids.New()
	f := &fakeStore{jobPage: store.JobPage{Items: []store.Job{{ID: ids.New(), JobType: domain.JobProcess}}, Total: 1}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/jobs?resourceId=" + res + "&orderId=" + order +
		"&jobType=setup&orderStatus=Scheduled&from=2026-11-02T08:00:00Z&to=2026-11-03T08:00:00Z&limit=20"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	got := f.jobFilters[0]
	if got.ResourceID != res || got.OrderID != order || got.JobType != domain.JobSetup || got.OrderStatus != domain.Scheduled || got.Limit != 20 ||
		got.From == nil || got.To == nil || !got.To.After(*got.From) {
		t.Fatalf("filter: %+v", got)
	}
	var page struct{ Items []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestListJobs_RefusesBadFilters(t *testing.T) {
	for name, q := range map[string]string{
		"resourceId": "resourceId=x", "jobType": "jobType=weld", "orderStatus": "orderStatus=Done", "from": "from=2026-11-02",
		"to": "from=2026-11-03T00:00:00Z&to=2026-11-02T00:00:00Z", "limit": "limit=0", "cursor": "cursor=nope",
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeStore{}
			_, h := newServer(t, f)
			p := expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/jobs?" + q}), http.StatusBadRequest, validationCode)
			if _, ok := p.Errors[strings.SplitN(name, "&", 2)[0]]; !ok || len(f.jobFilters) != 0 {
				t.Fatalf("errors %v", p.Errors)
			}
		})
	}
}

func TestOrderJobs_UnknownOrderIs404(t *testing.T) {
	f := &fakeStore{getErr: httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "production order not found")}
	_, h := newServer(t, f)
	expectProblem(t, do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/orders/" + ids.New() + "/jobs"}), http.StatusNotFound, store.CodeOrderNotFound)
	if len(f.jobFilters) != 0 {
		t.Error("jobs of an unknown order are not listed")
	}
}

func TestOrderJobs_ListsTheOrdersJobs(t *testing.T) {
	id := ids.New()
	f := &fakeStore{jobPage: store.JobPage{Items: []store.Job{{ID: ids.New(), OrderID: id}}}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodGet, duties: []string{DutyRead}, path: "/v1/production/orders/" + id + "/jobs"})
	if rec.Code != http.StatusOK || f.jobFilters[0].OrderID != id || f.jobFilters[0].Limit != maxOrderJobs {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f.jobFilters)
	}
}
