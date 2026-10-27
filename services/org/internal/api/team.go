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

var (
	teamMemberKinds = []string{"user", "party"}
	teamMemberRoles = []string{"member", "lead"}
)

type addTeamMemberRequest struct {
	MemberKind string  `json:"memberKind"`
	MemberID   string  `json:"memberId"`
	MemberRole string  `json:"memberRole"`
	ValidFrom  string  `json:"validFrom"`
	ValidTo    *string `json:"validTo"`
}

type updateTeamRequest struct {
	Version *int `json:"version"`
	store.TeamPatch
}

type endTeamMemberRequest struct {
	ValidTo *string `json:"validTo"`
}

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
	r.With(authz.RequireDuty(dutyOperatingUnit)).Patch(teams+"/{id}", teamRoute(a.updateTeam))
	r.With(authz.RequireDuty(dutyOperatingUnit)).Post(teams+"/{id}/members", teamRoute(a.addTeamMember))
	r.With(authz.RequireDuty(dutyOperatingUnit)).Post(teams+"/{id}/members/{memberId}:end", teamRoute(a.endTeamMember))
	r.With(authz.RequireDuty(dutyOperatingUnit)).Delete(teams+"/{id}/members/{memberId}", teamRoute(a.removeTeamMember))
	r.With(authz.RequireDuty(dutyRead)).Get(teams+"/{id}/members", teamRoute(a.listTeamMembers))
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
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
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
	created, inserted, err := a.store.CreateTeamType(r.Context(), in, idemKey)
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
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
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
	created, inserted, err := a.store.CreateTeam(r.Context(), in, idemKey)
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
	f := store.TeamFilter{TeamTypeID: strings.ToLower(q.Get("teamTypeId")), OperatingUnitID: strings.ToLower(q.Get("operatingUnitId")), Status: q.Get("status"), MemberID: strings.ToLower(q.Get("memberId"))}
	if f.TeamTypeID != "" && !canonicalUUID(f.TeamTypeID) {
		badQuery(w, r, "teamTypeId", "must be a UUID")
		return
	}
	if f.OperatingUnitID != "" && !canonicalUUID(f.OperatingUnitID) {
		badQuery(w, r, "operatingUnitId", "must be a UUID")
		return
	}
	if f.MemberID != "" && !canonicalUUID(f.MemberID) {
		badQuery(w, r, "memberId", "must be a UUID")
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

func (a *API) updateTeam(w http.ResponseWriter, r *http.Request, id string) {
	if refuseScopedCaller(w, r, "update teams") {
		return
	}
	var req updateTeamRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
		if trimmed == "" || utf8.RuneCountInString(trimmed) > teamNameLength {
			fields["name"] = fmt.Sprintf("must be 1 to %d characters", teamNameLength)
		}
	}
	if req.Description != nil && utf8.RuneCountInString(*req.Description) > teamDescriptionLength {
		fields["description"] = fmt.Sprintf("must be at most %d characters", teamDescriptionLength)
	}
	if req.OperatingUnitID != nil {
		lower := strings.ToLower(*req.OperatingUnitID)
		req.OperatingUnitID = &lower
		if lower != "" && !canonicalUUID(lower) {
			fields["operatingUnitId"] = "must be a UUID"
		}
	}
	if req.EffectiveTo != nil && *req.EffectiveTo != "" {
		if _, ok := parseDate(*req.EffectiveTo); !ok {
			fields["effectiveTo"] = "must be a date as YYYY-MM-DD"
		}
	}
	if req.Status != nil && !validStatus(*req.Status) {
		fields["status"] = "must be active or inactive"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	team, err := a.store.UpdateTeam(r.Context(), id, *req.Version, req.TeamPatch)
	if err != nil {
		slog.Error("update team failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if team.Version != *req.Version {
		a.teamUpdated.Add(1)
	}
	httpx.JSON(w, http.StatusOK, team)
}

func teamMemberRowID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "memberId")
	if !canonicalUUID(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTeamMemberNotFound, "team member not found"))
		return "", false
	}
	return id, true
}

