package api_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	teamTypesPath = "/v1/org/team-types"
	teamDuties    = "org.read,org.operating-unit.write"
	teamTypeID1   = "00000000-0000-0000-0000-000000000001"
)

func seededTeamTypes() *fakeStore {
	return &fakeStore{teamTypes: []store.TeamType{
		{ID: teamTypeID1, Code: "AUD", Name: "Auditors", MemberKinds: []string{"user"}},
		{ID: "00000000-0000-0000-0000-000000000002", Code: "STEW", Name: "Stewards", MemberKinds: []string{"user", "party"}},
		{ID: "00000000-0000-0000-0000-000000000003", Code: "VEND", Name: "Vendors", MemberKinds: []string{"party"}},
	}}
}

func TestTeamTypes(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"create upper-cases code", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"stew","name":" Stewards ","memberKinds":["user","party"]}`, teamDuties, true, nil, http.StatusCreated, `"code":"STEW","name":"Stewards","memberKinds":["user","party"]`},
		{"create duplicate code", seededTeamTypes(), http.MethodPost, teamTypesPath, `{"code":"stew","name":"x","memberKinds":["user"]}`, teamDuties, true, nil, http.StatusConflict, "ORG-TEAM-TYPE-CODE-DUPLICATE"},
		{"create empty kinds", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":[]}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKinds"`},
		{"create absent kinds", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x"}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKinds"`},
		{"create null kinds", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":null}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKinds"`},
		{"create unknown kind", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":["robot"]}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKinds"`},
		{"create repeated kind", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":["user","user"]}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKinds"`},
		{"create three kinds", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":["user","party","user"]}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKinds"`},
		{"create string for array", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":"user"}`, teamDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"create bad code", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"-","name":"x","memberKinds":["user"]}`, teamDuties, true, nil, http.StatusBadRequest, `"code"`},
		{"create blank name", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"  ","memberKinds":["user"]}`, teamDuties, true, nil, http.StatusBadRequest, `"name"`},
		{"create 61-char name", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"` + strings.Repeat("n", 61) + `","memberKinds":["user"]}`, teamDuties, true, nil, http.StatusBadRequest, `"name"`},
		{"create 60-char name", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"` + strings.Repeat("n", 60) + `","memberKinds":["user"]}`, teamDuties, true, nil, http.StatusCreated, `"code":"STEW"`},
		{"create unknown field", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":["user"],"membershipRule":"x"}`, teamDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"create without duty", &fakeStore{}, http.MethodPost, teamTypesPath, `{}`, "org.read", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"create without tenant", &fakeStore{}, http.MethodPost, teamTypesPath, `{}`, "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"create legal-entity scoped caller", &fakeStore{}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":["user"]}`, teamDuties, true, scoped, http.StatusForbidden, "AUTH-0003"},
		{"create store error hides internals", &fakeStore{err: errors.New("pq: secret")}, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"x","memberKinds":["user"]}`, teamDuties, true, nil, http.StatusInternalServerError, "COM-"},
		{"list filters by member kind", seededTeamTypes(), http.MethodGet, teamTypesPath + "?memberKind=party", "", teamDuties, true, nil, http.StatusOK, `"code":"VEND"`},
		{"list bad member kind", seededTeamTypes(), http.MethodGet, teamTypesPath + "?memberKind=x", "", teamDuties, true, nil, http.StatusBadRequest, `"memberKind"`},
		{"list bad member kind case", seededTeamTypes(), http.MethodGet, teamTypesPath + "?memberKind=User", "", teamDuties, true, nil, http.StatusBadRequest, `"memberKind"`},
		{"list bad cursor", seededTeamTypes(), http.MethodGet, teamTypesPath + "?cursor=%25", "", teamDuties, true, nil, http.StatusBadRequest, "cursor"},
		{"list limit over max", seededTeamTypes(), http.MethodGet, teamTypesPath + "?limit=101", "", teamDuties, true, nil, http.StatusBadRequest, "limit"},
		{"list without duty", seededTeamTypes(), http.MethodGet, teamTypesPath, "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"list scoped caller may read", seededTeamTypes(), http.MethodGet, teamTypesPath, "", teamDuties, true, scoped, http.StatusOK, `"code":"AUD"`},
		{"get ok", seededTeamTypes(), http.MethodGet, teamTypesPath + "/" + teamTypeID1, "", teamDuties, true, nil, http.StatusOK, `"code":"AUD"`},
		{"get unknown", seededTeamTypes(), http.MethodGet, teamTypesPath + "/00000000-0000-0000-0000-0000000000ff", "", teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-TYPE-NOT-FOUND"},
		{"get malformed", seededTeamTypes(), http.MethodGet, teamTypesPath + "/nope", "", teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-TYPE-NOT-FOUND"},
		{"get without duty", seededTeamTypes(), http.MethodGet, teamTypesPath + "/" + teamTypeID1, "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("response leaks store error: %s", rec.Body)
			}
		})
	}
}

func TestTeamTypesPageByCursorInCodeOrder(t *testing.T) {
	r, _ := newRouter(seededTeamTypes())
	var codes []string
	path := teamTypesPath + "?limit=2"
	for pages := 0; path != ""; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		rec := do(r, http.MethodGet, path, "", teamDuties, true)
		var page struct {
			Items      []store.TeamType `json:"items"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("page = %d %s %v", rec.Code, rec.Body, err)
		}
		for _, item := range page.Items {
			codes = append(codes, item.Code)
		}
		path = ""
		if page.NextCursor != "" {
			path = teamTypesPath + "?limit=2&cursor=" + page.NextCursor
		}
	}
	if got := strings.Join(codes, ","); got != "AUD,STEW,VEND" {
		t.Fatalf("codes = %s, want AUD,STEW,VEND", got)
	}
}

