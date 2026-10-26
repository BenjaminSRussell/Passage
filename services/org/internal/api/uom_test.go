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

const (
	conversionsPath = "/v1/org/uom/conversions"
	gToLb           = `{"fromCode":"g","toCode":"lb","factor":"0.5"}`
)

func TestConversions_List(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
		notBody    string
	}{
		{"item shape", "", "org.read", true, http.StatusOK, `"fromCode":"kg","toCode":"g","class":"mass","factor":"1000","offset":"0","inverseFactor":"0.001","inverseOffset":"0","roundTrip":true,"system":true,"productId":null,"status":"active"`, ""},
		{"offset inverse", "", "org.read", true, http.StatusOK, `"inverseFactor":"0.555555555556","inverseOffset":"-17.777777777778"`, ""},
		{"by class", "?class=mass", "org.read", true, http.StatusOK, `"fromCode":"lb"`, `"fromCode":"C"`},
		{"by unit on either side", "?unit=kg", "org.read", true, http.StatusOK, `"toCode":"kg"`, `"fromCode":"C"`},
		{"unit is case sensitive", "?unit=KG", "org.read", true, http.StatusOK, `{"items":[]}`, ""},
		{"unknown class", "?class=bogus", "org.read", true, http.StatusBadRequest, `"class"`, ""},
		{"unknown status", "?status=gone", "org.read", true, http.StatusBadRequest, `"status"`, ""},
		{"no tenant", "", "", false, http.StatusUnauthorized, "AUTH-0001", ""},
		{"without duty", "", "org.reference.write", true, http.StatusForbidden, "AUTH-0003", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodGet, conversionsPath+tc.query, "", tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || (tc.notBody != "" && strings.Contains(rec.Body.String(), tc.notBody)) {
				t.Fatalf("got %d %s; want %d containing %q without %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody, tc.notBody)
			}
		})
	}
}

func TestConversions_ListReportsInactiveUnitsAndFiltersByStatus(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	do(r, http.MethodPatch, unitsPath+"/lb", `{"status":"inactive"}`, referenceDuties, true)
	active := do(r, http.MethodGet, conversionsPath+"?status=active", "", "org.read", true)
	inactive := do(r, http.MethodGet, conversionsPath+"?status=inactive", "", "org.read", true)
	if strings.Contains(active.Body.String(), `"fromCode":"lb"`) || !strings.Contains(active.Body.String(), `"fromCode":"kg"`) {
		t.Fatalf("active list = %s; want kg rows without lb", active.Body)
	}
	if !strings.Contains(inactive.Body.String(), `"fromCode":"lb"`) || !strings.Contains(inactive.Body.String(), `"status":"inactive"`) || strings.Contains(inactive.Body.String(), `"fromCode":"kg"`) {
		t.Fatalf("inactive list = %s; want only lb rows", inactive.Body)
	}
}

func TestConversions_ListStoreErrorIs500(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: http.ErrAbortHandler})
	rec := do(r, http.MethodGet, conversionsPath, "", "org.read", true)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "abort") {
		t.Fatalf("got %d %s; want 500 without detail", rec.Code, rec.Body)
	}
}

