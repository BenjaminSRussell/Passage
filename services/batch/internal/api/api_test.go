package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/api"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/inbox"
	"github.com/BenjaminSRussell/passage/services/batch/internal/schema"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
)

type fakeStore struct {
	err         error
	ping        error
	def         store.Definition
	inst        store.Instance
	items       []store.WorkItem
	job         store.Job
	run         store.Run
	msg         store.Message
	schema      schema.JSONSchema
	overlap     bool
	params      json.RawMessage
	legalEntity string
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
func (f *fakeStore) StartRun(_ context.Context, _ string, in store.RunInput) (store.Run, error) {
	f.params, f.legalEntity = in.Params, in.LegalEntityID
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

func TestStartRunPassesParamsToTheStoreAndRejectsUnknownFields(t *testing.T) {
	f := &fakeStore{run: store.Run{ID: "r1", Status: domain.RunExecuting}}
	r := newRouter(f)
	body := `{"rows":5,"params":{"masterId":"` + testkit.TenantID(50) + `","selected":{"size":["S"]}}}`
	if rec := do(r, http.MethodPost, "/v1/batch/jobs/explode/runs", body, allDuties, true); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(string(f.params), `"masterId"`) || !strings.Contains(string(f.params), `"size"`) {
		t.Fatalf("params not forwarded: %s", f.params)
	}
	f.params = nil
	if rec := do(r, http.MethodPost, "/v1/batch/jobs/explode/runs", `{"rows":5,"param":{}}`, allDuties, true); rec.Code != http.StatusBadRequest || f.params != nil {
		t.Fatalf("a misspelt params key must be a 400, got %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, "/v1/batch/jobs/explode/runs", body, "batch.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("starting a run needs batch.write, got %d", rec.Code)
	}
}

type staticSource struct {
	items []inbox.Item
	err   error
}

func (staticSource) Name() string { return "ledger" }
func (staticSource) Duty() string { return "ledger.journal.approve" }
func (s staticSource) Fetch(context.Context, tenant.Principal, string) (inbox.Fetched, error) {
	return inbox.Fetched{Items: s.items}, s.err
}

func inboxRequest(r http.Handler, target, entities, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Legal-Entity", entities)
	req.Header.Set("X-Passage-User", testkit.TenantID(8))
	req.Header.Set("X-Passage-Duties", duties)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestInbox(t *testing.T) {
	entity, other := testkit.TenantID(9), testkit.TenantID(10)
	agg := &inbox.Aggregator{Sources: []inbox.Source{staticSource{items: []inbox.Item{{ID: "b1", Source: "ledger", CreatedAt: time.Now()}}}}}
	r := httpx.NewRouter()
	api.New(&fakeStore{}).WithInbox(agg).Register(r)
	duties := "inbox.read,ledger.journal.approve"

	rec := inboxRequest(r, "/v1/inbox", entity, duties)
	var page inbox.Page
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].ID != "b1" {
		t.Fatalf("inbox %d %s", rec.Code, rec.Body.String())
	}
	cases := map[string]struct {
		target, entities, duties string
		want                     int
	}{
		"no inbox duty":                {"/v1/inbox", entity, "ledger.journal.approve", http.StatusForbidden},
		"several entities, none named": {"/v1/inbox", entity + "," + other, duties, http.StatusBadRequest},
		"entity outside the scope":     {"/v1/inbox?legalEntityId=" + testkit.TenantID(11), entity, duties, http.StatusForbidden},
		"entity not a UUID":            {"/v1/inbox?legalEntityId=nope", entity, duties, http.StatusBadRequest},
		"limit above the maximum":      {"/v1/inbox?limit=201", entity, duties, http.StatusBadRequest},
		"cursor not from the inbox":    {"/v1/inbox?cursor=!!", entity, duties, http.StatusBadRequest},
		"named entity in scope":        {"/v1/inbox?legalEntityId=" + other, entity + "," + other, duties, http.StatusOK},
	}
	for name, c := range cases {
		if rec := inboxRequest(r, c.target, c.entities, c.duties); rec.Code != c.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// flakySource answers with its items until fail is set, as a source that goes down between two pages.
type flakySource struct {
	items []inbox.Item
	fail  *bool
}

func (flakySource) Name() string { return "ledger" }
func (flakySource) Duty() string { return "ledger.journal.approve" }
func (s flakySource) Fetch(context.Context, tenant.Principal, string) (inbox.Fetched, error) {
	if *s.fail {
		return inbox.Fetched{}, errors.New("down")
	}
	return inbox.Fetched{Items: s.items}, nil
}

// #547: a cursor from a page whose sources answered differently is a 409 with its own code, not the invalid-input one.
func TestInboxCursorFromDifferentSourcesIs409WithItsOwnCode(t *testing.T) {
	fail := false
	now := time.Now()
	agg := &inbox.Aggregator{Sources: []inbox.Source{flakySource{fail: &fail, items: []inbox.Item{
		{ID: "b1", Source: "ledger", CreatedAt: now}, {ID: "b2", Source: "ledger", CreatedAt: now.Add(-time.Hour)}}}}}
	r := httpx.NewRouter()
	api.New(&fakeStore{}).WithInbox(agg).Register(r)
	duties, entity := "inbox.read,ledger.journal.approve", testkit.TenantID(9)

	first := inboxRequest(r, "/v1/inbox?limit=1", entity, duties)
	var page inbox.Page
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || page.NextCursor == "" {
		t.Fatalf("first page %d %s", first.Code, first.Body.String())
	}
	fail = true
	rec := inboxRequest(r, "/v1/inbox?limit=1&cursor="+url.QueryEscape(page.NextCursor), entity, duties)
	var problem struct {
		Code string `json:"code"`
	}
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &problem) != nil || problem.Code != "WFL-0412" {
		t.Fatalf("second page %d %s, want 409 WFL-0412", rec.Code, rec.Body.String())
	}
}

func TestInboxStaysAvailableWhenASourceFails(t *testing.T) {
	agg := &inbox.Aggregator{Sources: []inbox.Source{staticSource{err: errors.New("down")}}}
	r := httpx.NewRouter()
	api.New(&fakeStore{}).WithInbox(agg).Register(r)
	rec := inboxRequest(r, "/v1/inbox", testkit.TenantID(9), "inbox.read,ledger.journal.approve")
	var page inbox.Page
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Unavailable) != 1 || page.Unavailable[0].Source != "ledger" || len(page.Items) != 0 {
		t.Fatalf("inbox %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(newRouter(&fakeStore{}), http.MethodGet, "/v1/inbox", "", "inbox.read", true); rec.Code != http.StatusNotFound {
		t.Fatalf("route without an aggregator: %d", rec.Code)
	}
}