func TestTeamTypesCursorReachesStore(t *testing.T) {
	f := seededTeamTypes()
	r, _ := newRouter(f)
	cursor := base64.RawURLEncoding.EncodeToString([]byte("AUD"))
	do(r, http.MethodGet, teamTypesPath+"?memberKind=user&cursor="+cursor, "", teamDuties, true)
	if f.teamTypeAfter != "AUD" || f.teamTypeFilter.MemberKind != "user" {
		t.Fatalf("store saw after=%q filter=%+v", f.teamTypeAfter, f.teamTypeFilter)
	}
}

func TestTeamTypeReplayIsNotCountedAndKeyReuseIsRefused(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	body := `{"code":"STEW","name":"Stewards","memberKinds":["user"]}`
	first := do(r, http.MethodPost, teamTypesPath, body, teamDuties, true, "Idempotency-Key", "k1")
	replay := do(r, http.MethodPost, teamTypesPath, body, teamDuties, true, "Idempotency-Key", "k1")
	reuse := do(r, http.MethodPost, teamTypesPath, `{"code":"STEW","name":"Other","memberKinds":["user"]}`, teamDuties, true, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() {
		t.Fatalf("first = %d %s, replay = %d %s", first.Code, first.Body, replay.Code, replay.Body)
	}
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("reuse = %d %s", reuse.Code, reuse.Body)
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_team_types_created_total 1\n") {
		t.Fatalf("metrics = %s", rec.Body)
	}
}

const (
	teamsPath = "/v1/org/teams"
	teamBody  = `{"code":"mdstew","name":"MD stewards","teamTypeId":"` + teamTypeID1 + `","effectiveFrom":"2026-01-01","effectiveTo":"2026-12-31"}`
)