func TestConversions_Create(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"valid", gToLb, referenceDuties, http.StatusCreated, `"fromCode":"g","toCode":"lb","class":"mass","factor":"0.5","offset":"0","inverseFactor":"2","inverseOffset":"0","roundTrip":true,"system":false,"productId":null,"status":"active"`},
		{"valid offset", `{"fromCode":"g","toCode":"lb","factor":"2","offset":"-17.5"}`, referenceDuties, http.StatusCreated, `"offset":"-17.5"`},
		{"null offset defaults", `{"fromCode":"g","toCode":"lb","factor":"2","offset":null}`, referenceDuties, http.StatusCreated, `"offset":"0"`},
		{"factor zero", `{"fromCode":"g","toCode":"lb","factor":"0"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor negative", `{"fromCode":"g","toCode":"lb","factor":"-1"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor text", `{"fromCode":"g","toCode":"lb","factor":"abc"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor exponent", `{"fromCode":"g","toCode":"lb","factor":"1e999999999"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor thirteen decimals", `{"fromCode":"g","toCode":"lb","factor":"1.0000000000001"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor seventeen integer digits", `{"fromCode":"g","toCode":"lb","factor":"12345678901234567"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor sixteen integer digits and twelve decimals", `{"fromCode":"g","toCode":"lb","factor":"1234567890123456.123456789012"}`, referenceDuties, http.StatusCreated, `"id"`},
		{"factor missing", `{"fromCode":"g","toCode":"lb"}`, referenceDuties, http.StatusBadRequest, `"factor"`},
		{"factor as number", `{"fromCode":"g","toCode":"lb","factor":2}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"offset text", `{"fromCode":"g","toCode":"lb","factor":"2","offset":"abc"}`, referenceDuties, http.StatusBadRequest, `"offset"`},
		{"offset empty", `{"fromCode":"g","toCode":"lb","factor":"2","offset":""}`, referenceDuties, http.StatusBadRequest, `"offset"`},
		{"same unit", `{"fromCode":"g","toCode":"g","factor":"2"}`, referenceDuties, http.StatusBadRequest, `"toCode"`},
		{"missing from", `{"toCode":"g","factor":"2"}`, referenceDuties, http.StatusBadRequest, `"fromCode"`},
		{"unknown field", `{"fromCode":"g","toCode":"lb","factor":"2","system":true}`, referenceDuties, http.StatusBadRequest, "COM-0001"},
		{"unknown unit", `{"fromCode":"g","toCode":"furlong","factor":"2"}`, referenceDuties, http.StatusNotFound, "ORG-UOM-UNIT-NOT-FOUND"},
		{"class mismatch", `{"fromCode":"kg","toCode":"C","factor":"2"}`, referenceDuties, http.StatusUnprocessableEntity, "ORG-UOM-CLASS-MISMATCH"},
		{"reverse of a system pair", `{"fromCode":"g","toCode":"kg","factor":"0.001"}`, referenceDuties, http.StatusConflict, "ORG-UOM-CONVERSION-DUPLICATE"},
		{"without the write duty", gToLb, "org.read", http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, a := newRouter(f)
			rec := do(r, http.MethodPost, conversionsPath, tc.body, tc.duties, true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if got, want := metric(a, "org_uom_conversions_created_total"), map[bool]string{true: "1", false: "0"}[rec.Code == http.StatusCreated]; got != want {
				t.Fatalf("counter = %s; want %s", got, want)
			}
		})
	}
}

func TestConversions_CreateRefusesWithoutTenantAndLegalEntityScopedCaller(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	if rec := do(r, http.MethodPost, conversionsPath, gToLb, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a tenant = %d; want 401", rec.Code)
	}
	rec := do(r, http.MethodPost, conversionsPath, gToLb, referenceDuties, true, "X-Passage-Legal-Entity", testkit.TenantID(2))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || f.conversionCreates != 0 {
		t.Fatalf("scoped caller = %d %s; want 403 AUTH-0003 and no write", rec.Code, rec.Body)
	}
}

func TestConversions_CreateStoreErrorIs500(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: http.ErrAbortHandler})
	if rec := do(r, http.MethodPost, conversionsPath, gToLb, referenceDuties, true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d; want 500", rec.Code)
	}
}

func TestConversions_CreateRefusesInactiveUnit(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	do(r, http.MethodPatch, unitsPath+"/lb", `{"status":"inactive"}`, referenceDuties, true)
	rec := do(r, http.MethodPost, conversionsPath, gToLb, referenceDuties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "ORG-UOM-UNIT-INACTIVE") {
		t.Fatalf("got %d %s; want 422 ORG-UOM-UNIT-INACTIVE", rec.Code, rec.Body)
	}
}

func TestConversions_CreateReplayWritesAndCountsOnce(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	first := do(r, http.MethodPost, conversionsPath, gToLb, referenceDuties, true, "Idempotency-Key", "k1")
	replay := do(r, http.MethodPost, conversionsPath, gToLb, referenceDuties, true, "Idempotency-Key", "k1")
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() || f.conversionCreates != 1 {
		t.Fatalf("first %d %s, replay %d %s, writes %d; want identical 201s and one write", first.Code, first.Body, replay.Code, replay.Body, f.conversionCreates)
	}
	reuse := do(r, http.MethodPost, conversionsPath, `{"fromCode":"g","toCode":"lb","factor":"0.6"}`, referenceDuties, true, "Idempotency-Key", "k1")
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("reuse = %d %s; want 422 idempotency_key_reuse", reuse.Code, reuse.Body)
	}
	if got := metric(a, "org_uom_conversions_created_total"); got != "1" {
		t.Fatalf("counter = %s; want 1", got)
	}
}

func TestConversions_CreatedConversionIsUsedByConvert(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	do(r, http.MethodPost, conversionsPath, gToLb, referenceDuties, true)
	for path, want := range map[string]string{
		"/v1/org/uom/convert?from=g&to=lb&qty=2": `"quantity":"1"`,
		"/v1/org/uom/convert?from=lb&to=g&qty=3": `"quantity":"6"`,
	} {
		if rec := do(r, http.MethodGet, path, "", "", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET %s = %d %s; want %s", path, rec.Code, rec.Body, want)
		}
	}
}
