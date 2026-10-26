package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const unitsPath = "/v1/org/uom/units"

func httpGet(h http.Handler) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestUnits_ReadRoutes(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
		notBody    string
	}{
		{"list", unitsPath, "org.read", true, http.StatusOK, `"code":"kg"`, ""},
		{"list by class", unitsPath + "?class=mass", "org.read", true, http.StatusOK, `"code":"lb"`, `"code":"C"`},
		{"unknown class", unitsPath + "?class=weight", "org.read", true, http.StatusBadRequest, "COM-0001", ""},
		{"unknown status", unitsPath + "?status=gone", "org.read", true, http.StatusBadRequest, "COM-0001", ""},
		{"no tenant", unitsPath, "", false, http.StatusUnauthorized, "AUTH-0001", ""},
		{"list without duty", unitsPath, "org.reference.write", true, http.StatusForbidden, "", ""},
		{"get", unitsPath + "/kg", "org.read", true, http.StatusOK, `"decimalPrecision":3,"system":true,"status":"active"`, ""},
		{"get unknown", unitsPath + "/furlong", "org.read", true, http.StatusNotFound, "ORG-UOM-UNIT-NOT-FOUND", ""},
		{"get is case sensitive", unitsPath + "/KG", "org.read", true, http.StatusNotFound, "ORG-UOM-UNIT-NOT-FOUND", ""},
		{"get without duty", unitsPath + "/kg", "", true, http.StatusForbidden, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || (tc.notBody != "" && strings.Contains(rec.Body.String(), tc.notBody)) {
				t.Fatalf("got %d %s; want %d containing %q without %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody, tc.notBody)
			}
		})
	}
}

func TestUnits_StoreErrorIs500(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: http.ErrAbortHandler})
	for _, path := range []string{unitsPath, unitsPath + "/kg"} {
		if rec := do(r, http.MethodGet, path, "", "org.read", true); rec.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s = %d; want 500", path, rec.Code)
		}
	}
}

