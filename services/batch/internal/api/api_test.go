package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/api"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/schema"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
)

type fakeStore struct {
	err     error
	ping    error
	def     store.Definition
	inst    store.Instance
	items   []store.WorkItem
	job     store.Job
	run     store.Run
	msg     store.Message
	schema  schema.JSONSchema
	overlap bool
}

func (f *fakeStore) Ping(context.Context) error { return f.ping }
func (f *fakeStore) SaveDefinition(_ context.Context, def store.Definition) (store.Definition, error) {
	if f.err != nil {
		return store.Definition{}, f.err
	}
	def.ID = "def-1"
	f.def = def
	return def, nil
}
func (f *fakeStore) GetActiveDefinition(context.Context, string, string) (store.Definition, error) {
	return f.def, f.err
}
func (f *fakeStore) Submit(context.Context, store.Submit) (store.Instance, error) {
	if f.err != nil {
		return store.Instance{}, f.err
	}
	return f.inst, nil
}
func (f *fakeStore) GetInstance(context.Context, string) (store.Instance, error) {
	if f.err != nil {
		return store.Instance{}, f.err
	}
	return f.inst, nil
}
func (f *fakeStore) ListWorkItems(context.Context, string, string) ([]store.WorkItem, error) {
	return f.items, f.err
}
func (f *fakeStore) Act(context.Context, string, domain.Action) (store.WorkItem, error) {
	if f.err != nil {
		return store.WorkItem{}, f.err
	}
	if len(f.items) == 0 {
		return store.WorkItem{}, nil
	}
	return f.items[0], nil
}
func (f *fakeStore) SaveJob(_ context.Context, job store.Job) (store.Job, error) {
	if f.err != nil {
		return store.Job{}, f.err
	}
	job.ID = "job-1"
	f.job = job
	return job, nil
}
func (f *fakeStore) GetJob(context.Context, string) (store.Job, error) { return f.job, f.err }
func (f *fakeStore) StartRun(context.Context, string, int64, string) (store.Run, error) {
	if f.overlap {
		return store.Run{}, httpx.NewProblem(http.StatusConflict, domain.CodeRunOverlap, "job already has a running instance")
	}
	if f.err != nil {
		return store.Run{}, f.err
	}
	return f.run, nil
}
func (f *fakeStore) GetRun(context.Context, string) (store.Run, error) { return f.run, f.err }
func (f *fakeStore) PublishTemplate(_ context.Context, tpl store.Template, allowed map[string]bool) (store.Template, error) {
	if err := domain.ValidatePlaceholders(tpl.Subject, tpl.Body, allowed); err != nil {
		return store.Template{}, httpx.NewProblem(http.StatusBadRequest, domain.CodeUnknownPlaceholder, err.Error())
	}
	return tpl, f.err
}
func (f *fakeStore) EnqueueMessage(_ context.Context, msg store.Message) (store.Message, error) {
	msg.ID = "m1"
	msg.Status = domain.MsgQueued
	return msg, f.err
}
func (f *fakeStore) RetryMessage(context.Context, string) (store.Message, error) { return f.msg, f.err }
func (f *fakeStore) ListMessages(context.Context, string) ([]store.Message, error) {
	return []store.Message{f.msg}, f.err
}
func (f *fakeStore) PutPreference(_ context.Context, pref store.Preference) (store.Preference, error) {
	return pref, f.err
}
func (f *fakeStore) RegisterSchema(context.Context, string, schema.JSONSchema) error { return f.err }
func (f *fakeStore) GetSchema(context.Context, string) (schema.JSONSchema, error) {
	return f.schema, f.err
}

func newRouter(f *fakeStore) *chi.Mux {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func do(r http.Handler, method, path, body, duties string, withTenant bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Legal-Entity", testkit.TenantID(9))
		req.Header.Set("X-Passage-User", testkit.TenantID(8))
		req.Header.Set("X-Passage-Duties", duties)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const allDuties = "workflow.write,workflow.read,workflow.approve,batch.write,batch.read,notify.write,notify.read,notify.send,events.write,events.read"

func TestAPI(t *testing.T) {
	f := &fakeStore{
		inst:  store.Instance{ID: "i1", Status: domain.InstanceSubmitted},
		items: []store.WorkItem{{ID: "w1", InstanceID: "i1", Status: domain.ItemPending}},
		job:   store.Job{Code: "rows-1m", Handler: "synthetic.rows"},
		run:   store.Run{ID: "r1", Status: domain.RunExecuting, RowsTotal: 100},
		msg:   store.Message{ID: "m1", Status: domain.MsgQueued},
	}
	r := newRouter(f)

	if rec := do(r, http.MethodPost, "/v1/workflows/instances", `{"entity":"purchaserequisition","recordId":"00000000-0000-0000-0000-000000000011","legalEntityId":"00000000-0000-0000-0000-000000000009","assignees":["00000000-0000-0000-0000-000000000006"]}`, allDuties, true); rec.Code != http.StatusCreated {
		t.Fatalf("submit %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodGet, "/v1/workitems?assignee=me&status=pending", "", allDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("inbox %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/batch/jobs", `{"code":"rows-1m","handler":"synthetic.rows","partitionCount":16,"concurrency":4,"exclusive":true}`, allDuties, true); rec.Code != http.StatusCreated {
		t.Fatalf("job %d %s", rec.Code, rec.Body.String())
	}
	f.overlap = true
	if rec := do(r, http.MethodPost, "/v1/batch/jobs/rows-1m/runs", `{"rows":1000000}`, allDuties, true); rec.Code != http.StatusConflict {
		t.Fatalf("overlap %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/notify/templates", `{"code":"workflow.workitem.assigned","channel":"email","language":"de-DE","subject":"x","body":"{{record.nonexistent}}","allowedPlaceholders":["record.number"]}`, allDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("template %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodGet, "/v1/events/topics", "", allDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("topics %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/workflows/instances", `{}`, "batch.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("duty %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/workflows/instances", `{}`, allDuties, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant %d", rec.Code)
	}
}
