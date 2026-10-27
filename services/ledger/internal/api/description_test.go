package api

import (
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const testTemplate = "0190a000-0000-7000-8000-0000000000a1"

func TestPutDescriptionTemplate_CreatedThenReplaced(t *testing.T) {
	fs := &fakeStore{created: true}
	h := server(fs)
	body := `{"scope":"tenant","sourceType":"manual_journal","template":"Journal %1 of %2","fieldBindings":{"1":"voucher","2":"document_date"}}`
	rec := call(h, http.MethodPut, "/api/v1/ledger/description-templates/"+testTemplate, body, dutySetupWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	in := fs.putTemplates[0]
	if in.ID != testTemplate || in.Language != "en" || in.LegalEntityID != nil || in.FieldBindings["2"] != "document_date" {
		t.Fatalf("request not mapped with the default language: %+v", in)
	}

	fs.created = false
	rec = call(h, http.MethodPut, "/api/v1/ledger/description-templates/"+testTemplate, body, dutySetupWrite, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutDescriptionTemplate_RequiresSetupDuty(t *testing.T) {
	fs := &fakeStore{}
	body := `{"scope":"tenant","sourceType":"manual_journal","template":"Journal"}`
	rec := call(server(fs), http.MethodPut, "/api/v1/ledger/description-templates/"+testTemplate, body, dutyRead, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
	if len(fs.putTemplates) != 0 {
		t.Fatal("a caller without ledger.setup.write must not reach the store")
	}
}

func TestPutDescriptionTemplate_Validation(t *testing.T) {
	le := `"0190a000-0000-7000-8000-0000000000e1"`
	tests := []struct {
		name      string
		id        string
		body      string
		wantField string
		wantMsg   string
	}{
		{"id not a uuid", "nope", `{"scope":"tenant","sourceType":"x","template":"T"}`, "id", "must be a UUID"},
		{"unknown scope", testTemplate, `{"scope":"global","sourceType":"x","template":"T"}`, "scope", "must be tenant or legal_entity"},
		{"tenant with legal entity", testTemplate, `{"scope":"tenant","legalEntityId":` + le + `,"sourceType":"x","template":"T"}`,
			"legalEntityId", "must be omitted for tenant scope"},
		{"legal entity without id", testTemplate, `{"scope":"legal_entity","sourceType":"x","template":"T"}`,
			"legalEntityId", "is required for legal_entity scope and must be a UUID"},
		{"bad source type", testTemplate, `{"scope":"tenant","sourceType":"Manual Journal","template":"T"}`,
			"sourceType", "must be lowercase letters, digits and underscores, starting with a letter"},
		{"bad language", testTemplate, `{"scope":"tenant","sourceType":"x","language":"english","template":"T"}`,
			"language", "must be a language tag such as en or en-US"},
		{"blank template", testTemplate, `{"scope":"tenant","sourceType":"x","template":"  "}`, "template", "template is required"},
		{"unbound placeholder", testTemplate, `{"scope":"tenant","sourceType":"x","template":"Journal %1"}`,
			"fieldBindings", "placeholder %1 has no binding"},
		{"unknown field", testTemplate, `{"scope":"tenant","sourceType":"x","template":"J %1","fieldBindings":{"1":"salary"}}`,
			"fieldBindings", "unknown field salary for %1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodPut, "/api/v1/ledger/description-templates/"+tc.id, tc.body, dutySetupWrite, nil)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			errs, _ := decode(t, rec)["errors"].(map[string]any)
			if errs[tc.wantField] != tc.wantMsg {
				t.Fatalf("want %s = %q, got %v", tc.wantField, tc.wantMsg, errs)
			}
			if len(fs.putTemplates) != 0 {
				t.Fatal("an invalid template must not reach the store")
			}
		})
	}
}

func TestDescriptionTemplates_ReadRoutes(t *testing.T) {
	fs := &fakeStore{templates: []store.DescriptionTemplate{{ID: testTemplate, Scope: "tenant", SourceType: "manual_journal",
		Language: "en", Template: "Journal %1", FieldBindings: map[string]string{"1": "voucher"}}}}
	h := server(fs)
	rec := call(h, http.MethodGet, "/api/v1/ledger/description-templates", "", dutyRead, nil)
	if rec.Code != http.StatusOK || len(decode(t, rec)["items"].([]any)) != 1 {
		t.Fatalf("list: status %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodGet, "/api/v1/ledger/description-templates/"+testTemplate, "", dutyRead, nil)
	if rec.Code != http.StatusOK || decode(t, rec)["template"] != "Journal %1" {
		t.Fatalf("get: status %d: %s", rec.Code, rec.Body.String())
	}
	for _, id := range []string{"0190a000-0000-7000-8000-0000000000ff", "not-a-uuid"} {
		rec = call(h, http.MethodGet, "/api/v1/ledger/description-templates/"+id, "", dutyRead, nil)
		if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != "LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND" {
			t.Fatalf("missing %s: status %d: %s", id, rec.Code, rec.Body.String())
		}
	}
}
