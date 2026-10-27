package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

type descriptionTemplateRequest struct {
	Scope         string            `json:"scope"`
	LegalEntityID *string           `json:"legalEntityId"`
	SourceType    string            `json:"sourceType"`
	Language      string            `json:"language"`
	Template      string            `json:"template"`
	FieldBindings map[string]string `json:"fieldBindings"`
}

func templateNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, "LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND", "description template not found")
}

func (a *API) listDescriptionTemplates(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListDescriptionTemplates(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getDescriptionTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, templateNotFound())
		return
	}
	d, err := a.store.GetDescriptionTemplate(r.Context(), id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// putDescriptionTemplate creates (201) or replaces (200) a default description
// template (FR-11.17). The legal-entity scope rule is enforced by the store.
func (a *API) putDescriptionTemplate(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	var req descriptionTemplateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	switch req.Scope {
	case store.ScopeTenant:
		if req.LegalEntityID != nil {
			fields["legalEntityId"] = "must be omitted for tenant scope"
		}
	case store.ScopeLegalEntity:
		if req.LegalEntityID == nil || !ids.Valid(*req.LegalEntityID) {
			fields["legalEntityId"] = "is required for legal_entity scope and must be a UUID"
		}
	default:
		fields["scope"] = "must be tenant or legal_entity"
	}
	if !domain.ValidSourceType(req.SourceType) {
		fields["sourceType"] = "must be lowercase letters, digits and underscores, starting with a letter"
	}
	if req.Language == "" {
		req.Language = domain.DefaultDescriptionLanguage
	}
	if !domain.ValidLanguage(req.Language) {
		fields["language"] = "must be a language tag such as en or en-US"
	}
	for k, v := range domain.ValidateDescriptionTemplate(req.Template, req.FieldBindings) {
		fields[k] = v
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	d, created, err := a.store.PutDescriptionTemplate(r.Context(), id, store.DescriptionTemplate{Scope: req.Scope,
		LegalEntityID: req.LegalEntityID, SourceType: req.SourceType, Language: req.Language, Template: req.Template,
		FieldBindings: req.FieldBindings})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, d)
}
