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
	}, bodyFromGroup)
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
	}, bodyFromCapability)
}

func bodyFromGroup(g domain.ResourceGroup) groupBody {
	return groupBody{ID: g.ID, Code: g.Code, Name: g.Name, SiteID: g.SiteID, CalendarID: g.CalendarID}
}

func bodyFromCapability(c domain.Capability) capabilityBody {
	return capabilityBody{ID: c.ID, Code: c.Code, Name: c.Name}
}

func getByID[T, B any](a *API, w http.ResponseWriter, r *http.Request, operation string, fetch func(ctx context.Context, id string) (T, error), convert func(T) B) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	item, err := fetch(r.Context(), id)
	if err != nil {
		a.fail(w, r, operation, err, &a.resourceFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, convert(item))
}

func (a *API) getResourceGroup(w http.ResponseWriter, r *http.Request) {
	getByID(a, w, r, "get resource group", a.store.GetResourceGroup, bodyFromGroup)
}

func (a *API) getCapability(w http.ResponseWriter, r *http.Request) {
	getByID(a, w, r, "get capability", a.store.GetCapability, bodyFromCapability)
}

func (a *API) getCalendar(w http.ResponseWriter, r *http.Request) {
	getByID(a, w, r, "get calendar", a.store.GetCalendar, func(c store.Calendar) calendarView { return bodyFromCalendar(c, nil) })
}

func (a *API) listCalendars(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields, _ := listFilterFields(r)
	if len(fields) > 0 {
		unprocessable(w, r, fields)
		return
	}
	listPage(a, w, r, "list calendars", &a.resourceFailed, func(ctx context.Context, after string, limit int) (store.Page[store.Calendar], error) {
		return a.store.ListCalendars(ctx, after, limit)
	}, func(c store.Calendar) calendarView { return bodyFromCalendar(c, nil) })
}