func TestTeams(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	withBody := func(old, replacement string) string { return strings.Replace(teamBody, old, replacement, 1) }
	seeded := func() *fakeStore {
		return &fakeStore{teams: []store.Team{{ID: teamTypeID1, Code: "AUD", Name: "Auditors", EffectiveFrom: "2026-01-01"}}}
	}
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"create", &fakeStore{}, http.MethodPost, teamsPath, teamBody, teamDuties, true, nil, http.StatusCreated, `"code":"MDSTEW","name":"MD stewards","teamTypeId":"` + teamTypeID1 + `","effectiveFrom":"2026-01-01","effectiveTo":"2026-12-31","status":"active","version":1`},
		{"create duplicate", seeded(), http.MethodPost, teamsPath, withBody("mdstew", "aud"), teamDuties, true, nil, http.StatusConflict, "ORG-TEAM-CODE-DUPLICATE"},
		{"create effectiveTo before effectiveFrom", &fakeStore{}, http.MethodPost, teamsPath, withBody("2026-12-31", "2025-12-31"), teamDuties, true, nil, http.StatusBadRequest, `"effectiveTo"`},
		{"create open-ended", &fakeStore{}, http.MethodPost, teamsPath, `{"code":"OPEN","name":"x","teamTypeId":"` + teamTypeID1 + `","effectiveFrom":"2026-01-01"}`, teamDuties, true, nil, http.StatusCreated, `"code":"OPEN"`},
		{"create 501-char description", &fakeStore{}, http.MethodPost, teamsPath, withBody(`"name"`, `"description":"`+strings.Repeat("d", 501)+`","name"`), teamDuties, true, nil, http.StatusBadRequest, `"description"`},
		{"create 500-char description", &fakeStore{}, http.MethodPost, teamsPath, withBody(`"name"`, `"description":"`+strings.Repeat("d", 500)+`","name"`), teamDuties, true, nil, http.StatusCreated, `"code":"MDSTEW"`},
		{"create non-UUID team type", &fakeStore{}, http.MethodPost, teamsPath, withBody(teamTypeID1, "nope"), teamDuties, true, nil, http.StatusBadRequest, `"teamTypeId"`},
		{"create absent team type", &fakeStore{}, http.MethodPost, teamsPath, `{"code":"OPEN","name":"x","effectiveFrom":"2026-01-01"}`, teamDuties, true, nil, http.StatusBadRequest, `"teamTypeId"`},
		{"create non-UUID operating unit", &fakeStore{}, http.MethodPost, teamsPath, withBody(`"name"`, `"operatingUnitId":"nope","name"`), teamDuties, true, nil, http.StatusBadRequest, `"operatingUnitId"`},
		{"create empty operating unit", &fakeStore{}, http.MethodPost, teamsPath, withBody(`"name"`, `"operatingUnitId":"","name"`), teamDuties, true, nil, http.StatusBadRequest, `"operatingUnitId"`},
		{"create impossible effectiveFrom", &fakeStore{}, http.MethodPost, teamsPath, withBody("2026-01-01", "2026-13-01"), teamDuties, true, nil, http.StatusBadRequest, `"effectiveFrom"`},
		{"create absent effectiveFrom", &fakeStore{}, http.MethodPost, teamsPath, `{"code":"OPEN","name":"x","teamTypeId":"` + teamTypeID1 + `"}`, teamDuties, true, nil, http.StatusBadRequest, `"effectiveFrom"`},
		{"create unparsable effectiveTo", &fakeStore{}, http.MethodPost, teamsPath, withBody("2026-12-31", "soon"), teamDuties, true, nil, http.StatusBadRequest, `"effectiveTo"`},
		{"create bad code", &fakeStore{}, http.MethodPost, teamsPath, withBody("mdstew", "-"), teamDuties, true, nil, http.StatusBadRequest, `"code"`},
		{"create blank name", &fakeStore{}, http.MethodPost, teamsPath, withBody("MD stewards", "  "), teamDuties, true, nil, http.StatusBadRequest, `"name"`},
		{"create membershipRule refused", &fakeStore{}, http.MethodPost, teamsPath, withBody(`"name"`, `"membershipRule":"x","name"`), teamDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"create without duty", &fakeStore{}, http.MethodPost, teamsPath, teamBody, "org.read", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"create without tenant", &fakeStore{}, http.MethodPost, teamsPath, teamBody, "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"create legal-entity scoped caller", &fakeStore{}, http.MethodPost, teamsPath, teamBody, teamDuties, true, scoped, http.StatusForbidden, "AUTH-0003"},
		{"create store error hides internals", &fakeStore{err: errors.New("pq: secret")}, http.MethodPost, teamsPath, teamBody, teamDuties, true, nil, http.StatusInternalServerError, "COM-"},
		{"list", seeded(), http.MethodGet, teamsPath + "?asOf=2026-12-31&status=active&teamTypeId=" + teamTypeID1 + "&operatingUnitId=" + teamTypeID1, "", teamDuties, true, nil, http.StatusOK, `"code":"AUD"`},
		{"list scoped caller may read", seeded(), http.MethodGet, teamsPath, "", teamDuties, true, scoped, http.StatusOK, `"code":"AUD"`},
		{"list without duty", seeded(), http.MethodGet, teamsPath, "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"list bad asOf", seeded(), http.MethodGet, teamsPath + "?asOf=2026-13-01", "", teamDuties, true, nil, http.StatusBadRequest, `"asOf"`},
		{"list bad status", seeded(), http.MethodGet, teamsPath + "?status=bogus", "", teamDuties, true, nil, http.StatusBadRequest, `"status"`},
		{"list bad teamTypeId", seeded(), http.MethodGet, teamsPath + "?teamTypeId=nope", "", teamDuties, true, nil, http.StatusBadRequest, `"teamTypeId"`},
		{"list bad operatingUnitId", seeded(), http.MethodGet, teamsPath + "?operatingUnitId=nope", "", teamDuties, true, nil, http.StatusBadRequest, `"operatingUnitId"`},
		{"list bad cursor", seeded(), http.MethodGet, teamsPath + "?cursor=%25", "", teamDuties, true, nil, http.StatusBadRequest, "cursor"},
		{"list limit over max", seeded(), http.MethodGet, teamsPath + "?limit=101", "", teamDuties, true, nil, http.StatusBadRequest, "limit"},
		{"get ok", seeded(), http.MethodGet, teamsPath + "/" + teamTypeID1, "", teamDuties, true, nil, http.StatusOK, `"code":"AUD"`},
		{"get unknown", seeded(), http.MethodGet, teamsPath + "/00000000-0000-0000-0000-0000000000ff", "", teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-NOT-FOUND"},
		{"get malformed", seeded(), http.MethodGet, teamsPath + "/nope", "", teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-NOT-FOUND"},
		{"get without duty", seeded(), http.MethodGet, teamsPath + "/" + teamTypeID1, "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("response leaks store error: %s", rec.Body)
			}
		})
	}
}

