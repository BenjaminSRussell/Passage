package api_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const ownerID = "0190c9d2-7a4b-7c3e-8f10-000000000001"

var translationCollections = []struct {
	collection, ownerType, writeDuty, notFound string
}{
	{"legal-entities", "legal_entity", "org.legal-entity.write", store.CodeLegalEntityNotFound},
	{"operating-units", "operating_unit", "org.operating-unit.write", store.CodeOUNotFound},
	{"sites", "site", "org.site.write", store.CodeSiteNotFound},
	{"warehouses", "warehouse", "org.site.write", store.CodeWarehouseNotFound},
}

func TestTranslation_PutCreatesReplacesAndCountsOnlyChanges(t *testing.T) {
	for _, c := range translationCollections {
		t.Run(c.collection, func(t *testing.T) {
			r, a := newRouter(&fakeStore{})
			path := "/v1/org/" + c.collection + "/" + ownerID + "/translations/fr-CA"
			duties := "org.read," + c.writeDuty
			steps := []struct {
				body    string
				status  int
				counter string
			}{
				{`{"name":"Passage Fabrication"}`, http.StatusCreated, "1"},
				{`{"name":"  Passage Fabrication  "}`, http.StatusOK, "1"},
				{`{"name":"Passage Fabrication QC"}`, http.StatusOK, "2"},
			}
			for _, step := range steps {
				rec := do(r, http.MethodPut, path, step.body, duties, true)
				if rec.Code != step.status || !strings.Contains(rec.Body.String(), `"ownerType":"`+c.ownerType+`"`) || !strings.Contains(rec.Body.String(), `"language":"fr-CA"`) {
					t.Fatalf("body %s: status %d %s; want %d", step.body, rec.Code, rec.Body, step.status)
				}
				if got := metric(a, "org_name_translations_put_total"); got != step.counter {
					t.Fatalf("body %s: counter = %s; want %s", step.body, got, step.counter)
				}
			}
		})
	}
}

func TestTranslation_PutRefusesInvalidInput(t *testing.T) {
	tests := []struct {
		name, language, body, field string
	}{
		{"underscore tag", "fr_CA", `{"name":"x"}`, "language"},
		{"upper-case language", "FR", `{"name":"x"}`, "language"},
		{"three letters", "fra", `{"name":"x"}`, "language"},
		{"lower-case region", "fr-ca", `{"name":"x"}`, "language"},
		{"blank name", "fr", `{"name":""}`, "name"},
		{"whitespace name", "fr", `{"name":"   "}`, "name"},
		{"missing name", "fr", `{}`, "name"},
		{"121 runes", "fr", `{"name":"` + strings.Repeat("é", 121) + `"}`, "name"},
	}
	for _, c := range translationCollections {
		for _, tt := range tests {
			t.Run(c.collection+"/"+tt.name, func(t *testing.T) {
				f := &fakeStore{}
				r, _ := newRouter(f)
				rec := do(r, http.MethodPut, "/v1/org/"+c.collection+"/"+ownerID+"/translations/"+tt.language, tt.body, "org.read,"+c.writeDuty, true)
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"COM-0001"`) || !strings.Contains(rec.Body.String(), `"`+tt.field+`"`) || len(f.translations) != 0 {
					t.Fatalf("status %d body %s", rec.Code, rec.Body)
				}
			})
		}
	}
	r, _ := newRouter(&fakeStore{})
	path := "/v1/org/sites/" + ownerID + "/translations/fr"
	if rec := do(r, http.MethodPut, path, `{"name":"`+strings.Repeat("é", 120)+`"}`, "org.site.write", true); rec.Code != http.StatusCreated {
		t.Fatalf("120 multi-byte runes: %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{`, `{"name":5}`, `{"name":"x","extra":1}`} {
		if rec := do(r, http.MethodPut, path, body, "org.site.write", true); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status %d", body, rec.Code)
		}
	}
}

func TestTranslation_AuthorizationAndOwnerErrors(t *testing.T) {
	for _, c := range translationCollections {
		t.Run(c.collection, func(t *testing.T) {
			base := "/v1/org/" + c.collection + "/" + ownerID
			r, _ := newRouter(&fakeStore{})
			for _, route := range []struct{ method, path, duties string }{
				{http.MethodPut, base + "/translations/fr", "org.read"},
				{http.MethodDelete, base + "/translations/fr", "org.read"},
				{http.MethodGet, base + "/translations", c.writeDuty},
				{http.MethodGet, base + "/name", c.writeDuty},
			} {
				if rec := do(r, route.method, route.path, `{"name":"x"}`, route.duties, true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
					t.Fatalf("%s %s with %q: %d %s", route.method, route.path, route.duties, rec.Code, rec.Body)
				}
				if rec := do(r, route.method, route.path, `{"name":"x"}`, "", false); rec.Code != http.StatusUnauthorized {
					t.Fatalf("%s %s without a tenant: %d", route.method, route.path, rec.Code)
				}
			}
			malformed := "/v1/org/" + c.collection + "/not-a-uuid"
			for _, route := range []struct{ method, suffix string }{
				{http.MethodPut, "/translations/fr"}, {http.MethodDelete, "/translations/fr"}, {http.MethodGet, "/translations"}, {http.MethodGet, "/name"},
			} {
				rec := do(r, route.method, malformed+route.suffix, `{"name":"x"}`, "org.read,"+c.writeDuty, true)
				if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), c.notFound) {
					t.Fatalf("%s %s: %d %s; want 404 %s", route.method, route.suffix, rec.Code, rec.Body, c.notFound)
				}
			}
			f := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, c.notFound, "owner not found")}
			r, _ = newRouter(f)
			if rec := do(r, http.MethodGet, base+"/translations", "", "org.read", true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), c.notFound) {
				t.Fatalf("unknown owner: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestTranslation_StoreRefusalsReachTheClient(t *testing.T) {
	path := "/v1/org/sites/" + ownerID + "/translations/xx-XX"
	invalid := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeTranslationLanguageInvalid, "language is unknown or inactive")
	invalid.Errors = map[string]string{"language": "must be an active language"}
	r, a := newRouter(&fakeStore{err: invalid})
	rec := do(r, http.MethodPut, path, `{"name":"x"}`, "org.site.write", true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeTranslationLanguageInvalid) || !strings.Contains(rec.Body.String(), `"language"`) {
		t.Fatalf("invalid language: %d %s", rec.Code, rec.Body)
	}
	r, a = newRouter(&fakeStore{err: errors.New("boom")})
	if rec := do(r, http.MethodPut, path, `{"name":"x"}`, "org.site.write", true); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body)
	}
	if got := metric(a, "org_name_translations_put_total"); got != "0" {
		t.Fatalf("counter after failure = %s; want 0", got)
	}
}

