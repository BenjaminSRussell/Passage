package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/schema"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
)

const (
	readyTimeout   = 500 * time.Millisecond
	dutyWorkflowW  = "workflow.write"
	dutyWorkflowR  = "workflow.read"
	dutyApprove    = "workflow.approve"
	dutyBatchW     = "batch.write"
	dutyBatchR     = "batch.read"
	dutyNotifyW    = "notify.write"
	dutyNotifyR    = "notify.read"
	dutyNotifySend = "notify.send"
	dutyEventsW    = "events.write"
	dutyEventsR    = "events.read"
)

// API is the Wave 04 HTTP surface.
type API struct {
	store     store.Store
	submitted atomic.Int64
	failed    atomic.Int64
}

func New(s store.Store) *API { return &API{store: s} }

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyWorkflowW)).Post("/v1/workflows/definitions", a.saveDefinition)
	r.With(authz.RequireDuty(dutyWorkflowW)).Post("/v1/workflows/instances", a.submit)
	r.With(authz.RequireDuty(dutyWorkflowR)).Get("/v1/workflows/instances/{id}", a.getInstance)
	r.With(authz.RequireDuty(dutyWorkflowW)).Post("/v1/workflows/instances/{id}/actions", a.instanceAction)
	r.With(authz.RequireDuty(dutyApprove)).Get("/v1/workitems", a.inbox)
	r.With(authz.RequireDuty(dutyApprove)).Post("/v1/workitems/{id}/actions", a.act)
	r.With(authz.RequireDuty(dutyBatchW)).Post("/v1/batch/jobs", a.saveJob)
	r.With(authz.RequireDuty(dutyBatchR)).Get("/v1/batch/jobs/{code}", a.getJob)
	r.With(authz.RequireDuty(dutyBatchW)).Post("/v1/batch/jobs/{code}/runs", a.startRun)
	r.With(authz.RequireDuty(dutyBatchR)).Get("/v1/batch/runs/{id}", a.getRun)
	r.With(authz.RequireDuty(dutyNotifyW)).Post("/v1/notify/templates", a.publishTemplate)
	r.With(authz.RequireDuty(dutyNotifySend)).Post("/v1/notify/messages", a.enqueueMessage)
	r.With(authz.RequireDuty(dutyNotifyR)).Get("/v1/notify/messages", a.listMessages)
	r.With(authz.RequireDuty(dutyNotifyW)).Post("/v1/notify/messages/{id}/retry", a.retryMessage)
	r.With(authz.RequireDuty(dutyNotifyW)).Put("/v1/notify/preferences/me", a.putPreference)
	r.With(authz.RequireDuty(dutyEventsR)).Get("/v1/events/topics", a.topics)
	r.With(authz.RequireDuty(dutyEventsR)).Get("/v1/events/asyncapi/{domain}", a.asyncAPI)
	r.With(authz.RequireDuty(dutyEventsR)).Get("/v1/events/schemas/{type}", a.getSchema)
	r.With(authz.RequireDuty(dutyEventsW)).Post("/v1/events/schemas/{type}", a.registerSchema)
	r.With(authz.RequireDuty(dutyBatchW)).Post("/v1/events/replay", a.replay)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# HELP batch_submitted_total Workflow submits and job starts.\n# TYPE batch_submitted_total counter\nbatch_submitted_total %d\n", a.submitted.Load())
		_, _ = fmt.Fprintf(w, "# HELP batch_failed_total Failed commands.\n# TYPE batch_failed_total counter\nbatch_failed_total %d\n", a.failed.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type definitionRequest struct {
	Entity          string        `json:"entity"`
	LegalEntityID   string        `json:"legalEntityId"`
	CommentRequired bool          `json:"commentRequired"`
	AdminUserID     string        `json:"adminUserId"`
	Steps           []domain.Step `json:"steps"`
}

func (a *API) saveDefinition(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req definitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	def, err := a.store.SaveDefinition(r.Context(), store.Definition{
		Entity: req.Entity, LegalEntityID: req.LegalEntityID, CommentReq: req.CommentRequired,
		AdminUserID: req.AdminUserID, Steps: req.Steps, Status: "active", Version: 1,
	})
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, def)
}

