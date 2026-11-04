package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const assignmentsURL = "/v1/pricing/procedure-assignments"

// Both discounts apply to one line of 100.00: LINE_DISC 8% and MULTILINE_DISC 12%.
const simulateRecords = `"records":[
	{"id":"price","type":"PRICE","customerKind":"all","productKind":"all","currency":"USD","amount":"100.00","validFrom":"2026-01-01","validTo":"2026-12-31"},
	{"id":"line8","type":"LINE_DISC","customerKind":"all","productKind":"all","currency":"USD","amount":"0","discPct1":"8","validFrom":"2026-01-01","validTo":"2026-12-31"},
	{"id":"multi12","type":"MULTILINE_DISC","customerKind":"all","productKind":"all","currency":"USD","amount":"0","discPct1":"12","validFrom":"2026-01-01","validTo":"2026-12-31"}]`

func simulateBody(extra string) string {
	return fmt.Sprintf(`{"legalEntityId":"%s","customerId":"C1","currency":"USD","pricingDate":"2026-06-01",%s
		"lines":[{"productId":"P1","unit":"EA","quantity":"1"}],%s}`, legalEntity, extra, simulateRecords)
}

// activeProcedureNamed creates and activates a procedure with the given steps JSON.
func activeProcedureNamed(t *testing.T, h http.Handler, name, steps string) string {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":"%s","name":"%s","steps":%s}`, legalEntity, name, steps)
	code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, body)
	if code != http.StatusCreated {
		t.Fatalf("create %s = %d %v", name, code, out)
	}
	id := out["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id+":activate", procedureDuties, ``); code != http.StatusOK {
		t.Fatalf("activate %s = %d %v", name, code, out)
	}
	return id
}

const bestSteps = `[{"stepNo":10,"conditionType":"PRICE"},
	{"stepNo":20,"conditionType":"LINE_DISC","exclusionGroup":"DISC","exclusionMode":"BEST"},
	{"stepNo":30,"conditionType":"MULTILINE_DISC","exclusionGroup":"DISC","exclusionMode":"BEST"}]`

const stackSteps = `[{"stepNo":10,"conditionType":"PRICE"},{"stepNo":20,"conditionType":"LINE_DISC"},{"stepNo":30,"conditionType":"MULTILINE_DISC"}]`

func TestProcedureAssignment_SetListClearWithVersions(t *testing.T) {
	h := router(newFake())
	activeProcedureNamed(t, h, "AX_STANDARD", stackSteps)
	body := fmt.Sprintf(`{"legalEntityId":"%s","salesChannel":"","procedureName":"AX_STANDARD","version":0}`, legalEntity)
	status, out := call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", body, inEntity...)
	if status != http.StatusOK || out["procedureName"] != "AX_STANDARD" || out["version"] != float64(1) {
		t.Fatalf("set = %d %v", status, out)
	}
	if status, out = call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", body, inEntity...); status != http.StatusPreconditionFailed || out["code"] != "conflict_version" {
		t.Fatalf("stale set = %d %v; want 412 conflict_version", status, out)
	}
	web := fmt.Sprintf(`{"legalEntityId":"%s","salesChannel":"WEB","procedureName":"AX_STANDARD","version":0}`, legalEntity)
	if status, out = call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", web, inEntity...); status != http.StatusOK {
		t.Fatalf("set WEB = %d %v", status, out)
	}
	status, out = call(t, h, http.MethodGet, assignmentsURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...)
	items, _ := out["items"].([]any)
	if status != http.StatusOK || len(items) != 2 || items[0].(map[string]any)["salesChannel"] != "" || items[1].(map[string]any)["salesChannel"] != "WEB" {
		t.Fatalf("list = %d %v; want the default first, then WEB", status, out)
	}
	clear := assignmentsURL + "?legalEntityId=" + legalEntity + "&salesChannel=WEB&version="
	if status, out = call(t, h, http.MethodDelete, clear+"7", "pricing.admin", "", inEntity...); status != http.StatusPreconditionFailed {
		t.Fatalf("clear at a stale version = %d %v; want 412", status, out)
	}
	if status, _ = call(t, h, http.MethodDelete, clear+"1", "pricing.admin", "", inEntity...); status != http.StatusNoContent {
		t.Fatalf("clear = %d; want 204", status)
	}
	_, out = call(t, h, http.MethodGet, assignmentsURL+"?legalEntityId="+legalEntity, "pricing.view", "", inEntity...)
	if items, _ := out["items"].([]any); len(items) != 1 {
		t.Fatalf("list after clear = %v; want only the default", out)
	}
}

func TestProcedureAssignment_RefusalsDutiesAndScope(t *testing.T) {
	h := router(newFake())
	body := fmt.Sprintf(`{"legalEntityId":"%s","salesChannel":"","procedureName":"AX_STANDARD","version":0}`, legalEntity)
	if status, out := call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", body, inEntity...); status != http.StatusUnprocessableEntity || out["code"] != "PRC-0063" {
		t.Fatalf("assigning a procedure with no active version = %d %v; want 422 PRC-0063", status, out)
	}
	if status, _ := call(t, h, http.MethodPut, assignmentsURL, "pricing.view,pricing.maintain", body, inEntity...); status != http.StatusForbidden {
		t.Fatalf("set without pricing.admin = %d; want 403", status)
	}
	if status, _ := call(t, h, http.MethodDelete, assignmentsURL+"?legalEntityId="+legalEntity+"&version=1", "pricing.view", "", inEntity...); status != http.StatusForbidden {
		t.Fatalf("clear without pricing.admin = %d; want 403", status)
	}
	if status, _ := call(t, h, http.MethodGet, assignmentsURL+"?legalEntityId="+legalEntity, "", "", inEntity...); status != http.StatusForbidden {
		t.Fatalf("list without a duty = %d; want 403", status)
	}
	if status, out := call(t, h, http.MethodGet, assignmentsURL+"?legalEntityId="+otherEntity, "pricing.view", "", inEntity...); status != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("list of another legal entity = %d %v; want 403 AUTH-0003", status, out)
	}
	status, out := call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", `{"legalEntityId":"x","salesChannel":"web","procedureName":"ax std","version":-1}`, inEntity...)
	fields, _ := out["errors"].(map[string]any)
	if status != http.StatusUnprocessableEntity || fields["legalEntityId"] == nil || fields["salesChannel"] == nil || fields["procedureName"] == nil || fields["version"] == nil {
		t.Fatalf("invalid body = %d %v; want 422 naming all four fields", status, out)
	}
	if status, out = call(t, h, http.MethodDelete, assignmentsURL+"?legalEntityId=x&version=0", "pricing.admin", "", inEntity...); status != http.StatusUnprocessableEntity {
		t.Fatalf("clear with a bad id and version = %d %v; want 422", status, out)
	}
}

