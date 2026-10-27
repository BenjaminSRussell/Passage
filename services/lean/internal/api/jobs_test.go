package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

const (
	jobID  = "66666666-0000-4000-8000-000000000001"
	cellID = "66666666-0000-4000-8000-000000000002"
)

func TestStartJob_PassesVersionAndKey(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":3}`, duties: DutyKanbanWork, headers: key("s-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotJobID != jobID || fs.gotVersion != 3 || fs.gotIdem.Key != "s-1" || fs.gotIdem.Fingerprint == "" {
		t.Fatalf("start %d %s %+v", rec.Code, rec.Body, fs)
	}
	fs.replay = true
	if again := (call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":3}`, duties: DutyKanbanWork, headers: key("s-1")}).do(newRouter(fs)); again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay headers %v", again.Header())
	}
	// A job that cannot start (a picking activity on a Planned card) is 409 LEAN-0001.
	fs = &fakeStore{err: &domain.Error{Code: domain.CodeMove, Detail: "the activity needs picking"}}
	rec = call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":1}`, duties: DutyKanbanWork, headers: key("s-2")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "LEAN-0001" {
		t.Fatalf("refused start: %d %s", rec.Code, rec.Body)
	}
	fs = &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeJobNotFound, "kanban job not found")}
	rec = call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":1}`, duties: DutyKanbanWork, headers: key("s-3")}.do(newRouter(fs))
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "LEAN-0108" {
		t.Fatalf("missing job: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":0}`, duties: DutyKanbanWork, headers: key("s-4")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["version"] == "" {
		t.Fatalf("no version: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":1}`, duties: DutyKanbanWork}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":start", body: `{"version":1}`, duties: DutyRead, headers: key("s-5")}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty started a job: %d", rec.Code)
	}
}

func TestCompleteJob_DefaultsAndValidation(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":complete", body: `{"version":2,"reportedQty":"19.5"}`,
		duties: DutyKanbanWork, headers: key("c-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotVersion != 2 || !fs.gotDone.ReportedQty.Equal(decimal.RequireFromString("19.5")) ||
		!fs.gotDone.ScrapQty.IsZero() || fs.gotDone.HandlingUnits != 1 {
		t.Fatalf("complete %d %s %+v", rec.Code, rec.Body, fs.gotDone)
	}
	rec = call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":complete", body: `{"version":2,"reportedQty":"0","scrapQty":"2","handlingUnits":3}`,
		duties: DutyKanbanWork, headers: key("c-2")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || !fs.gotDone.ScrapQty.Equal(decimal.NewFromInt(2)) || fs.gotDone.HandlingUnits != 3 {
		t.Fatalf("scrap only %d %+v", rec.Code, fs.gotDone)
	}
	cases := map[string]struct{ body, field string }{
		"no reported quantity": {`{"version":1}`, "reportedQty"},
		"negative scrap":       {`{"version":1,"reportedQty":"1","scrapQty":"-1"}`, "scrapQty"},
		"too many decimals":    {`{"version":1,"reportedQty":"0.0000001"}`, "reportedQty"},
		"nothing reported":     {`{"version":1,"reportedQty":"0","scrapQty":"0"}`, "reportedQty"},
		"zero handling units":  {`{"version":1,"reportedQty":"1","handlingUnits":0}`, "handlingUnits"},
		"no version":           {`{"reportedQty":"1"}`, "version"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":complete", body: c.body, duties: DutyKanbanWork, headers: key("c")}.do(newRouter(fs))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[c.field] == "" {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if fs.gotJobID != "" {
				t.Fatal("store called on invalid input")
			}
		})
	}
	// AC-24.7 on the work-cell board: a Planned job dragged to Completed is 409 LEAN-0001.
	fs = &fakeStore{err: &domain.Error{Code: domain.CodeMove, Detail: "kanban job move planned → completed is not allowed"}}
	rec = call{method: "POST", path: "/v1/lean/kanban-jobs/" + jobID + ":complete", body: `{"version":1,"reportedQty":"20"}`,
		duties: DutyKanbanWork, headers: key("c-3")}.do(newRouter(fs))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != "LEAN-0001" {
		t.Fatalf("planned → completed: %d %s", rec.Code, rec.Body)
	}
}

func TestBoard_WorkCellGroupsJobsIntoStateColumns(t *testing.T) {
	fs := &fakeStore{jobs: []store.Job{
		{ID: "a", State: domain.InProgress}, {ID: "b", State: domain.Planned}, {ID: "c", State: domain.Completed}, {ID: "d", State: domain.Planned},
	}}
	rec := call{method: "GET", path: "/v1/lean/board?workCellId=" + cellID + "&flowId=" + flowID, duties: DutyKanbanWork}.do(newRouter(fs))
	var body struct {
		WorkCellID string `json:"workCellId"`
		Columns    []struct {
			State string `json:"state"`
			Jobs  []struct {
				ID string `json:"id"`
			} `json:"jobs"`
			Kanbans []any `json:"kanbans"`
		} `json:"columns"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.WorkCellID != cellID || len(body.Columns) != 5 ||
		fs.gotBoard.WorkCellID != cellID || fs.gotBoard.FlowID != flowID {
		t.Fatalf("job board %d %s %+v", rec.Code, rec.Body, fs.gotBoard)
	}
	want := map[string]int{"not-planned": 0, "planned": 2, "prepared": 0, "in-progress": 1, "completed": 1}
	order := []string{"not-planned", "planned", "prepared", "in-progress", "completed"}
	for i, c := range body.Columns {
		if c.State != order[i] || len(c.Jobs) != want[c.State] || c.Kanbans != nil {
			t.Fatalf("column %d = %s with %d jobs", i, c.State, len(c.Jobs))
		}
	}
	if rec := (call{method: "GET", path: "/v1/lean/board?workCellId=cell-1", duties: DutyKanbanWork}).do(newRouter(fs)); rec.Code != http.StatusBadRequest ||
		problemOf(t, rec).Errors["workCellId"] == "" {
		t.Fatalf("bad work cell: %d", rec.Code)
	}
}
