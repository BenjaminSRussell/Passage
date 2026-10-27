package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

const pumpCatalogJSON = `{"symptoms":[{"code":"NOISE","text":"Noise"}],"areas":[{"code":"BEARING","text":"Bearing"}],` +
	`"types":[{"code":"WEAR","text":"Wear"}],"causes":[{"code":"FATIGUE","text":"Fatigue"}],` +
	`"remedies":[{"code":"REPLACE","text":"Replace"}],"allowedCombinations":[{"symptom":"NOISE","area":"BEARING","type":"WEAR"}]}`

// Every case below is rejected before the store is reached (no database needed).
func TestSlice2Routes_RejectBeforeStore(t *testing.T) {
	h := newTestServer()
	id := testkit.TenantID(6)
	with := func(duties string, extra map[string]string) map[string]string {
		hdr := headers(duties)
		for k, v := range extra {
			hdr[k] = v
		}
		return hdr
	}
	match := map[string]string{"If-Match": `"1"`}
	noKey := func(duties string) map[string]string {
		return map[string]string{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": duties}
	}
	warranty := func(patch string) string {
		return `{"warrantyType":"OEM","vendorPartyId":"` + testkit.TenantID(8) + `","startBasis":"PURCHASE","startsOn":"2025-11-01","months":24` + patch + `}`
	}
	cases := []struct {
		name, method, path, body string
		hdr                      map[string]string
		status                   int
		code                     string
	}{
		{"lifecycle needs If-Match", http.MethodPost, "/api/v1/eam/assets/" + id + "/lifecycle", `{"to":"INACTIVE"}`,
			headers("EamAssetMaintain"), http.StatusPreconditionRequired, "precondition_required"},
		{"lifecycle cannot activate", http.MethodPost, "/api/v1/eam/assets/" + id + "/lifecycle", `{"to":"ACTIVE"}`,
			with("EamAssetMaintain", match), http.StatusBadRequest, "EAM-VAL-001"},
		{"lifecycle needs asset duty", http.MethodPost, "/api/v1/eam/assets/" + id + "/lifecycle", `{"to":"INACTIVE"}`,
			with("EamPlanner", match), http.StatusForbidden, "AUTH-0003"},
		{"criticality needs a key", http.MethodPost, "/api/v1/eam/assets/" + id + "/criticality",
			`{"consequence":{"safety":1,"environment":1,"production":1,"quality":1,"cost":1},"likelihood":1}`,
			noKey("EamAssetMaintain"), http.StatusBadRequest, "idempotency_key_required"},
		{"criticality score out of range", http.MethodPost, "/api/v1/eam/assets/" + id + "/criticality",
			`{"consequence":{"safety":3,"environment":1,"production":6,"quality":1,"cost":1},"likelihood":4}`,
			headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"criticality missing a category", http.MethodPost, "/api/v1/eam/assets/" + id + "/criticality",
			`{"consequence":{"safety":3},"likelihood":4}`, headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"assessments need inquire", http.MethodGet, "/api/v1/eam/assets/" + id + "/criticality", ``,
			headers("EamTechnician"), http.StatusForbidden, "AUTH-0003"},
		{"warranty bad vendor", http.MethodPost, "/api/v1/eam/assets/" + id + "/warranties",
			`{"warrantyType":"OEM","vendorPartyId":"acme","startBasis":"PURCHASE","startsOn":"2025-11-01","months":24,"partsCovered":true}`,
			headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"warranty bad date", http.MethodPost, "/api/v1/eam/assets/" + id + "/warranties",
			`{"warrantyType":"OEM","vendorPartyId":"` + testkit.TenantID(8) + `","startBasis":"PURCHASE","startsOn":"01/11/2025","months":24,"partsCovered":true}`,
			headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"warranty covers nothing", http.MethodPost, "/api/v1/eam/assets/" + id + "/warranties", warranty(``),
			headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"warranty bad exclusion", http.MethodPost, "/api/v1/eam/assets/" + id + "/warranties", warranty(`,"partsCovered":true,"exclusions":["paint"]`),
			headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"warranty bad basis", http.MethodPost, "/api/v1/eam/assets/" + id + "/warranties",
			`{"warrantyType":"OEM","vendorPartyId":"` + testkit.TenantID(8) + `","startBasis":"LEASE","startsOn":"2025-11-01","months":24,"partsCovered":true}`,
			headers("EamAssetMaintain"), http.StatusBadRequest, "EAM-VAL-001"},
		{"catalog needs If-Match", http.MethodPut, "/api/v1/eam/fault-catalogs/PUMP", pumpCatalogJSON,
			headers("EamPlanAdmin"), http.StatusPreconditionRequired, "precondition_required"},
		{"catalog bad asset type", http.MethodPut, "/api/v1/eam/fault-catalogs/pump", pumpCatalogJSON,
			with("EamPlanAdmin", match), http.StatusBadRequest, "EAM-VAL-001"},
		{"catalog combination with an unknown code", http.MethodPut, "/api/v1/eam/fault-catalogs/PUMP",
			`{"symptoms":[{"code":"NOISE","text":"Noise"}],"areas":[{"code":"BEARING","text":"Bearing"}],"types":[{"code":"WEAR","text":"Wear"}],` +
				`"causes":[{"code":"FATIGUE","text":"Fatigue"}],"remedies":[{"code":"REPLACE","text":"Replace"}],` +
				`"allowedCombinations":[{"symptom":"LEAK","area":"BEARING","type":"WEAR"}]}`,
			with("EamPlanAdmin", match), http.StatusBadRequest, "EAM-VAL-001"},
		{"catalog needs plan admin", http.MethodPut, "/api/v1/eam/fault-catalogs/PUMP", pumpCatalogJSON,
			with("EamPlanner", match), http.StatusForbidden, "AUTH-0003"},
		{"fault rejects a bad code", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/faults",
			`{"symptom":"noise","area":"BEARING","type":"WEAR","cause":"FATIGUE","remedy":"REPLACE"}`,
			headers("EamTechnician"), http.StatusBadRequest, "EAM-VAL-001"},
		{"fault needs every code", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/faults",
			`{"symptom":"NOISE","area":"BEARING","type":"WEAR"}`, headers("EamTechnician"), http.StatusBadRequest, "EAM-VAL-001"},
		{"fault needs technician duty", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/faults",
			`{"symptom":"NOISE","area":"BEARING","type":"WEAR","cause":"FATIGUE","remedy":"REPLACE"}`,
			headers("EamPlanner"), http.StatusForbidden, "AUTH-0003"},
		{"downtime needs from and to", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/downtime",
			`{"from":"2026-10-21T08:00:00Z"}`, headers("EamPlanner"), http.StatusBadRequest, "EAM-VAL-001"},
		{"downtime reversed window", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/downtime",
			`{"from":"2026-10-21T10:00:00Z","to":"2026-10-21T08:00:00Z"}`, headers("EamTechnician"), http.StatusBadRequest, "EAM-VAL-001"},
		{"downtime bad resource", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/downtime",
			`{"from":"2026-10-21T08:00:00Z","to":"2026-10-21T12:00:00Z","resourceId":"M-12"}`, headers("EamPlanner"), http.StatusBadRequest, "EAM-VAL-001"},
		{"downtime needs a duty", http.MethodPost, "/api/v1/eam/work-orders/" + id + "/downtime",
			`{"from":"2026-10-21T08:00:00Z","to":"2026-10-21T12:00:00Z"}`, headers("EamInquire"), http.StatusForbidden, "AUTH-0003"},
		{"triage unknown action", http.MethodPost, "/api/v1/eam/requests/" + id + "/triage", `{"action":"MERGE"}`,
			headers("EamPlanner"), http.StatusBadRequest, "EAM-VAL-001"},
		{"triage needs a key", http.MethodPost, "/api/v1/eam/requests/" + id + "/triage", `{"action":"DEFER"}`,
			noKey("EamPlanner"), http.StatusBadRequest, "idempotency_key_required"},
		{"triage needs planner", http.MethodPost, "/api/v1/eam/requests/" + id + "/triage", `{"action":"DEFER"}`,
			headers("EamRequestCreate"), http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectCode(t, call(t, h, tc.method, tc.path, tc.body, tc.hdr), tc.status, tc.code)
		})
	}
}

