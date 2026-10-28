package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	feedbackID  = "0195f0a0-0000-7000-8000-000000000030"
	jobResource = "0195f0a0-0000-7000-8000-000000000021"
	registerDut = DutyRegister
	jobDuties   = DutyRegister + "," + DutyJobSchedule
)

// The base fake answers job calls with nothing; jobFake records them.
func (f *fakeStore) UpsertJob(_ context.Context, j store.Job) (store.Job, bool, error) {
	return j, true, nil
}

func (f *fakeStore) GetJob(context.Context, string) (store.Job, error) {
	return store.Job{}, httpx.NewProblem(http.StatusNotFound, store.CodeJobNotFound, "job not found")
}

func (f *fakeStore) ListJobs(context.Context, store.JobFilter) ([]store.Job, error) {
	return nil, nil
}

func (f *fakeStore) RecordFeedback(context.Context, string, store.FeedbackInput) (store.FeedbackResult, error) {
	return store.FeedbackResult{}, nil
}

// jobFake records job calls; seen feedback ids replay as duplicates like the store.
type jobFake struct {
	fakeStore
	upserted []store.Job
	listed   store.JobFilter
	feedback []store.FeedbackInput
	seen     map[string]bool
	err      error
}

func (f *jobFake) UpsertJob(_ context.Context, j store.Job) (store.Job, bool, error) {
	f.upserted = append(f.upserted, j)
	return j, len(f.upserted) == 1, f.err
}

func (f *jobFake) ListJobs(_ context.Context, flt store.JobFilter) ([]store.Job, error) {
	f.listed = flt
	return nil, f.err
}

func (f *jobFake) RecordFeedback(_ context.Context, id string, in store.FeedbackInput) (store.FeedbackResult, error) {
	if f.err != nil {
		return store.FeedbackResult{}, f.err
	}
	f.feedback = append(f.feedback, in)
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	dup := f.seen[in.ID]
	f.seen[in.ID] = true
	return store.FeedbackResult{Job: store.Job{ID: id, Status: store.JobStarted}, Duplicate: dup}, nil
}

const upsertBody = `{"id":"` + jobID + `","orderRef":"MO-1","operationRef":"OP-10","resourceId":"` + jobResource + `",
"plannedQuantity":"100","sequenceStart":"2026-10-20T08:00:00Z",
"checks":[{"code":"len","name":"Length","kind":"numeric","min":"10","max":"12","mandatory":true}]}`

func feedbackPath() string { return "/v1/shopfloor/jobs/" + jobID + ":feedback" }

func problemOf(t *testing.T, body []byte) (code string, fields map[string]string) {
	t.Helper()
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("not a problem: %s", body)
	}
	return p.Code, p.Errors
}

// The schedule writes a job's plan: 201 when new, 200 when it changes an existing job.
func TestUpsertJob_CreatedThenUpdated(t *testing.T) {
	f := &jobFake{}
	h := newRouter(f)
	first := call(t, h, http.MethodPost, "/v1/shopfloor/jobs", upsertBody, jobDuties)
	second := call(t, h, http.MethodPost, "/v1/shopfloor/jobs", upsertBody, jobDuties)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("status %d then %d: %s", first.Code, second.Code, second.Body.String())
	}
	got := f.upserted[0]
	if got.OrderRef != "MO-1" || got.PlannedQty.String() != "100" || len(got.Checks) != 1 || !got.Checks[0].Mandatory || got.Checks[0].Max.String() != "12" {
		t.Fatalf("upserted = %+v", got)
	}
}

func TestUpsertJob_ValidationFailures(t *testing.T) {
	bad := `{"id":"nope","orderRef":" ","operationRef":"OP","resourceId":"` + jobResource + `","plannedQuantity":"1.00001",
"checks":[{"code":"len","name":"L","kind":"numeric","min":"5","max":"1"},{"code":"len","name":"L","kind":"pass-fail"}]}`
	f := &jobFake{}
	rec := call(t, newRouter(f), http.MethodPost, "/v1/shopfloor/jobs", bad, jobDuties)
	code, fields := problemOf(t, rec.Body.Bytes())
	if rec.Code != http.StatusBadRequest || code != "COM-0001" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"id", "orderRef", "plannedQuantity", "sequenceStart", "checks[0].max", "checks[1].code"} {
		if fields[field] == "" {
			t.Errorf("want field error %q in %v", field, fields)
		}
	}
	if len(f.upserted) != 0 {
		t.Fatal("an invalid job must not reach the store")
	}
}

