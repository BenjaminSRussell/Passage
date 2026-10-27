package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

func unprocessable(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func pageFields(r *http.Request, fields map[string]string) {
	_, _, err := pageParams(r)
	var problem *httpx.Problem
	if !errors.As(err, &problem) {
		return
	}
	field := "cursor"
	if strings.HasPrefix(problem.Detail, "limit") {
		field = "limit"
	}
	fields[field] = problem.Detail
}

func listFilterFields(r *http.Request, names ...string) (map[string]string, map[string]string) {
	q := r.URL.Query()
	fields, values := map[string]string{}, map[string]string{}
	for _, name := range names {
		values[name] = q.Get(name)
		checkUUID(name, values[name], fields)
	}
	pageFields(r, fields)
	return fields, values
}

func (a *API) listResources(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields, v := listFilterFields(r, "siteId", "groupId", "capabilityId")
	resourceType := r.URL.Query().Get("type")
	if resourceType != "" && !resourceTypes[resourceType] {
		fields["type"] = "must be machine, tool, labor, vendor or location"
	}
	if len(fields) > 0 {
		unprocessable(w, r, fields)
		return
	}
	f := store.ResourceFilter{SiteID: v["siteId"], Type: resourceType, GroupID: v["groupId"], CapabilityID: v["capabilityId"]}
	listPage(a, w, r, "list resources", &a.resourceFailed, func(ctx context.Context, after string, limit int) (store.Page[store.ResourceRecord], error) {
		return a.store.ListResources(ctx, f, after, limit)
	}, bodyFromResource)
}

func (a *API) listResourceGroups(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields, v := listFilterFields(r, "siteId")
	if len(fields) > 0 {
		unprocessable(w, r, fields)
		return
	}
	listPage(a, w, r, "list resource groups", &a.resourceFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.ResourceGroup], error) {
		return a.store.ListResourceGroups(ctx, v["siteId"], after, limit)
	}, func(g domain.ResourceGroup) groupBody {
		return groupBody{ID: g.ID, Code: g.Code, Name: g.Name, SiteID: g.SiteID, CalendarID: g.CalendarID}
	})
}

func (a *API) listCapabilities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields, _ := listFilterFields(r)
	if len(fields) > 0 {
		unprocessable(w, r, fields)
		return
	}
	listPage(a, w, r, "list capabilities", &a.resourceFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.Capability], error) {
		return a.store.ListCapabilities(ctx, after, limit)
	}, func(c domain.Capability) capabilityBody { return capabilityBody{ID: c.ID, Code: c.Code, Name: c.Name} })
}
