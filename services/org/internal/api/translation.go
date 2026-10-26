package api

import (
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const languageRule = "must be a language tag such as fr or fr-CA"

type translationOwner struct {
	collection, ownerType, writeDuty string
	route                            func(func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc
}

var translationOwners = []translationOwner{
	{"legal-entities", "legal_entity", dutyLegalEntity, legalEntityRoute},
	{"operating-units", "operating_unit", dutyOperatingUnit, operatingUnitRoute},
	{"sites", "site", dutySite, siteRoute},
	{"warehouses", "warehouse", dutySite, warehouseRoute},
}

type putTranslationRequest struct {
	Name string `json:"name"`
}

func (a *API) registerTranslations(r chi.Router) {
	for _, o := range translationOwners {
		base := "/v1/org/" + o.collection + "/{id}"
		write := r.With(authz.RequireDuty(o.writeDuty))
		read := r.With(authz.RequireDuty(dutyRead))
		write.Put(base+"/translations/{language}", o.route(a.putTranslation(o.ownerType)))
		write.Delete(base+"/translations/{language}", o.route(a.deleteTranslation(o.ownerType)))
		read.Get(base+"/translations", o.route(a.listTranslations(o.ownerType)))
		read.Get(base+"/name", o.route(a.resolveName(o.ownerType)))
	}
}

func (a *API) translationFailed(w http.ResponseWriter, r *http.Request, op string, err error) {
	slog.Error(op+" failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, err)
}

func (a *API) putTranslation(ownerType string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		var req putTranslationRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := map[string]string{}
		language := chi.URLParam(r, "language")
		if !languageTag.MatchString(language) {
			fields["language"] = languageRule
		}
		name := strings.TrimSpace(req.Name)
		if n := utf8.RuneCountInString(name); n == 0 || n > maxNameLength {
			fields["name"] = "must be 1 to 120 characters"
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		row, created, err := a.store.PutNameTranslation(r.Context(), ownerType, id, language, name)
		if err != nil {
			a.translationFailed(w, r, "put name translation", err)
			return
		}
		if row.Changed {
			a.translationsPut.Add(1)
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		httpx.JSON(w, status, row)
	}
}

func (a *API) deleteTranslation(ownerType string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		language := chi.URLParam(r, "language")
		if !languageTag.MatchString(language) {
			validationFailed(w, r, map[string]string{"language": languageRule})
			return
		}
		if err := a.store.DeleteNameTranslation(r.Context(), ownerType, id, language); err != nil {
			a.translationFailed(w, r, "delete name translation", err)
			return
		}
		a.translationsDeleted.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) listTranslations(ownerType string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		items, err := a.store.ListNameTranslations(r.Context(), ownerType, id)
		if err != nil {
			a.translationFailed(w, r, "list name translations", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func (a *API) resolveName(ownerType string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		var tag string
		if q := r.URL.Query(); q.Has("language") {
			if tag = q.Get("language"); !languageTag.MatchString(tag) {
				badQuery(w, r, "language", languageRule)
				return
			}
		}
		resolution, err := a.store.ResolveOrgUnitName(r.Context(), ownerType, id, tag)
		if err != nil {
			a.translationFailed(w, r, "resolve organization name", err)
			return
		}
		httpx.JSON(w, http.StatusOK, resolution)
	}
}