func TestTeamsListFiltersReachStore(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	do(r, http.MethodGet, teamsPath+"?asOf=2026-12-31&status=inactive&teamTypeId="+strings.ToUpper(teamTypeID1)+"&operatingUnitId="+teamTypeID1, "", teamDuties, true)
	if f.teamFilter.AsOf == nil || f.teamFilter.AsOf.Format("2006-01-02") != "2026-12-31" || f.teamFilter.Status != "inactive" || f.teamFilter.TeamTypeID != teamTypeID1 || f.teamFilter.OperatingUnitID != teamTypeID1 {
		t.Fatalf("store saw %+v", f.teamFilter)
	}
}

func TestTeamsPageByCursorInCodeOrder(t *testing.T) {
	f := &fakeStore{teams: []store.Team{{Code: "A1"}, {Code: "B1"}, {Code: "C1"}}}
	r, _ := newRouter(f)
	var codes []string
	path := teamsPath + "?limit=2"
	for pages := 0; path != ""; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		rec := do(r, http.MethodGet, path, "", teamDuties, true)
		var page struct {
			Items      []store.Team `json:"items"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("page = %d %s %v", rec.Code, rec.Body, err)
		}
		for _, item := range page.Items {
			codes = append(codes, item.Code)
		}
		path = ""
		if page.NextCursor != "" {
			path = teamsPath + "?limit=2&cursor=" + page.NextCursor
		}
	}
	if got := strings.Join(codes, ","); got != "A1,B1,C1" {
		t.Fatalf("codes = %s, want A1,B1,C1", got)
	}
}

func TestTeamReplayIsNotCountedAndKeyReuseIsRefused(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	first := do(r, http.MethodPost, teamsPath, teamBody, teamDuties, true, "Idempotency-Key", "k1")
	replay := do(r, http.MethodPost, teamsPath, teamBody, teamDuties, true, "Idempotency-Key", "k1")
	reuse := do(r, http.MethodPost, teamsPath, strings.Replace(teamBody, "MD stewards", "Other", 1), teamDuties, true, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() {
		t.Fatalf("first = %d %s, replay = %d %s", first.Code, first.Body, replay.Code, replay.Body)
	}
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("reuse = %d %s", reuse.Code, reuse.Body)
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_teams_created_total 1\n") {
		t.Fatalf("metrics = %s", rec.Body)
	}
}

const (
	memberID1  = "00000000-0000-0000-0000-0000000000a1"
	memberBody = `{"memberKind":"user","memberId":"` + memberID1 + `","validFrom":"2026-02-01","validTo":"2026-06-30"}`
)

func TestTeamMembers(t *testing.T) {
	scoped := []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}
	membersPath := teamsPath + "/" + teamTypeID1 + "/members"
	withBody := func(old, replacement string) string { return strings.Replace(memberBody, old, replacement, 1) }
	seeded := func() *fakeStore {
		return &fakeStore{teams: []store.Team{{ID: teamTypeID1, Code: "AUD"}}, teamMembers: []store.TeamMember{{ID: "00000000-0000-0000-0000-000000000001", MemberKind: "user", MemberRole: "lead"}}}
	}
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"add", http.MethodPost, membersPath, memberBody, teamDuties, true, nil, http.StatusCreated, `"teamId":"` + teamTypeID1 + `","memberKind":"user","memberId":"` + memberID1 + `","memberRole":"member","validFrom":"2026-02-01","validTo":"2026-06-30","source":"manual"`},
		{"add lower-cases member id and keeps lead", http.MethodPost, membersPath, `{"memberKind":"party","memberId":"` + strings.ToUpper(memberID1) + `","memberRole":"lead","validFrom":"2026-02-01"}`, teamDuties, true, nil, http.StatusCreated, `"memberId":"` + memberID1 + `","memberRole":"lead"`},
		{"add bad kind", http.MethodPost, membersPath, withBody(`"user"`, `"admin"`), teamDuties, true, nil, http.StatusBadRequest, `"memberKind"`},
		{"add absent kind", http.MethodPost, membersPath, `{"memberId":"` + memberID1 + `","validFrom":"2026-02-01"}`, teamDuties, true, nil, http.StatusBadRequest, `"memberKind"`},
		{"add non-UUID member", http.MethodPost, membersPath, withBody(memberID1, "nope"), teamDuties, true, nil, http.StatusBadRequest, `"memberId"`},
		{"add number for member id", http.MethodPost, membersPath, withBody(`"`+memberID1+`"`, `123`), teamDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"add bad role", http.MethodPost, membersPath, withBody(`"validFrom"`, `"memberRole":"boss","validFrom"`), teamDuties, true, nil, http.StatusBadRequest, `"memberRole"`},
		{"add impossible validFrom", http.MethodPost, membersPath, withBody("2026-02-01", "2026-13-01"), teamDuties, true, nil, http.StatusBadRequest, `"validFrom"`},
		{"add absent validFrom", http.MethodPost, membersPath, `{"memberKind":"user","memberId":"` + memberID1 + `"}`, teamDuties, true, nil, http.StatusBadRequest, `"validFrom"`},
		{"add unparsable validTo", http.MethodPost, membersPath, withBody("2026-06-30", "soon"), teamDuties, true, nil, http.StatusBadRequest, `"validTo"`},
		{"add validTo before validFrom", http.MethodPost, membersPath, withBody("2026-06-30", "2026-01-31"), teamDuties, true, nil, http.StatusBadRequest, `"validTo"`},
		{"add source refused", http.MethodPost, membersPath, withBody(`"validFrom"`, `"source":"rule","validFrom"`), teamDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"add unknown team", http.MethodPost, teamsPath + "/00000000-0000-0000-0000-0000000000ff/members", memberBody, teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-NOT-FOUND"},
		{"add malformed team", http.MethodPost, teamsPath + "/nope/members", memberBody, teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-NOT-FOUND"},
		{"add without duty", http.MethodPost, membersPath, memberBody, "org.read", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"add without tenant", http.MethodPost, membersPath, memberBody, "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"add legal-entity scoped caller", http.MethodPost, membersPath, memberBody, teamDuties, true, scoped, http.StatusForbidden, "AUTH-0003"},
		{"add store error hides internals", http.MethodPost, membersPath, memberBody, teamDuties, true, nil, http.StatusInternalServerError, "COM-"},
		{"list", http.MethodGet, membersPath + "?asOf=2026-03-01&memberKind=user&memberRole=lead", "", teamDuties, true, nil, http.StatusOK, `"memberRole":"lead"`},
		{"list scoped caller may read", http.MethodGet, membersPath, "", teamDuties, true, scoped, http.StatusOK, `"memberRole":"lead"`},
		{"list without duty", http.MethodGet, membersPath, "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"list bad asOf", http.MethodGet, membersPath + "?asOf=2026-13-01", "", teamDuties, true, nil, http.StatusBadRequest, `"asOf"`},
		{"list bad memberKind", http.MethodGet, membersPath + "?memberKind=x", "", teamDuties, true, nil, http.StatusBadRequest, `"memberKind"`},
		{"list bad memberRole", http.MethodGet, membersPath + "?memberRole=Lead", "", teamDuties, true, nil, http.StatusBadRequest, `"memberRole"`},
		{"list undecodable cursor", http.MethodGet, membersPath + "?cursor=%25", "", teamDuties, true, nil, http.StatusBadRequest, "cursor"},
		{"list non-UUID cursor", http.MethodGet, membersPath + "?cursor=" + base64.RawURLEncoding.EncodeToString([]byte("nope")), "", teamDuties, true, nil, http.StatusBadRequest, "cursor"},
		{"list unknown team", http.MethodGet, teamsPath + "/00000000-0000-0000-0000-0000000000ff/members", "", teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-NOT-FOUND"},
		{"list malformed team", http.MethodGet, teamsPath + "/nope/members", "", teamDuties, true, nil, http.StatusNotFound, "ORG-TEAM-NOT-FOUND"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := seeded()
			if tc.name == "add store error hides internals" {
				f.err = errors.New("pq: secret")
			}
			r, _ := newRouter(f)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("response leaks store error: %s", rec.Body)
			}
		})
	}
}

func TestTeamMemberAddPassesTheClockAndReplayIsNotCounted(t *testing.T) {
	f := &fakeStore{teams: []store.Team{{ID: teamTypeID1}}}
	r, a := newRouter(f)
	a.SetClock(func() time.Time { return time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC) })
	path := teamsPath + "/" + teamTypeID1 + "/members"
	first := do(r, http.MethodPost, path, memberBody, teamDuties, true, "Idempotency-Key", "k1")
	replay := do(r, http.MethodPost, path, memberBody, teamDuties, true, "Idempotency-Key", "k1")
	reuse := do(r, http.MethodPost, path, strings.Replace(memberBody, memberID1, "00000000-0000-0000-0000-0000000000a2", 1), teamDuties, true, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() {
		t.Fatalf("first = %d %s, replay = %d %s", first.Code, first.Body, replay.Code, replay.Body)
	}
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("reuse = %d %s", reuse.Code, reuse.Body)
	}
	if f.teamMemberToday.Format("2006-01-02") != "2026-03-01" {
		t.Fatalf("store saw today = %s", f.teamMemberToday)
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_team_members_added_total 1\n") {
		t.Fatalf("metrics = %s", rec.Body)
	}
}

func TestTeamMembersFiltersReachStoreAndPageByCursor(t *testing.T) {
	f := &fakeStore{teams: []store.Team{{ID: teamTypeID1}}, teamMembers: []store.TeamMember{
		{ID: "00000000-0000-0000-0000-000000000001"}, {ID: "00000000-0000-0000-0000-000000000002"}, {ID: "00000000-0000-0000-0000-000000000003"},
	}}
	r, _ := newRouter(f)
	base := teamsPath + "/" + teamTypeID1 + "/members"
	do(r, http.MethodGet, base+"?asOf=2026-03-01&memberKind=party&memberRole=lead", "", teamDuties, true)
	if f.teamMemberFilter.AsOf == nil || f.teamMemberFilter.AsOf.Format("2006-01-02") != "2026-03-01" || f.teamMemberFilter.MemberKind != "party" || f.teamMemberFilter.MemberRole != "lead" {
		t.Fatalf("store saw %+v", f.teamMemberFilter)
	}
	var ids []string
	path := base + "?limit=2"
	for pages := 0; path != ""; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		rec := do(r, http.MethodGet, path, "", teamDuties, true)
		var page struct {
			Items      []store.TeamMember `json:"items"`
			NextCursor string             `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("page = %d %s %v", rec.Code, rec.Body, err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ID[len(item.ID)-1:])
		}
		path = ""
		if page.NextCursor != "" {
			path = base + "?limit=2&cursor=" + page.NextCursor
		}
	}
	if got := strings.Join(ids, ","); got != "1,2,3" {
		t.Fatalf("ids = %s, want 1,2,3", got)
	}
}
