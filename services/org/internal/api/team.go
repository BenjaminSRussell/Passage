package api

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	teamTypes      = "/v1/org/team-types"
	teamNameLength = 60
)

var teamMemberKinds = []string{"user", "party"}

type createTeamTypeRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	MemberKinds []string `json:"memberKinds"`
}

func (a *API) registerTeams(r chi.Router) {
	r.With(authz.RequireDuty(dutyOperatingUnit)).Post(teamTypes, a.createTeamType)
	r.With(authz.RequireDuty(dutyRead)).Get(teamTypes, a.listTeamTypes)
	r.With(authz.RequireDuty(dutyRead)).Get(teamTypes+"/{id}", teamTypeRoute(a.getTeamType))
}

func validMemberKinds(kinds []string) bool {
	if len(kinds) == 0 || len(kinds) > len(teamMemberKinds) {
		return false
	}
	for i, kind := range kinds {
		if !slices.Contains(teamMemberKinds, kind) || slices.Contains(kinds[:i], kind) {
			return false
		}
	}
	return true
}

func (a *API) createTeamType(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || refuseScopedCaller(w, r, "create team types") {
		return
	}
	var req createTeamTypeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.TeamType{Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name), MemberKinds: req.MemberKinds}
	fields := map[string]string{}
	if !operatingUnitCode.MatchString(in.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if in.Name == "" || utf8.RuneCountInString(in.Name) > teamNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", teamNameLength)
	}
	if !validMemberKinds(in.MemberKinds) {
		fields["memberKinds"] = "must be one or two distinct values of user and party"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, inserted, err := a.store.CreateTeamType(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create team type failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.teamTypeCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listTeamTypes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := store.TeamTypeFilter{MemberKind: r.URL.Query().Get("memberKind")}
	if f.MemberKind != "" && !slices.Contains(teamMemberKinds, f.MemberKind) {
		badQuery(w, r, "memberKind", "must be user or party")
		return
	}
	page, err := a.store.ListTeamTypes(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list team types failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := struct {
		Items      []store.TeamType `json:"items"`
		NextCursor string           `json:"nextCursor,omitempty"`
	}{Items: page.Items}
	if page.NextCode != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextCode))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func teamTypeRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTeamTypeNotFound, "team type not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getTeamType(w http.ResponseWriter, r *http.Request, id string) {
	t, err := a.store.GetTeamType(r.Context(), id)
	if err != nil {
		slog.Error("get team type failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}