// Writing the projection is the schedule's service duty, not the operator's.
func TestUpsertJob_RequiresScheduleDuty(t *testing.T) {
	rec := call(t, newRouter(&jobFake{}), http.MethodPost, "/v1/shopfloor/jobs", upsertBody, registerDut)
	if code, _ := problemOf(t, rec.Body.Bytes()); rec.Code != http.StatusForbidden || code != "AUTH-0003" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListJobs_FiltersAndValidation(t *testing.T) {
	f := &jobFake{}
	rec := call(t, newRouter(f), http.MethodGet, "/v1/shopfloor/jobs?resourceId="+jobResource+"&status=started&limit=20", "", registerDut)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if f.listed != (store.JobFilter{ResourceID: jobResource, Status: store.JobStarted, Limit: 20}) {
		t.Fatalf("filter = %+v", f.listed)
	}
	for _, q := range []string{"status=paused", "resourceId=r1", "limit=0"} {
		if rec := call(t, newRouter(f), http.MethodGet, "/v1/shopfloor/jobs?"+q, "", registerDut); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func TestGetJob_NotFoundAndBadID(t *testing.T) {
	h := newRouter(&jobFake{})
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/jobs/"+jobID, "", registerDut)
	if code, _ := problemOf(t, rec.Body.Bytes()); rec.Code != http.StatusNotFound || code != store.CodeJobNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/jobs/nope", "", registerDut); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func feedbackBody(extra string) string {
	return `{"feedbackId":"` + feedbackID + `","goodQuantity":"9","scrapQuantity":"1","scrapReasonId":"BURR","finished":true` + extra + `}`
}

// A stored feedback is 201; the same feedbackId again is 200 with duplicate true (offline replay).
func TestFeedback_StoredThenReplayed(t *testing.T) {
	f := &jobFake{}
	h := newRouter(f)
	first := call(t, h, http.MethodPost, feedbackPath(), feedbackBody(`,"checks":[{"code":"len","value":" 11 "}]`), registerDut)
	second := call(t, h, http.MethodPost, feedbackPath(), feedbackBody(`,"checks":[{"code":"len","value":" 11 "}]`), registerDut)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"duplicate":true`) {
		t.Fatalf("first %d second %d body %s", first.Code, second.Code, second.Body.String())
	}
	in := f.feedback[0]
	if in.ID != feedbackID || in.GoodQty.String() != "9" || in.ScrapQty.String() != "1" || *in.ScrapReasonID != "BURR" ||
		!in.Finished || in.RecordedBy != "supervisor-1" || in.OverrideReason != nil || len(in.Checks) != 1 {
		t.Fatalf("feedback = %+v", in)
	}
}

func TestFeedback_ValidationFailures(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"scrap needs a reason", `{"feedbackId":"` + feedbackID + `","scrapQuantity":"1"}`, "scrapReasonId"},
		{"blank reason", `{"feedbackId":"` + feedbackID + `","scrapQuantity":"1","scrapReasonId":"  "}`, "scrapReasonId"},
		{"negative good", `{"feedbackId":"` + feedbackID + `","goodQuantity":"-1"}`, "goodQuantity"},
		{"too many decimals", `{"feedbackId":"` + feedbackID + `","goodQuantity":"1.00001"}`, "goodQuantity"},
		{"nothing reported", `{"feedbackId":"` + feedbackID + `","goodQuantity":"0"}`, "goodQuantity"},
		{"feedback id", `{"feedbackId":"x","finished":true}`, "feedbackId"},
		{"blank check value", `{"feedbackId":"` + feedbackID + `","checks":[{"code":"len","value":" "}]}`, "checks[0].value"},
		{"check twice", `{"feedbackId":"` + feedbackID + `","checks":[{"code":"len","value":"1"},{"code":"len","value":"2"}]}`, "checks[1].code"},
		{"override without finish", `{"feedbackId":"` + feedbackID + `","goodQuantity":"1","override":{"reason":"x"}}`, "override"},
		{"override without reason", `{"feedbackId":"` + feedbackID + `","finished":true,"override":{"reason":" "}}`, "override.reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &jobFake{}
			rec := call(t, newRouter(f), http.MethodPost, feedbackPath(), tc.body, registerDut+","+DutyCheckOverride)
			code, fields := problemOf(t, rec.Body.Bytes())
			if rec.Code != http.StatusBadRequest || code != "COM-0001" || fields[tc.field] == "" {
				t.Fatalf("status %d, want field %q: %s", rec.Code, tc.field, rec.Body.String())
			}
			if len(f.feedback) != 0 {
				t.Fatal("invalid feedback reached the store")
			}
		})
	}
}

// FR-25.5: only a holder of the override duty finishes over a failed mandatory check.
func TestFeedback_OverrideNeedsDutyAndUser(t *testing.T) {
	body := feedbackBody(`,"override":{"reason":" Retest on CMM passed "}`)
	f := &jobFake{}
	rec := call(t, newRouter(f), http.MethodPost, feedbackPath(), body, registerDut)
	if code, _ := problemOf(t, rec.Body.Bytes()); rec.Code != http.StatusForbidden || code != "AUTH-0003" || len(f.feedback) != 0 {
		t.Fatalf("operator override: %d %s", rec.Code, rec.Body.String())
	}
	rec = callWith(t, newRouter(f), http.MethodPost, feedbackPath(), body, registerDut+","+DutyCheckOverride, map[string]string{"X-Passage-User": ""})
	if rec.Code != http.StatusUnauthorized || len(f.feedback) != 0 {
		t.Fatalf("anonymous override: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, newRouter(f), http.MethodPost, feedbackPath(), body, registerDut+","+DutyCheckOverride)
	if rec.Code != http.StatusCreated || f.feedback[0].OverrideReason == nil || *f.feedback[0].OverrideReason != "Retest on CMM passed" {
		t.Fatalf("supervisor override: %d %s", rec.Code, rec.Body.String())
	}
}

// AC-25.9: the store refuses the finish with SF-0005; the handler passes it on and counts it.
func TestFeedback_BlockedFinishIsSF0005(t *testing.T) {
	f := &jobFake{err: httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeMandatoryCheck, "Mandatory check len failed; operation cannot be finished.")}
	a := New(f)
	r := httpx.NewRouter()
	a.Register(r)
	rec := call(t, r, http.MethodPost, feedbackPath(), feedbackBody(`,"checks":[{"code":"len","value":"13"}]`), registerDut)
	if code, _ := problemOf(t, rec.Body.Bytes()); rec.Code != http.StatusUnprocessableEntity || code != "SF-0005" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if a.finishBlocked.Load() != 1 || a.failed.Load() != 0 {
		t.Fatalf("finishBlocked %d failed %d", a.finishBlocked.Load(), a.failed.Load())
	}
}