func TestTranslation_ListIsOrderedAndEmptyIsNotAnError(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	base := "/v1/org/warehouses/" + ownerID
	if rec := do(r, http.MethodGet, base+"/translations", "", "org.read", true); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}
	for _, language := range []string{"fr-CA", "de", "fr"} {
		do(r, http.MethodPut, base+"/translations/"+language, `{"name":"n `+language+`"}`, "org.site.write", true)
	}
	body := do(r, http.MethodGet, base+"/translations", "", "org.read", true).Body.String()
	de, fr, frCA := strings.Index(body, `"language":"de"`), strings.Index(body, `"language":"fr"`), strings.Index(body, `"language":"fr-CA"`)
	if de < 0 || fr < de || frCA < fr {
		t.Fatalf("list not ordered by language: %s", body)
	}
}

func TestTranslation_DeleteThenRepeat(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	path := "/v1/org/operating-units/" + ownerID + "/translations/fr-CA"
	do(r, http.MethodPut, path, `{"name":"Unité"}`, "org.operating-unit.write", true)
	if rec := do(r, http.MethodDelete, path, "", "org.operating-unit.write", true); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodDelete, path, "", "org.operating-unit.write", true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeTranslationNotFound) {
		t.Fatalf("repeat delete: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodDelete, "/v1/org/operating-units/"+ownerID+"/translations/fr_CA", "", "org.operating-unit.write", true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"language"`) {
		t.Fatalf("malformed language: %d %s", rec.Code, rec.Body)
	}
	if got := metric(a, "org_name_translations_deleted_total"); got != "1" {
		t.Fatalf("deleted counter = %s; want 1", got)
	}
}

func TestTranslation_NameRoute(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	base := "/v1/org/sites/" + ownerID
	get := func(query string) string {
		rec := do(r, http.MethodGet, base+"/name"+query, "", "org.read", true)
		if rec.Code != http.StatusOK {
			t.Fatalf("name%s: %d %s", query, rec.Code, rec.Body)
		}
		return strings.TrimSpace(rec.Body.String())
	}
	baseBody := `{"ownerType":"site","ownerId":"` + ownerID + `","name":"Base name","source":"base"}`
	if got := get(""); got != baseBody {
		t.Fatalf("no language = %s", got)
	}
	do(r, http.MethodPut, base+"/translations/fr", `{"name":"Nom"}`, "org.site.write", true)
	if got := get("?language=fr-CA"); !strings.Contains(got, `"language":"fr"`) || !strings.Contains(got, `"name":"Nom"`) || !strings.Contains(got, `"source":"translation"`) {
		t.Fatalf("primary subtag fallback = %s", got)
	}
	if got := get("?language=de"); got != baseBody {
		t.Fatalf("unmatched language = %s", got)
	}
	for _, bad := range []string{"?language=", "?language=fr_CA", "?language=FR"} {
		if rec := do(r, http.MethodGet, base+"/name"+bad, "", "org.read", true); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"language"`) {
			t.Fatalf("name%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
}
