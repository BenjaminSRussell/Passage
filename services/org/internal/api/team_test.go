package api_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
