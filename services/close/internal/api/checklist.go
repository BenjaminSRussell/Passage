package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

func (a *API) putTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.checklists == nil {
		fail(w, r, missingSlice("close checklist"))
		return
	}
	id := chi.URLParam(r, "templateId")
	if !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "templateId must be a UUID"))
		return
	}
	var tmpl domain.Template
	if err := httpx.Decode(r, &tmpl); err != nil {
		fail(w, r, err)
		return
	}
	tmpl.ID = id
	if tmpl.Name == "" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "name is required"))
		return
	}
	out, err := a.checklists.PutTemplate(r.Context(), tmpl)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.checklists == nil {
		fail(w, r, missingSlice("close checklist"))
		return
	}
	id := chi.URLParam(r, "templateId")
	if !ids.Valid(id) {
		fail(w, r, &domain.Error{Code: domain.CodeNoRun, Detail: "template not found"})
		return
	}
	out, err := a.checklists.GetTemplate(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type createRunRequest struct {
	TemplateID     string   `json:"templateId"`
	FiscalPeriodID string   `json:"fiscalPeriodId"`
	LegalEntityIDs []string `json:"legalEntityIds"`
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.checklists == nil {
		fail(w, r, missingSlice("close checklist"))
		return
	}
	if err := requireKey(r.Header.Get(idempotencyHeader)); err != nil {
		fail(w, r, err)
		return
	}
	var req createRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if !ids.Valid(req.TemplateID) || !ids.Valid(req.FiscalPeriodID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "templateId and fiscalPeriodId must be UUIDs"))
		return
	}
	for _, id := range req.LegalEntityIDs {
		if !ids.Valid(id) {
			fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "legalEntityIds must be UUIDs"))
			return
		}
	}
	out, err := a.checklists.CreateRun(r.Context(), req.TemplateID, req.FiscalPeriodID, p.UserID, req.LegalEntityIDs, r.Header.Get(idempotencyHeader), store.HashRequest(http.MethodPost, r.URL.Path, []byte(req.TemplateID+req.FiscalPeriodID)))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) board(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.checklists == nil {
		fail(w, r, missingSlice("close checklist"))
		return
	}
	id := chi.URLParam(r, "runId")
	if !ids.Valid(id) {
		fail(w, r, &domain.Error{Code: domain.CodeNoRun, Detail: "close run not found"})
		return
	}
	q := r.URL.Query()
	tasks, err := a.checklists.ListTasks(r.Context(), id, q.Get("entity"), q.Get("status"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if tasks == nil {
		tasks = []domain.BoardTask{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": tasks})
}

type completeBody struct {
	EvidenceURI string `json:"evidenceUri"`
}
type skipBody struct {
	Reason string `json:"reason"`
}
type reassignBody struct {
	AssigneeUserID string `json:"assigneeUserId"`
}

func (a *API) startTask(w http.ResponseWriter, r *http.Request) {
	a.taskAction(w, r, func(id, actor string) (domain.BoardTask, error) {
		return a.checklists.StartTask(r.Context(), id, actor)
	})
}

func (a *API) completeTask(w http.ResponseWriter, r *http.Request) {
	var body completeBody
	if err := optionalDecode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	a.taskAction(w, r, func(id, actor string) (domain.BoardTask, error) {
		return a.checklists.CompleteTask(r.Context(), id, actor, body.EvidenceURI)
	})
}

func (a *API) skipTask(w http.ResponseWriter, r *http.Request) {
	var body skipBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	a.taskAction(w, r, func(id, actor string) (domain.BoardTask, error) {
		return a.checklists.SkipTask(r.Context(), id, actor, body.Reason)
	})
}

func (a *API) reassignTask(w http.ResponseWriter, r *http.Request) {
	var body reassignBody
	if err := httpx.Decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	a.taskAction(w, r, func(id, actor string) (domain.BoardTask, error) {
		return a.checklists.ReassignTask(r.Context(), id, actor, body.AssigneeUserID)
	})
}

func (a *API) taskAction(w http.ResponseWriter, r *http.Request, fn func(id, actor string) (domain.BoardTask, error)) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.checklists == nil {
		fail(w, r, missingSlice("close checklist"))
		return
	}
	id := chi.URLParam(r, "taskId")
	if !ids.Valid(id) {
		fail(w, r, &domain.Error{Code: domain.CodeNoRun, Detail: "task not found"})
		return
	}
	out, err := fn(id, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func optionalDecode(r *http.Request, v any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return httpx.Decode(r, v)
}
