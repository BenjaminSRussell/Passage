package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const templatesPath = "/v1/integration/data-templates"

type publishTemplateRequest struct {
	Name     string            `json:"name"`
	Entity   string            `json:"entity"`
	Version  int               `json:"version"`
	Defaults map[string]string `json:"defaults"`
}

type templateView struct {
	ID           string            `json:"id"`
	Entity       string            `json:"entity"`
	Name         string            `json:"name"`
	Version      int               `json:"version"`
	Checksum     string            `json:"checksum"`
	DefaultCount int               `json:"defaultCount"`
	Defaults     map[string]string `json:"defaults,omitempty"`
	CreatedAt    time.Time         `json:"createdAt"`
}

func (a *API) registerTemplates(r chi.Router) {
	r.With(authz.RequireDuty(dutySpecWrite)).Post(templatesPath, a.publishTemplate)
	r.With(authz.RequireDuty(dutyRun)).Get(templatesPath, a.listTemplates)
	r.With(authz.RequireDuty(dutyRun)).Get(templatesPath+"/{id}", a.getTemplate)
}

// templateFor rebuilds the domain template from a stored one and checks it fits the mapping of the run.
func templateFor(stored store.DataTemplate, mapping domain.Mapping) (domain.DataTemplate, error) {
	t := domain.DataTemplate{Name: stored.Name, Entity: stored.Entity, Version: stored.Version, Defaults: stored.Defaults}
	return t, mapping.CheckTemplate(t)
}

func (a *API) publishTemplate(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req publishTemplateRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	raw, err := json.Marshal(req)
	if err != nil {
		fail(w, r, err)
		return
	}
	tmpl, err := domain.ParseTemplate(raw)
	if err != nil {
		fail(w, r, problemFrom(err))
		return
	}
	trimmed, err := json.Marshal(tmpl)
	if err != nil {
		fail(w, r, err)
		return
	}
	_, sum, err := canonicalize(trimmed)
	if err != nil {
		fail(w, r, problemFrom(&domain.Error{Code: domain.CodeTemplateInvalid, Detail: "template JSON: " + err.Error()}))
		return
	}
	saved, created, err := a.store.SaveDataTemplate(r.Context(), store.DataTemplate{
		Entity: tmpl.Entity, Name: tmpl.Name, Version: tmpl.Version, Checksum: sum, Defaults: tmpl.Defaults,
	}, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "publish data template failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", templatesPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, templateToView(saved, true))
}

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var after *store.TemplateCursor
	if v := r.URL.Query().Get("cursor"); v != "" {
		if after, ok = templateCursor(v); !ok {
			fail(w, r, cursorProblem())
			return
		}
	}
	templates, err := a.store.ListDataTemplates(r.Context(), r.URL.Query().Get("entity"), after, limit+1)
	if err != nil {
		a.logFailure(r, "list data templates failed", err)
		fail(w, r, err)
		return
	}
	templates, more := trimPage(templates, limit)
	items := make([]templateView, 0, len(templates))
	for _, t := range templates {
		items = append(items, templateToView(t, false))
	}
	body := map[string]any{"items": items}
	if more {
		last := templates[limit-1]
		body["nextCursor"] = encodePageCursor(store.TemplateCursor{Entity: last.Entity, Name: last.Name, Version: last.Version})
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getTemplate(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	t, err := a.store.GetDataTemplate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get data template failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, templateToView(t, true))
}

func templateToView(t store.DataTemplate, withDefaults bool) templateView {
	v := templateView{ID: t.ID, Entity: t.Entity, Name: t.Name, Version: t.Version, Checksum: t.Checksum, DefaultCount: t.DefaultCount, CreatedAt: t.CreatedAt}
	if withDefaults {
		v.Defaults = t.Defaults
	}
	return v
}

func (a *API) runLedger(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	afterKey := ""
	if v := r.URL.Query().Get("cursor"); v != "" {
		if afterKey, ok = ledgerCursor(v); !ok {
			fail(w, r, cursorProblem())
			return
		}
	}
	templated := r.URL.Query().Get("templated")
	if templated != "" && templated != "true" && templated != "false" {
		fail(w, r, validation(map[string]string{"templated": "must be true or false"}))
		return
	}
	rows, err := a.store.RunLedger(r.Context(), chi.URLParam(r, "id"), templated == "true", afterKey, limit+1)
	if err != nil {
		a.logFailure(r, "list run ledger failed", err)
		fail(w, r, err)
		return
	}
	rows, more := trimPage(rows, limit)
	body := map[string]any{"items": rows}
	if more {
		body["nextCursor"] = encodePageCursor(map[string]string{"k": rows[limit-1].SourceKey})
	}
	httpx.JSON(w, http.StatusOK, body)
}