func lineOf(out map[string]any) map[string]any { return out["lines"].([]any)[0].(map[string]any) }

func TestSimulate_AssignedProcedureDrivesTheResolution(t *testing.T) {
	h := router(newFake())
	// Without a procedure the reference order stacks both discounts: 100 x 0.92 x 0.88.
	status, out := call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(""))
	if status != http.StatusOK || lineOf(out)["lineNet"] != "80.96" || out["procedure"] != nil {
		t.Fatalf("reference order = %d %v; want 80.96 and no procedure", status, out)
	}
	activeProcedureNamed(t, h, "AX_STANDARD", stackSteps)
	activeProcedureNamed(t, h, "BEST_ONLY", bestSteps)
	set := func(channel, name string) {
		b := fmt.Sprintf(`{"legalEntityId":"%s","salesChannel":"%s","procedureName":"%s","version":0}`, legalEntity, channel, name)
		if s, o := call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", b, inEntity...); s != http.StatusOK {
			t.Fatalf("assign %s/%s = %d %v", channel, name, s, o)
		}
	}
	set("", "AX_STANDARD")
	set("WEB", "BEST_ONLY")

	status, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(""))
	proc, _ := out["procedure"].(map[string]any)
	if status != http.StatusOK || lineOf(out)["lineNet"] != "80.96" || proc["name"] != "AX_STANDARD" || proc["version"] != float64(1) {
		t.Fatalf("default assignment = %d %v; want 80.96 under AX_STANDARD v1", status, out)
	}
	// AC-26.6: the WEB channel's BEST group keeps only the 12% discount.
	status, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(`"salesChannel":"WEB",`))
	proc, _ = out["procedure"].(map[string]any)
	if status != http.StatusOK || lineOf(out)["lineNet"] != "88" || proc["name"] != "BEST_ONLY" {
		t.Fatalf("WEB channel = %d %v; want 88 under BEST_ONLY", status, out)
	}
	// A channel with no row of its own falls back to the default; a named procedure wins over both.
	if status, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(`"salesChannel":"EDI",`)); status != http.StatusOK || lineOf(out)["lineNet"] != "80.96" {
		t.Fatalf("EDI channel = %d %v; want the default's 80.96", status, out)
	}
	status, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(`"procedure":"BEST_ONLY",`))
	if status != http.StatusOK || lineOf(out)["lineNet"] != "88" {
		t.Fatalf("named procedure = %d %v; want 88", status, out)
	}
	if status, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(`"procedure":"NOPE",`)); status != http.StatusUnprocessableEntity || out["code"] != "PRC-0063" {
		t.Fatalf("unknown procedure = %d %v; want 422 PRC-0063", status, out)
	}
	if status, out = call(t, h, http.MethodPost, "/v1/pricing/simulate", allDuties, simulateBody(`"salesChannel":"web",`)); status != http.StatusUnprocessableEntity {
		t.Fatalf("bad sales channel = %d %v; want 422", status, out)
	}
}

func TestResolve_UsesTheAssignedProcedure(t *testing.T) {
	h := router(newFake())
	for _, rec := range []string{
		`{"type":"PRICE","customerKind":"all","productKind":"all","amount":"100.00"}`,
		`{"type":"LINE_DISC","customerKind":"all","productKind":"all","amount":"0","discPct1":"8"}`,
		`{"type":"MULTILINE_DISC","customerKind":"all","productKind":"all","amount":"0","discPct1":"12"}`,
	} {
		body := fmt.Sprintf(`{"legalEntityId":"%s","currency":"USD","fromQuantity":"0","validFrom":"2026-01-01","validTo":"2026-12-31",`, legalEntity) + rec[1:]
		code, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, body)
		if code != http.StatusCreated {
			t.Fatalf("create record = %d %v", code, created)
		}
		if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+created["id"].(string)+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
			t.Fatalf("activate record = %d %v", code, out)
		}
	}
	activeProcedureNamed(t, h, "BEST_ONLY", bestSteps)
	b := fmt.Sprintf(`{"legalEntityId":"%s","salesChannel":"","procedureName":"BEST_ONLY","version":0}`, legalEntity)
	if s, o := call(t, h, http.MethodPut, assignmentsURL, "pricing.admin", b, inEntity...); s != http.StatusOK {
		t.Fatalf("assign = %d %v", s, o)
	}
	status, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, fmt.Sprintf(resolveBody, legalEntity, "1"))
	proc, _ := out["procedure"].(map[string]any)
	if status != http.StatusOK || lineOf(out)["lineNet"] != "88" || proc["name"] != "BEST_ONLY" || proc["id"] == "" {
		t.Fatalf("resolve = %d %v; want 88 under BEST_ONLY", status, out)
	}
}
