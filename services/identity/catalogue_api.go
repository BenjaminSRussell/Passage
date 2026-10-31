package identity

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
)

const (
	codeDutyNotFound = "IDN-DUTY-NOT-FOUND"
	codeRoleNotFound = "IDN-ROLE-NOT-FOUND"
)

// CatalogueAPI serves the read-only role and duty catalogue. Until roles are first-class (issue #83), a
// role is an assignable duty: its name is the duty name that identity_role_assignments.role holds.
type CatalogueAPI struct {
	catalogue *Catalogue
}

func NewCatalogueAPI(catalogue *Catalogue) *CatalogueAPI {
	return &CatalogueAPI{catalogue: catalogue}
}

func (a *CatalogueAPI) Register(r chi.Router) {
	guard := requireAnyDuty(dutyMaintainSecurityRoles)
	r.With(guard).Get("/v1/authz/roles", a.listRoles)
	r.With(guard).Get("/v1/authz/roles/{name}", a.getRole)
	r.With(guard).Get("/v1/authz/duties", a.listDuties)
	r.With(guard).Get("/v1/authz/duties/{name}", a.getDuty)
}

type dutySummary struct {
	Name            string `json:"name"`
	ServiceOnly     bool   `json:"serviceOnly"`
	PermissionCount int    `json:"permissionCount"`
}

type dutyDetail struct {
	Name        string           `json:"name"`
	ServiceOnly bool             `json:"serviceOnly"`
	Permissions []DutyPermission `json:"permissions"`
}

type roleSummary struct {
	Name   string   `json:"name"`
	Duties []string `json:"duties"`
}

type roleDetail struct {
	Name   string       `json:"name"`
	Duties []dutyDetail `json:"duties"`
}

func matchesQuery(name, q string) bool {
	return q == "" || strings.Contains(strings.ToLower(name), strings.ToLower(q))
}

func catalogueQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	if _, ok := principal(w, r); !ok {
		return "", false
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > maxQuery {
		invalidFields(w, r, "invalid catalogue filter", map[string]string{"q": "must be at most 100 characters"})
		return "", false
	}
	return q, true
}

func pathName(r *http.Request) string {
	name := chi.URLParam(r, "name")
	if decoded, err := url.PathUnescape(name); err == nil {
		return decoded
	}
	return name
}

func (a *CatalogueAPI) listDuties(w http.ResponseWriter, r *http.Request) {
	q, ok := catalogueQuery(w, r)
	if !ok {
		return
	}
	serviceOnly := r.URL.Query().Get("serviceOnly")
	if serviceOnly != "" && serviceOnly != "true" && serviceOnly != "false" {
		invalidFields(w, r, "invalid catalogue filter", map[string]string{"serviceOnly": "must be true or false"})
		return
	}
	items := []dutySummary{}
	for _, d := range a.catalogue.Duties() {
		if matchesQuery(d.Name, q) && (serviceOnly == "" || (serviceOnly == "true") == d.ServiceOnly) {
			items = append(items, dutySummary{Name: d.Name, ServiceOnly: d.ServiceOnly, PermissionCount: countMethods(d.Permissions)})
		}
	}
	httpx.JSON(w, http.StatusOK, page[dutySummary]{Items: items})
}

func countMethods(permissions []DutyPermission) int {
	n := 0
	for _, p := range permissions {
		n += len(p.Methods)
	}
	return n
}

func (a *CatalogueAPI) getDuty(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	d, found := a.catalogue.Duty(pathName(r))
	if !found {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeDutyNotFound, "duty not found"))
		return
	}
	httpx.JSON(w, http.StatusOK, dutyDetail(d))
}

func (a *CatalogueAPI) listRoles(w http.ResponseWriter, r *http.Request) {
	q, ok := catalogueQuery(w, r)
	if !ok {
		return
	}
	items := []roleSummary{}
	for _, d := range a.catalogue.Duties() {
		if !d.ServiceOnly && matchesQuery(d.Name, q) {
			items = append(items, roleSummary{Name: d.Name, Duties: []string{d.Name}})
		}
	}
	httpx.JSON(w, http.StatusOK, page[roleSummary]{Items: items})
}

func (a *CatalogueAPI) getRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	d, found := a.catalogue.Duty(pathName(r))
	if !found || d.ServiceOnly {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeRoleNotFound, "role not found"))
		return
	}
	httpx.JSON(w, http.StatusOK, roleDetail{Name: d.Name, Duties: []dutyDetail{dutyDetail(d)}})
}