func TestUnits_Create(t *testing.T) {
	const valid = `{"code":"bx","name":"Box","class":"each","decimalPrecision":0}`
	tests := []struct {
		name       string
		body       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"valid", valid, referenceDuties, http.StatusCreated, `"code":"bx","name":"Box","class":"each","decimalPrecision":0,"system":false,"status":"active"`},
		{"name is trimmed", `{"code":"bx","name":"  Box  ","class":"each","decimalPrecision":2}`, referenceDuties, http.StatusCreated, `"name":"Box"`},
		{"code with a space", `{"code":"k g","name":"Box","class":"each","decimalPrecision":0}`, referenceDuties, http.StatusBadRequest, `"code":`},
		{"code too long", `{"code":"abcdefghijk","name":"Box","class":"each","decimalPrecision":0}`, referenceDuties, http.StatusBadRequest, `"code":`},
		{"empty name", `{"code":"bx","name":" ","class":"each","decimalPrecision":0}`, referenceDuties, http.StatusBadRequest, `"name":`},
		{"long name", `{"code":"bx","name":"` + strings.Repeat("n", 121) + `","class":"each","decimalPrecision":0}`, referenceDuties, http.StatusBadRequest, `"name":`},
		{"unknown class", `{"code":"bx","name":"Box","class":"weight","decimalPrecision":0}`, referenceDuties, http.StatusBadRequest, `"class":`},
		{"precision above range", `{"code":"bx","name":"Box","class":"each","decimalPrecision":7}`, referenceDuties, http.StatusBadRequest, `"decimalPrecision":`},
		{"negative precision", `{"code":"bx","name":"Box","class":"each","decimalPrecision":-1}`, referenceDuties, http.StatusBadRequest, `"decimalPrecision":`},
		{"missing precision", `{"code":"bx","name":"Box","class":"each"}`, referenceDuties, http.StatusBadRequest, `"decimalPrecision":`},
		{"string precision", `{"code":"bx","name":"Box","class":"each","decimalPrecision":"3"}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"fractional precision", `{"code":"bx","name":"Box","class":"each","decimalPrecision":2.5}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"unknown field", `{"code":"bx","name":"Box","class":"each","decimalPrecision":0,"system":true}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"duplicate of a system unit", `{"code":"kg","name":"Box","class":"each","decimalPrecision":0}`, referenceDuties, http.StatusConflict, "ORG-UOM-CODE-DUPLICATE"},
		{"without the write duty", valid, "org.read", http.StatusForbidden, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, a := newRouter(f)
			rec := do(r, http.MethodPost, unitsPath, tc.body, tc.duties, true, "Idempotency-Key", "key-1")
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			created := rec.Code == http.StatusCreated
			if created && f.unitCreateKey != "key-1" {
				t.Fatalf("idempotency key = %q; want key-1", f.unitCreateKey)
			}
			metrics := httpGet(a.Metrics())
			if want := map[bool]string{true: "org_uom_units_created_total 1", false: "org_uom_units_created_total 0"}[created]; !strings.Contains(metrics, want) {
				t.Fatalf("metrics lack %q:\n%s", want, metrics)
			}
		})
	}
}

func TestUnits_CreateAndPatchRefuseLegalEntityScopedCaller(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, unitsPath, `{"code":"bx","name":"Box","class":"each","decimalPrecision":0}`},
		{http.MethodPatch, unitsPath + "/lb", `{"status":"inactive"}`},
	} {
		rec := do(r, tc.method, tc.path, tc.body, referenceDuties, true, "X-Passage-Legal-Entity", testkit.TenantID(2))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
			t.Fatalf("%s = %d %s; want 403 AUTH-0003", tc.method, rec.Code, rec.Body.String())
		}
	}
}

func TestUnits_CreateStoreErrorIs500(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: http.ErrAbortHandler})
	rec := do(r, http.MethodPost, unitsPath, `{"code":"bx","name":"Box","class":"each","decimalPrecision":0}`, referenceDuties, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d; want 500", rec.Code)
	}
}

func TestUnits_Patch(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"deactivate", unitsPath + "/lb", `{"status":"inactive"}`, referenceDuties, http.StatusOK, `"status":"inactive"`},
		{"unknown status", unitsPath + "/lb", `{"status":"gone"}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"empty status", unitsPath + "/lb", `{}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"unknown code", unitsPath + "/furlong", `{"status":"inactive"}`, referenceDuties, http.StatusNotFound, "ORG-UOM-UNIT-NOT-FOUND"},
		{"without the write duty", unitsPath + "/lb", `{"status":"inactive"}`, "org.read", http.StatusForbidden, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, a := newRouter(&fakeStore{})
			rec := do(r, http.MethodPatch, tc.path, tc.body, tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			want := "org_reference_status_changed_total 0"
			if rec.Code == http.StatusOK {
				want = "org_reference_status_changed_total 1"
			}
			if metrics := httpGet(a.Metrics()); !strings.Contains(metrics, want) {
				t.Fatalf("metrics lack %q:\n%s", want, metrics)
			}
		})
	}
}

func TestUnits_DeactivationBlocksConvertAndReactivationRestoresIt(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	const convert = "/v1/org/uom/convert?from=lb&to=kg&qty=1"
	if rec := do(r, http.MethodGet, convert, "", "", true); rec.Code != http.StatusOK {
		t.Fatalf("before = %d %s; want 200", rec.Code, rec.Body.String())
	}
	do(r, http.MethodPatch, unitsPath+"/lb", `{"status":"inactive"}`, referenceDuties, true)
	if rec := do(r, http.MethodGet, convert, "", "", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ORG-UOM-NO-PATH") {
		t.Fatalf("inactive = %d %s; want 422 ORG-UOM-NO-PATH", rec.Code, rec.Body.String())
	}
	do(r, http.MethodPatch, unitsPath+"/lb", `{"status":"active"}`, referenceDuties, true)
	if rec := do(r, http.MethodGet, convert, "", "", true); rec.Code != http.StatusOK {
		t.Fatalf("reactivated = %d %s; want 200", rec.Code, rec.Body.String())
	}
}

func TestUnits_HaveNoDeleteRoute(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	if rec := do(r, http.MethodDelete, unitsPath+"/lb", "", referenceDuties, true); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE = %d; want 404 or 405", rec.Code)
	}
}