type submitRequest struct {
	Entity        string   `json:"entity"`
	RecordID      string   `json:"recordId"`
	LegalEntityID string   `json:"legalEntityId"`
	Assignees     []string `json:"assignees"`
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req submitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.RecordID) || !ids.Valid(req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "recordId and legalEntityId must be UUIDs"))
		return
	}
	inst, err := a.store.Submit(r.Context(), store.Submit{Entity: req.Entity, RecordID: req.RecordID, LegalEntityID: req.LegalEntityID, Assignees: req.Assignees})
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.submitted.Add(1)
	httpx.JSON(w, http.StatusCreated, inst)
}

func (a *API) getInstance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	inst, err := a.store.GetInstance(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, inst)
}

type actionRequest struct {
	Kind     string `json:"kind"`
	Comment  string `json:"comment"`
	Delegate string `json:"delegate"`
}

func (a *API) instanceAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req actionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items, err := a.store.ListWorkItems(r.Context(), "", "")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id := chi.URLParam(r, "id")
	for _, item := range items {
		if item.InstanceID == id {
			out, err := a.store.Act(r.Context(), item.ID, domain.Action{Kind: req.Kind, Comment: req.Comment, Delegate: req.Delegate})
			if err != nil {
				a.failed.Add(1)
				httpx.Fail(w, r, err)
				return
			}
			httpx.JSON(w, http.StatusOK, out)
			return
		}
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "no open work item"))
}

func (a *API) inbox(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListWorkItems(r.Context(), r.URL.Query().Get("assignee"), r.URL.Query().Get("status"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, items)
}

func (a *API) act(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req actionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.Act(r.Context(), chi.URLParam(r, "id"), domain.Action{Kind: req.Kind, Comment: req.Comment, Delegate: req.Delegate})
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) saveJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var job store.Job
	if err := httpx.Decode(r, &job); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.SaveJob(r.Context(), job)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	job, err := a.store.GetJob(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, job)
}

type runRequest struct {
	Rows int64 `json:"rows"`
}

func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	run, err := a.store.StartRun(r.Context(), chi.URLParam(r, "code"), req.Rows, p.UserID)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.submitted.Add(1)
	httpx.JSON(w, http.StatusCreated, run)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	run, err := a.store.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

func (a *API) publishTemplate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req struct {
		store.Template
		Allowed []string `json:"allowedPlaceholders"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	allowed := map[string]bool{}
	for _, name := range req.Allowed {
		allowed[name] = true
	}
	if len(allowed) == 0 {
		allowed = map[string]bool{"record.number": true, "record.amount": true, "link": true, "workitem.due_at": true, "instance.submitted_by.display_name": true}
	}
	out, err := a.store.PublishTemplate(r.Context(), req.Template, allowed)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) enqueueMessage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var msg store.Message
	if err := httpx.Decode(r, &msg); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.EnqueueMessage(r.Context(), msg)
	if err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	out, err := a.store.ListMessages(r.Context(), r.URL.Query().Get("status"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) retryMessage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	out, err := a.store.RetryMessage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) putPreference(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var pref store.Preference
	if err := httpx.Decode(r, &pref); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.PutPreference(r.Context(), pref)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) topics(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, []map[string]string{
		{"topic": "passage.workflow.instance", "type": "dev.passage.workflow.instance.submitted.v1"},
		{"topic": "passage.workflow.workitem", "type": "dev.passage.workflow.workitem.assigned.v1"},
		{"topic": "passage.batch.run", "type": "dev.passage.batch.run.completed.v1"},
		{"topic": "passage.notify.message", "type": "dev.passage.notify.message.sent.v1"},
		{"topic": "passage.metadata.entity", "type": "dev.passage.metadata.entity.published.v1"},
	})
}

func (a *API) asyncAPI(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"asyncapi": "3.0.0", "info": map[string]string{"title": chi.URLParam(r, "domain"), "version": "0.1.0"}})
}

func (a *API) getSchema(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	body, err := a.store.GetSchema(r.Context(), chi.URLParam(r, "type"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) registerSchema(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body schema.JSONSchema
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.store.RegisterSchema(r.Context(), chi.URLParam(r, "type"), body); err != nil {
		a.failed.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"status": "registered"})
}

func (a *API) replay(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && p.LegalEntityID != "*" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}
