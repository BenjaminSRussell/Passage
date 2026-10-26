package api

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	teamTypes             = "/v1/org/team-types"
	teams                 = "/v1/org/teams"
	teamNameLength        = 60
	teamDescriptionLength = 500
)

var teamMemberKinds = []string{"user", "party"}

type createTeamTypeRequest struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	MemberKinds []string `json:"memberKinds"`
}

type createTeamRequest struct {
	Code            string  `json:"code"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	TeamTypeID      string  `json:"teamTypeId"`
	OperatingUnitID *string `json:"operatingUnitId"`
	EffectiveFrom   string  `json:"effectiveFrom"`
	EffectiveTo     *string `json:"effectiveTo"`
}

func (a *API) registerTeams(r chi.Router) {
	r.With(authz.RequireDuty(dutyOperatingUnit)).Post(teams, a.createTeam)
	r.With(authz.RequireDuty(dutyRead)).Get(teams, a.listTeams)
	r.With(authz.RequireDuty(dutyRead)).Get(teams+"/{id}", teamRoute(a.getTeam))
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

func parseDate(value string) (time.Time, bool) {
	d, err := time.Parse(dateLayout, value)
	return d, err == nil
}

func (a *API) createTeam(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || refuseScopedCaller(w, r, "create teams") {
		return
	}
	var req createTeamRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.Team{Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name), Description: req.Description,
		TeamTypeID: strings.ToLower(req.TeamTypeID), OperatingUnitID: req.OperatingUnitID, EffectiveFrom: req.EffectiveFrom, EffectiveTo: req.EffectiveTo}
	if in.OperatingUnitID != nil {
		lower := strings.ToLower(*in.OperatingUnitID)
		in.OperatingUnitID = &lower
	}
	fields := map[string]string{}
	if !operatingUnitCode.MatchString(in.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if in.Name == "" || utf8.RuneCountInString(in.Name) > teamNameLength {
		fields["name"] = fmt.Sprintf("must be 1 to %d characters", teamNameLength)
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > teamDescriptionLength {
		fields["description"] = fmt.Sprintf("must be at most %d characters", teamDescriptionLength)
	}
	if !canonicalUUID(in.TeamTypeID) {
		fields["teamTypeId"] = "must be a UUID"
	}
	if in.OperatingUnitID != nil && !canonicalUUID(*in.OperatingUnitID) {
		fields["operatingUnitId"] = "must be a UUID"
	}
	from, fromOK := parseDate(in.EffectiveFrom)
	if !fromOK {
		fields["effectiveFrom"] = "must be a date as YYYY-MM-DD"
	}
	if in.EffectiveTo != nil {
		to, toOK := parseDate(*in.EffectiveTo)
		if !toOK {
			fields["effectiveTo"] = "must be a date as YYYY-MM-DD"
		} else if fromOK && to.Before(from) {
			fields["effectiveTo"] = "must not be before effectiveFrom"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, inserted, err := a.store.CreateTeam(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create team failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.teamCreated.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listTeams(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.TeamFilter{TeamTypeID: strings.ToLower(q.Get("teamTypeId")), OperatingUnitID: strings.ToLower(q.Get("operatingUnitId")), Status: q.Get("status")}
	if f.TeamTypeID != "" && !canonicalUUID(f.TeamTypeID) {
		badQuery(w, r, "teamTypeId", "must be a UUID")
		return
	}
	if f.OperatingUnitID != "" && !canonicalUUID(f.OperatingUnitID) {
		badQuery(w, r, "operatingUnitId", "must be a UUID")
		return
	}
	if f.Status != "" && !validStatus(f.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	if raw := q.Get("asOf"); raw != "" {
		asOf, ok := parseDate(raw)
		if !ok {
			badQuery(w, r, "asOf", "must be a date as YYYY-MM-DD")
			return
		}
		f.AsOf = &asOf
	}
	page, err := a.store.ListTeams(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list teams failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := struct {
		Items      []store.Team `json:"items"`
		NextCursor string       `json:"nextCursor,omitempty"`
	}{Items: page.Items}
	if page.NextCode != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextCode))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func teamRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !canonicalUUID(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTeamNotFound, "team not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getTeam(w http.ResponseWriter, r *http.Request, id string) {
	t, err := a.store.GetTeam(r.Context(), id)
	if err != nil {
		slog.Error("get team failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}
