package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/domain"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

// maxTermMonths bounds a RATABLE template's term.
const maxTermMonths = 120

type templateRequest struct {
	Method     string `json:"method"`
	TermMonths *int   `json:"termMonths"`
}

type templateOut struct {
	ProductID  string `json:"productId"`
	Method     string `json:"method"`
	TermMonths *int   `json:"termMonths,omitempty"`
	UpdatedAt  string `json:"updatedAt"`
}

func toTemplateOut(t store.Template) templateOut {
	return templateOut{ProductID: t.ProductID, Method: t.Method, TermMonths: t.TermMonths, UpdatedAt: t.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")}
}

// putTemplate sets how a product's revenue is recognized when its contract is opened from a posted invoice.
func (a *API) putTemplate(w http.ResponseWriter, r *http.Request) {
	product := chi.URLParam(r, "id")
	var req templateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(product) {
		fields["productId"] = "must be a UUID"
	}
	switch req.Method {
	case domain.MethodPointInTime, domain.MethodMilestone, domain.MethodUsage:
		if req.TermMonths != nil {
			fields["termMonths"] = "only a RATABLE template has a term"
		}
	case domain.MethodRatable:
		if req.TermMonths == nil || *req.TermMonths < 1 || *req.TermMonths > maxTermMonths {
			fields["termMonths"] = "a RATABLE template needs a term of 1 to 120 months"
		}
	default:
		fields["method"] = "must be POINT_IN_TIME, RATABLE, MILESTONE or USAGE"
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	out, err := a.store.PutTemplate(r.Context(), store.Template{ProductID: product, Method: req.Method, TermMonths: req.TermMonths})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTemplateOut(out))
}

func (a *API) listTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListTemplates(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]templateOut, len(items))
	for i, t := range items {
		out[i] = toTemplateOut(t)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
