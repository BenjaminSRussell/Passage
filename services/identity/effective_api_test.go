package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/identity"
)

type permissionRow struct {
	Resource    string `json:"resource"`
	Method      string `json:"method"`
	Allow       bool   `json:"allow"`
	Duty        string `json:"duty"`
	DutyMissing string `json:"dutyMissing"`
}

type reportBody struct {
	UserID       string          `json:"userId"`
	Duties       []string        `json:"duties"`
	IgnoredRoles []string        `json:"ignoredRoles"`
	Permissions  []permissionRow `json:"permissions"`
}

func newEffectiveRouter(t *testing.T, app *pgxpool.Pool) http.Handler {
	t.Helper()
	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	catalogue, err := identity.LoadCatalogue(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	identity.NewEffectivePermissionsAPI(app, catalogue).Register(r)
	return r
}

func getReport(router http.Handler, tenantID, path string, duties ...string) *httptest.ResponseRecorder {
	return getReportScoped(router, tenantID, "", path, append([]string{tenant.DutyAllScope}, duties...)...)
}

func getReportScoped(router http.Handler, tenantID, legalEntityID, path string, duties ...string) *httptest.ResponseRecorder {
	held := map[string]struct{}{}
	for _, d := range duties {
		held[d] = struct{}{}
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(tenant.With(req.Context(), tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntityID, UserID: "caller", Duties: held}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func grantRole(t *testing.T, app *pgxpool.Pool, tenantID, role, legalEntityID, window string) {
	t.Helper()
	if err := asTenant(t, app, tenantID, "*", func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `insert into identity_role_assignments (tenant_id, assignment_id, user_id, role, legal_entity_id, valid_from, valid_to)
select $1::uuid, gen_random_uuid(), $2::uuid, $3, nullif($4, '')::uuid, `+window, tenantID, userID, role, legalEntityID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func decodeReport(t *testing.T, rec *httptest.ResponseRecorder) (reportBody, map[string]permissionRow) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body reportBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	rows := map[string]permissionRow{}
	for _, row := range body.Permissions {
		rows[row.Method+" "+row.Resource] = row
	}
	return body, rows
}

func TestEffectivePermissionsEndpoint(t *testing.T) {
	app, tenantA, tenantB := setup(t)
	router := newEffectiveRouter(t, app)
	const view = "View effective permissions"
	path := "/v1/identity/users/" + userID + "/effective-permissions"

	grantRole(t, app, tenantA, "View vendors", "", "now() - interval '1 day', null")
	grantRole(t, app, tenantA, "Maintain users", "", "now() - interval '2 days', now() - interval '1 day'")
	grantRole(t, app, tenantA, "View audit log", legalA, "now() - interval '1 day', null")
	grantRole(t, app, tenantB, "Maintain vendors", "", "now() - interval '1 day', null")

	t.Run("tenant-wide active duties only, unknown roles ignored", func(t *testing.T) {
		body, rows := decodeReport(t, getReport(router, tenantA, path, view))
		if len(body.Duties) != 1 || body.Duties[0] != "View vendors" {
			t.Fatalf("duties = %v, want [View vendors]", body.Duties)
		}
		if len(body.IgnoredRoles) != 1 || body.IgnoredRoles[0] != "tenant_admin" {
			t.Fatalf("ignoredRoles = %v", body.IgnoredRoles)
		}
		if got := rows["GET /v1/vendors{,/**}"]; !got.Allow || got.Duty != "View vendors" {
			t.Errorf("GET vendors = %+v, want allowed by View vendors", got)
		}
		if got := rows["POST /v1/vendors{,/**}"]; got.Allow || got.DutyMissing != "Maintain vendors" {
			t.Errorf("POST vendors = %+v, want denied missing Maintain vendors", got)
		}
		if got := rows["GET /v1/identity/users{,/**}"]; got.Allow || got.DutyMissing != "Maintain users" {
			t.Errorf("expired Maintain users must not grant: %+v", got)
		}
		if got := rows["GET /v1/audit{,/**}"]; got.Allow {
			t.Errorf("legal-entity-scoped duty leaked into tenant-wide report: %+v", got)
		}
	})

	t.Run("legal entity adds its scoped duties", func(t *testing.T) {
		body, rows := decodeReport(t, getReport(router, tenantA, path+"?legalEntityId="+legalA, view))
		if len(body.Duties) != 2 || len(body.IgnoredRoles) != 2 {
			t.Fatalf("duties = %v ignored = %v, want View audit log, View vendors and ap_clerk, tenant_admin", body.Duties, body.IgnoredRoles)
		}
		if got := rows["GET /v1/audit{,/**}"]; !got.Allow || got.Duty != "View audit log" {
			t.Errorf("GET audit = %+v", got)
		}
		_, other := decodeReport(t, getReport(router, tenantA, path+"?legalEntityId="+legalB, view))
		if other["GET /v1/audit{,/**}"].Allow {
			t.Error("legal entity B must not inherit legal entity A duty")
		}
	})

	t.Run("report is tenant-isolated", func(t *testing.T) {
		body, _ := decodeReport(t, getReport(router, tenantB, path, view))
		if len(body.Duties) != 1 || body.Duties[0] != "Maintain vendors" {
			t.Fatalf("tenant B duties = %v", body.Duties)
		}
		if rec := getReport(router, testkit.TenantID(3), path, view); rec.Code != http.StatusNotFound {
			t.Fatalf("unknown tenant status = %d, want 404", rec.Code)
		}
	})

	t.Run("Maintain users holder is accepted like OPA", func(t *testing.T) {
		if rec := getReport(router, tenantA, path, "Maintain users"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("caller scoped to a legal entity is refused rather than shown a partial report", func(t *testing.T) {
		rec := getReportScoped(router, tenantA, legalA, path, view)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
		}
		if rec := getReportScoped(router, tenantA, "", path, view); rec.Code != http.StatusForbidden {
			t.Fatalf("unscoped caller without all-scope duty: status = %d, want 403", rec.Code)
		}
		if rec := getReport(router, tenantA, path, view); rec.Code != http.StatusOK {
			t.Fatalf("all-entity caller status = %d", rec.Code)
		}
	})

	t.Run("malformed ids carry field errors", func(t *testing.T) {
		rec := getReport(router, tenantA, "/v1/identity/users/nope/effective-permissions?legalEntityId=x", view)
		var problem struct {
			Errors map[string]string `json:"errors"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest || problem.Errors["id"] != "must be a UUID" || problem.Errors["legalEntityId"] != "must be a UUID" {
			t.Fatalf("status = %d, errors = %v", rec.Code, problem.Errors)
		}
	})

	t.Run("denied and malformed requests", func(t *testing.T) {
		for name, tc := range map[string]struct {
			rec  *httptest.ResponseRecorder
			want int
		}{
			"no duty":            {getReport(router, tenantA, path), http.StatusForbidden},
			"wrong duty":         {getReport(router, tenantA, path, "View vendors"), http.StatusForbidden},
			"bad user id":        {getReport(router, tenantA, "/v1/identity/users/not-a-uuid/effective-permissions", view), http.StatusBadRequest},
			"bad legal entity":   {getReport(router, tenantA, path+"?legalEntityId=x", view), http.StatusBadRequest},
			"unknown user":       {getReport(router, tenantA, "/v1/identity/users/20000000-0000-0000-0000-0000000000ff/effective-permissions", view), http.StatusNotFound},
			"no verified tenant": {getReport(router, "", path, view), http.StatusUnauthorized},
		} {
			if tc.rec.Code != tc.want {
				t.Errorf("%s: status = %d, want %d", name, tc.rec.Code, tc.want)
			}
		}
	})
}