// TestHTTP_FaultCatalogAndCompletion runs the slice-2 flow over HTTP: the
// catalog's ETag, the completion gate (EAM-FLT-001), an unknown combination
// (EAM-FLT-002), criticality and triage.
func TestHTTP_FaultCatalogAndCompletion(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := New(store.New(pool), nil).Routes()
	all := "EamAssetMaintain,EamPlanAdmin,EamPlanner,EamTechnician,EamInquire,EamRequestCreate"
	send := func(method, path, body, key string, extra map[string]string) *httptest.ResponseRecorder {
		hdr := headers(all)
		hdr["Idempotency-Key"] = key
		for k, v := range extra {
			hdr[k] = v
		}
		return call(t, h, method, path, body, hdr)
	}
	expectCode(t, send(http.MethodGet, "/api/v1/eam/fault-catalogs/PUMP", ``, "", nil), http.StatusNotFound, "EAM-NF-001")
	put := send(http.MethodPut, "/api/v1/eam/fault-catalogs/PUMP", pumpCatalogJSON, "", map[string]string{"If-Match": `"0"`})
	if put.Code != http.StatusOK || put.Header().Get("ETag") != `"1"` {
		t.Fatalf("first PUT: %d etag %q %s", put.Code, put.Header().Get("ETag"), put.Body.String())
	}
	expectCode(t, send(http.MethodPut, "/api/v1/eam/fault-catalogs/PUMP", pumpCatalogJSON, "", map[string]string{"If-Match": `"0"`}),
		http.StatusPreconditionFailed, "conflict_version")
	if get := send(http.MethodGet, "/api/v1/eam/fault-catalogs/PUMP", ``, "", nil); get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET: %d etag %q", get.Code, get.Header().Get("ETag"))
	}

	var asset struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/assets", `{"name":"Pump","assetType":"PUMP","siteId":"`+testkit.TenantID(9)+`","criticality":2}`, "a1", nil),
		http.StatusCreated, &asset)
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/assets/"+asset.ID+"/activate", ``, "act", map[string]string{"If-Match": `"1"`}), http.StatusOK, &asset)
	var assessment struct {
		RiskScore, Criticality int
	}
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/assets/"+asset.ID+"/criticality",
		`{"consequence":{"safety":3,"environment":1,"production":5,"quality":1,"cost":2},"likelihood":4}`, "crit", nil), http.StatusCreated, &assessment)
	if assessment.RiskScore != 20 || assessment.Criticality != 5 {
		t.Fatalf("assessment = %+v, want risk 20 and criticality 5 (AC-44.16)", assessment)
	}

	var wo struct {
		ID       string `json:"id"`
		Priority int    `json:"priority"`
		Version  int    `json:"version"`
	}
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/work-orders", `{"assetId":"`+asset.ID+`","workOrderType":"CORRECTIVE","currency":"USD"}`, "w1", nil),
		http.StatusCreated, &wo)
	if wo.Priority != 1 {
		t.Fatalf("corrective priority after the assessment = %d, want 1", wo.Priority)
	}
	for i, to := range []string{"SCHEDULED", "RELEASED", "IN_PROGRESS"} {
		decodeInto(t, send(http.MethodPost, "/api/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"`+to+`"}`, "s"+to,
			map[string]string{"If-Match": `"` + string(rune('1'+i)) + `"`}), http.StatusOK, &wo)
	}
	match := map[string]string{"If-Match": `"4"`}
	expectCode(t, send(http.MethodPost, "/api/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"COMPLETED"}`, "done-1", match),
		http.StatusUnprocessableEntity, "EAM-FLT-001")
	expectCode(t, send(http.MethodPost, "/api/v1/eam/work-orders/"+wo.ID+"/faults",
		`{"symptom":"NOISE","area":"BEARING","type":"CRACK","cause":"FATIGUE","remedy":"REPLACE"}`, "f0", nil),
		http.StatusUnprocessableEntity, "EAM-FLT-002")
	var detail struct {
		Fault *struct{ Symptom string } `json:"fault"`
	}
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/work-orders/"+wo.ID+"/faults",
		`{"symptom":"NOISE","area":"BEARING","type":"WEAR","cause":"FATIGUE","remedy":"REPLACE"}`, "f1", nil), http.StatusCreated, &detail)
	if detail.Fault == nil || detail.Fault.Symptom != "NOISE" {
		t.Fatalf("detail fault = %+v", detail.Fault)
	}
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"COMPLETED"}`, "done-2", match), http.StatusOK, &wo)

	var req struct{ ID, Status string }
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/requests", `{"assetId":"`+asset.ID+`","source":"USER","severity":3,"description":"Rattle"}`, "r1", nil),
		http.StatusCreated, &req)
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/requests/"+req.ID+"/triage", `{"action":"DEFER","note":"next shutdown"}`, "t1", nil), http.StatusOK, &req)
	if req.Status != "DEFERRED" {
		t.Fatalf("triage status = %s, want DEFERRED", req.Status)
	}
	decodeInto(t, send(http.MethodPost, "/api/v1/eam/requests/"+req.ID+"/triage", `{"action":"REJECT"}`, "t2", nil), http.StatusOK, &req)
	expectCode(t, send(http.MethodPost, "/api/v1/eam/requests/"+req.ID+"/triage", `{"action":"TRIAGE"}`, "t3", nil),
		http.StatusUnprocessableEntity, "EAM-REQ-001")
}
