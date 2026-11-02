package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const parametersURL = "/v1/pricing/parameters"

// inEntity scopes a call to the test legal entity, as the gateway's legal-entity claim does.
var inEntity = []string{"X-Passage-Legal-Entity", legalEntity}

func TestParameters_DefaultsThenSetWithVersion(t *testing.T) {
	h := router(newFake())
	status, out := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...)
	if status != http.StatusOK || out["journalFourEyes"] != false || out["version"] != float64(0) {
		t.Fatalf("defaults = %d %v", status, out)
	}
	body := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":true,"version":0}`, legalEntity)
	status, out = call(t, h, http.MethodPut, parametersURL, "pricing.admin", body, inEntity...)
	if status != http.StatusOK || out["journalFourEyes"] != true || out["version"] != float64(1) {
		t.Fatalf("set = %d %v", status, out)
	}
	status, out = call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...)
	if status != http.StatusOK || out["journalFourEyes"] != true || out["version"] != float64(1) {
		t.Fatalf("read after set = %d %v", status, out)
	}
	// A second write with the stale version 0 is a version conflict.
	status, out = call(t, h, http.MethodPut, parametersURL, "pricing.admin", body, inEntity...)
	if status != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale set = %d %v", status, out)
	}
}

func TestParameters_DutiesScopeAndValidation(t *testing.T) {
	h := router(newFake())
	body := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":true,"version":0}`, legalEntity)
	if status, _ := call(t, h, http.MethodPut, parametersURL, "pricing.view,pricing.maintain,pricing.journal.post", body, inEntity...); status != http.StatusForbidden {
		t.Fatalf("set without pricing.admin = %d, want 403", status)
	}
	if status, _ := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "", "", inEntity...); status != http.StatusForbidden {
		t.Fatalf("get without duty = %d, want 403", status)
	}
	// A legal entity outside the caller's scope is refused, not shown as the defaults.
	if status, out := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+otherEntity, "pricing.view", "", inEntity...); status != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("get of another legal entity = %d %v, want 403 AUTH-0003", status, out)
	}
	other := fmt.Sprintf(`{"legalEntityId":"%s","journalFourEyes":true,"version":0}`, otherEntity)
	if status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", other, inEntity...); status != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("set of another legal entity = %d %v, want 403 AUTH-0003", status, out)
	}
	if status, _ := call(t, h, http.MethodGet, parametersURL+"?legalEntityId="+legalEntity, "pricing.view", ""); status != http.StatusForbidden {
		t.Fatalf("get without a legal-entity claim = %d, want 403", status)
	}
	if status, _ := call(t, h, http.MethodGet, parametersURL+"?legalEntityId=x", "pricing.view", "", inEntity...); status != http.StatusUnprocessableEntity {
		t.Fatalf("bad legal entity = %d, want 422", status)
	}
	status, out := call(t, h, http.MethodPut, parametersURL, "pricing.admin", `{"legalEntityId":"x","version":-1}`, inEntity...)
	fields, _ := out["errors"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["legalEntityId"] == nil || fields["version"] == nil || fields["journalFourEyes"] == nil {
		t.Fatalf("bad body = %d %v, want 422 naming legalEntityId, version and journalFourEyes", status, out)
	}
	// The PUT replaces every value: an omitted journalFourEyes is not saved as false.
	missing := fmt.Sprintf(`{"legalEntityId":"%s","version":0}`, legalEntity)
	if status, _ := call(t, h, http.MethodPut, parametersURL, "pricing.admin", missing, inEntity...); status != http.StatusUnprocessableEntity {
		t.Fatalf("set without journalFourEyes = %d, want 422", status)
	}
}
