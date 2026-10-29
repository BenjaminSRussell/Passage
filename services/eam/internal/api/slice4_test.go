package api

import (
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// noKey carries the tenant and duties but no Idempotency-Key.
func noKey(duties string) map[string]string {
	return map[string]string{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": duties}
}

// TestSlice4Routes_RejectBeforeStore covers validation and duties of the
// functional location and sequence routes; every case returns before the store.
func TestSlice4Routes_RejectBeforeStore(t *testing.T) {
	h := newTestServer()
	id := testkit.TenantID(7)
	site := testkit.TenantID(8)
	withMatch := func(duties string) map[string]string {
		hdr := headers(duties)
		hdr["If-Match"] = `"1"`
		return hdr
	}
	step := `{"code":"A","jobType":"LUBE","multiple":1,"hierarchy":1}`
	seq := func(fields string) string {
		return `{"code":"PUMP-SEQ","name":"Pump","assetId":"` + id + `","baseIntervalValue":250` + fields + `}`
	}
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		hdr    map[string]string
		status int
		code   string
	}{
		{"location needs idempotency key", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"PLANT-1","name":"Plant","siteId":"` + site + `"}`, noKey("EamStructureMaintain"), 400, "idempotency_key_required"},
		{"location rejects lowercase code", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"plant","name":"Plant","siteId":"` + site + `"}`, headers("EamStructureMaintain"), 400, "EAM-VAL-001"},
		{"location code cannot hold the path separator", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"A/B","name":"Plant","siteId":"` + site + `"}`, headers("EamStructureMaintain"), 400, "EAM-VAL-001"},
		{"location needs a site", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"PLANT-1","name":"Plant","siteId":"x"}`, headers("EamStructureMaintain"), 400, "EAM-VAL-001"},
		{"location needs a name", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"PLANT-1","name":"  ","siteId":"` + site + `"}`, headers("EamStructureMaintain"), 400, "EAM-VAL-001"},
		{"location rejects a bad type", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"PLANT-1","name":"Plant","siteId":"` + site + `","locationType":"line"}`, headers("EamStructureMaintain"), 400, "EAM-VAL-001"},
		{"location needs structure duty", http.MethodPost, "/v1/eam/functional-locations",
			`{"code":"PLANT-1","name":"Plant","siteId":"` + site + `"}`, headers("EamAssetMaintain"), 403, "AUTH-0003"},
		{"location list rejects a bad site", http.MethodGet, "/v1/eam/functional-locations?site=nope", ``, headers("EamInquire"), 400, "EAM-VAL-001"},
		{"location list rejects a bad subtree", http.MethodGet, "/v1/eam/functional-locations?under=PLANT-1", ``, headers("EamInquire"), 400, "EAM-VAL-001"},
		{"location list needs inquire", http.MethodGet, "/v1/eam/functional-locations", ``, headers("EamPlanner"), 403, "AUTH-0003"},
		{"placement needs If-Match", http.MethodPost, "/v1/eam/assets/" + id + "/location",
			`{"functionalLocationId":null}`, headers("EamStructureMaintain"), 428, "precondition_required"},
		{"placement needs idempotency key", http.MethodPost, "/v1/eam/assets/" + id + "/location",
			`{"functionalLocationId":null}`, noKey("EamStructureMaintain"), 400, "idempotency_key_required"},
		{"placement rejects a code for the location", http.MethodPost, "/v1/eam/assets/" + id + "/location",
			`{"functionalLocationId":"BAY-1"}`, withMatch("EamStructureMaintain"), 400, "EAM-VAL-001"},
		{"placement needs structure duty", http.MethodPost, "/v1/eam/assets/" + id + "/location",
			`{"functionalLocationId":null}`, withMatch("EamTechnician"), 403, "AUTH-0003"},
		{"location history needs inquire", http.MethodGet, "/v1/eam/assets/" + id + "/location-history", ``, headers("EamTechnician"), 403, "AUTH-0003"},
		{"sequence needs plan admin", http.MethodPost, "/v1/eam/sequences", seq(`,"baseIntervalUnit":"DAYS","steps":[` + step + `]`),
			headers("EamInquire"), 403, "AUTH-0003"},
		{"sequence rejects a bad code", http.MethodPost, "/v1/eam/sequences",
			`{"code":"x","name":"n","assetId":"` + id + `","baseIntervalValue":1,"baseIntervalUnit":"DAYS","steps":[` + step + `]}`,
			headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
		{"sequence rejects an hour unit", http.MethodPost, "/v1/eam/sequences", seq(`,"baseIntervalUnit":"HOURS","steps":[` + step + `]`),
			headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
		{"sequence rejects no steps", http.MethodPost, "/v1/eam/sequences", seq(`,"baseIntervalUnit":"DAYS","steps":[]`),
			headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
		{"sequence rejects a zero interval", http.MethodPost, "/v1/eam/sequences",
			`{"code":"PUMP-SEQ","name":"n","assetId":"` + id + `","baseIntervalValue":0,"baseIntervalUnit":"DAYS","steps":[` + step + `]}`,
			headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
		{"sequence rejects a bad counter id", http.MethodPost, "/v1/eam/sequences", seq(`,"counterId":"hours","steps":[` + step + `]`),
			headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
		{"sequence list rejects a bad asset", http.MethodGet, "/v1/eam/sequences?asset=x", ``, headers("EamInquire"), 400, "EAM-VAL-001"},
		{"simulation bounds the occurrences", http.MethodGet, "/v1/eam/sequences/" + id + "/simulation?occurrences=500", ``,
			headers("EamInquire"), 400, "EAM-VAL-001"},
		{"occurrence needs plan admin", http.MethodPost, "/v1/eam/sequences/" + id + "/occurrences", `{}`, headers("EamPlanner"), 403, "AUTH-0003"},
		{"occurrence needs idempotency key", http.MethodPost, "/v1/eam/sequences/" + id + "/occurrences", `{}`, noKey("EamPlanAdmin"), 400, "idempotency_key_required"},
		{"occurrence rejects a bad currency", http.MethodPost, "/v1/eam/sequences/" + id + "/occurrences", `{"currency":"usd"}`,
			headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
		{"occurrence path needs a UUID", http.MethodPost, "/v1/eam/sequences/PUMP-SEQ/occurrences", `{}`, headers("EamPlanAdmin"), 400, "EAM-VAL-001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectCode(t, call(t, h, tc.method, tc.path, tc.body, tc.hdr), tc.status, tc.code)
		})
	}
}
