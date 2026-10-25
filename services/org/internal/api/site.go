package api

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	dutySite = "org.site.write"
	sites    = "/v1/org/sites"
)

var siteCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,9}$`)

type createSiteRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	TimeZone      *string `json:"timeZone"`
}

type updateSiteRequest struct {
	Version *int `json:"version"`
	store.SitePatch
}

type siteList struct {
	Items      []store.Site `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

func (a *API) registerSites(r chi.Router) {
	r.With(authz.RequireDuty(dutySite)).Post(sites, a.createSite)
	r.With(authz.RequireDuty(dutyRead)).Get(sites, a.listSites)
	r.With(authz.RequireDuty(dutyRead)).Get(sites+"/{id}", siteRoute(a.getSite))
	r.With(authz.RequireDuty(dutySite)).Patch(sites+"/{id}", siteRoute(a.updateSite))
}

func (a *API) createSite(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createSiteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.Site{LegalEntityID: req.LegalEntityID, Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name)}
	fields := validateShared(&in.Name, req.TimeZone, nil, nil)
	if req.TimeZone != nil {
		in.TimeZone = *req.TimeZone
	}
	if !ids.Valid(in.LegalEntityID) {
		fields["legalEntityId"] = "is required and must be a UUID"
	}
	if !siteCode.MatchString(in.Code) {
		fields["code"] = "must be 1 to 10 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	site, err := a.store.CreateSite(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create site failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.siteCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, site)
}

func (a *API) listSites(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, cursor, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var after store.SiteKey
	if cursor != "" {
		var ok bool
		if after.Code, after.ID, ok = strings.Cut(cursor, "/"); !ok || !ids.Valid(after.ID) {
			badQuery(w, r, "cursor", "is invalid")
			return
		}
	}
	q := r.URL.Query()
	f := store.SiteFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status")}
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"legalEntityId", "must be a UUID", f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID)},
		{"status", "must be active or inactive", f.Status != "" && !validStatus(f.Status)},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListSites(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list sites failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := siteList{Items: page.Items}
	if page.Next != nil {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Next.Code + "/" + page.Next.ID))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func siteRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSiteNotFound, "site not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getSite(w http.ResponseWriter, r *http.Request, id string) {
	site, err := a.store.GetSite(r.Context(), id)
	if err != nil {
		slog.Error("get site failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, site)
}

func (a *API) updateSite(w http.ResponseWriter, r *http.Request, id string) {
	var req updateSiteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	fields := validateShared(req.Name, req.TimeZone, nil, nil)
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.Status != nil && !validStatus(*req.Status) {
		fields["status"] = "must be active or inactive"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	site, err := a.store.UpdateSite(r.Context(), id, *req.Version, req.SitePatch)
	if err != nil {
		slog.Error("update site failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if site.Version != *req.Version {
		a.siteUpdated.Add(1)
	}
	httpx.JSON(w, http.StatusOK, site)
}
