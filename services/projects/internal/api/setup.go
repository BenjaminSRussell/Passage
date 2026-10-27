package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
)

// groupBody is the wire shape of PUT /v1/projects/groups/{groupId}.
type groupBody struct {
	ID                 string `json:"id,omitempty"`
	Name               string `json:"name"`
	DefaultProjectType string `json:"defaultProjectType,omitempty"`
}

// profileBody is the wire shape of a posting profile key and its accounts.
type profileBody struct {
	ProjectGroup           string `json:"projectGroup"`
	Category               string `json:"category"`
	LineProperty           string `json:"lineProperty"`
	CostAccount            string `json:"costAccount"`
	WIPAccount             string `json:"wipAccount"`
	AccruedRevenueAccount  string `json:"accruedRevenueAccount"`
	InvoicedRevenueAccount string `json:"invoicedRevenueAccount"`
	OnAccountAccount       string `json:"onAccountAccount"`
}

// profileView is a posting profile as returned by the API, with its version.
type profileView struct {
	profileBody
	Version int `json:"version"`
}

// resolveRequest addresses one concrete posting key (no wildcards).
type resolveRequest struct {
	ProjectGroup string `json:"projectGroup"`
	Category     string `json:"category"`
	LineProperty string `json:"lineProperty"`
}

type groupList struct {
	Items []domain.ProjectGroup `json:"items"`
}

type profileList struct {
	Items []profileView `json:"items"`
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListProjectGroups(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if items == nil {
		items = []domain.ProjectGroup{}
	}
	httpx.JSON(w, http.StatusOK, groupList{Items: items})
}

// putGroup creates a project group (no If-Match, 201) or replaces it (If-Match
// with its version, 200).
func (a *API) putGroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "groupId")
	var req groupBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.ID != "" && req.ID != id {
		httpx.Fail(w, r, validation(map[string]string{"id": "must match the path"}))
		return
	}
	g := domain.ProjectGroup{ID: id, Name: req.Name, DefaultProjectType: domain.ProjectType(req.DefaultProjectType)}
	if fields := domain.ValidateGroup(g); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	expected, ok := optionalIfMatch(w, r)
	if !ok {
		return
	}
	out, created, err := a.store.UpsertProjectGroup(r.Context(), g, expected)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, createdStatus(created), out)
}

func createdStatus(created bool) int {
	if created {
		return http.StatusCreated
	}
	return http.StatusOK
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListPostingProfiles(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := profileList{Items: make([]profileView, 0, len(items))}
	for _, p := range items {
		out.Items = append(out.Items, toProfileView(p))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// putProfile creates or replaces the posting profile whose key is in the body
// (If-Match as for putGroup). The key may be concrete, the group fallback
// (group, *, *) or the catch-all (*, *, *) (FR-41.5).
func (a *API) putProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req profileBody
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pr := fromProfileBody(req)
	if fields := domain.ValidateProfile(pr); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	expected, ok := optionalIfMatch(w, r)
	if !ok {
		return
	}
	out, created, err := a.store.UpsertPostingProfile(r.Context(), pr, expected)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, createdStatus(created), toProfileView(out))
}

// resolveProfile applies the FR-41.5 fallback order to a concrete key. A key
// with no row at any level fails with PROJ-POST-001 naming the missing key.
func (a *API) resolveProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req resolveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidGroupID(req.ProjectGroup) {
		fields["projectGroup"] = "must be a project group id"
	}
	if req.Category == "" || req.Category == domain.Wildcard || len(req.Category) > 20 {
		fields["category"] = "must be a category of 1 to 20 characters"
	}
	if req.LineProperty != domain.LineChargeable && req.LineProperty != domain.LineNonChargeable {
		fields["lineProperty"] = "must be CHARGEABLE or NON_CHARGEABLE"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	profiles, err := a.store.ListPostingProfiles(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pr, err := domain.Resolve(profiles, domain.ProfileKey{ProjectGroup: req.ProjectGroup, Category: req.Category, LineProperty: req.LineProperty})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toProfileView(pr))
}

func toProfileView(p domain.PostingProfile) profileView {
	return profileView{
		profileBody: profileBody{
			ProjectGroup: p.Key.ProjectGroup, Category: p.Key.Category, LineProperty: p.Key.LineProperty,
			CostAccount: p.CostAccount, WIPAccount: p.WIPAccount, AccruedRevenueAccount: p.AccruedRevenueAccount,
			InvoicedRevenueAccount: p.InvoicedRevenueAccount, OnAccountAccount: p.OnAccountAccount,
		},
		Version: p.Version,
	}
}

func fromProfileBody(b profileBody) domain.PostingProfile {
	return domain.PostingProfile{
		Key:                    domain.ProfileKey{ProjectGroup: b.ProjectGroup, Category: b.Category, LineProperty: b.LineProperty},
		CostAccount:            b.CostAccount,
		WIPAccount:             b.WIPAccount,
		AccruedRevenueAccount:  b.AccruedRevenueAccount,
		InvoicedRevenueAccount: b.InvoicedRevenueAccount,
		OnAccountAccount:       b.OnAccountAccount,
	}
}