func (a *API) endTeamMember(w http.ResponseWriter, r *http.Request, teamID string) {
	if refuseScopedCaller(w, r, "end team members") {
		return
	}
	memberRowID, ok := teamMemberRowID(w, r)
	if !ok {
		return
	}
	var req endTeamMemberRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var validTo time.Time
	valid := false
	if req.ValidTo != nil {
		validTo, valid = parseDate(*req.ValidTo)
	}
	if !valid {
		validationFailed(w, r, map[string]string{"validTo": "is required and must be a date as YYYY-MM-DD"})
		return
	}
	member, changed, err := a.store.EndTeamMember(r.Context(), teamID, memberRowID, validTo)
	if err != nil {
		slog.Error("end team member failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.teamMemberEnded.Add(1)
	}
	httpx.JSON(w, http.StatusOK, member)
}

func (a *API) removeTeamMember(w http.ResponseWriter, r *http.Request, teamID string) {
	if refuseScopedCaller(w, r, "remove team members") {
		return
	}
	memberRowID, ok := teamMemberRowID(w, r)
	if !ok {
		return
	}
	if err := a.store.RemoveTeamMember(r.Context(), teamID, memberRowID); err != nil {
		slog.Error("remove team member failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.teamMemberRemoved.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) addTeamMember(w http.ResponseWriter, r *http.Request, teamID string) {
	if refuseScopedCaller(w, r, "add team members") {
		return
	}
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
		return
	}
	var req addTeamMemberRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.TeamMember{MemberKind: req.MemberKind, MemberID: strings.ToLower(req.MemberID), MemberRole: req.MemberRole, ValidFrom: req.ValidFrom, ValidTo: req.ValidTo}
	if in.MemberRole == "" {
		in.MemberRole = teamMemberRoles[0]
	}
	fields := map[string]string{}
	if !slices.Contains(teamMemberKinds, in.MemberKind) {
		fields["memberKind"] = "must be user or party"
	}
	if !canonicalUUID(in.MemberID) {
		fields["memberId"] = "must be a UUID"
	}
	if !slices.Contains(teamMemberRoles, in.MemberRole) {
		fields["memberRole"] = "must be member or lead"
	}
	from, fromOK := parseDate(in.ValidFrom)
	if !fromOK {
		fields["validFrom"] = "must be a date as YYYY-MM-DD"
	}
	if in.ValidTo != nil {
		to, toOK := parseDate(*in.ValidTo)
		if !toOK {
			fields["validTo"] = "must be a date as YYYY-MM-DD"
		} else if fromOK && to.Before(from) {
			fields["validTo"] = "must not be before validFrom"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, inserted, err := a.store.AddTeamMember(r.Context(), teamID, in, a.now(), idemKey)
	if err != nil {
		slog.Error("add team member failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if inserted {
		a.teamMemberAdded.Add(1)
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listTeamMembers(w http.ResponseWriter, r *http.Request, teamID string) {
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if after != "" && !canonicalUUID(after) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor is invalid"))
		return
	}
	q := r.URL.Query()
	f := store.TeamMemberFilter{MemberKind: q.Get("memberKind"), MemberRole: q.Get("memberRole")}
	if f.MemberKind != "" && !slices.Contains(teamMemberKinds, f.MemberKind) {
		badQuery(w, r, "memberKind", "must be user or party")
		return
	}
	if f.MemberRole != "" && !slices.Contains(teamMemberRoles, f.MemberRole) {
		badQuery(w, r, "memberRole", "must be member or lead")
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
	page, err := a.store.ListTeamMembers(r.Context(), teamID, f, after, limit)
	if err != nil {
		slog.Error("list team members failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := struct {
		Items      []store.TeamMember `json:"items"`
		NextCursor string             `json:"nextCursor,omitempty"`
	}{Items: page.Items}
	if page.NextID != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextID))
	}
	httpx.JSON(w, http.StatusOK, body)
}
