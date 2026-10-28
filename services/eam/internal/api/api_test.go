package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// The validation and authorization paths below return before any store call,
// so the handler is built without a database.
func newTestServer() http.Handler {
	return New(nil, nil).Routes()
}

func call(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func headers(duties string) map[string]string {
	return map[string]string{
		"X-Passage-Tenant": testkit.TenantID(1),
		"X-Passage-Duties": duties,
		"Idempotency-Key":  "key-1",
		"X-Passage-User":   "user-1",
	}
}

func TestRoutes_RequireTenant(t *testing.T) {
	rec := call(t, newTestServer(), http.MethodPost, "/v1/eam/assets", `{}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRoutes_RequireDuty(t *testing.T) {
	rec := call(t, newTestServer(), http.MethodPost, "/v1/eam/assets", `{}`, headers("EamInquire"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCreateAsset_ValidationAndIdempotency(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		name   string
		hdr    map[string]string
		body   string
		status int
	}{
		{"missing idempotency key", map[string]string{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": "EamAssetMaintain"},
			`{}`, http.StatusBadRequest},
		{"criticality out of range", headers("EamAssetMaintain"),
			`{"name":"Pump","assetType":"PUMP","siteId":"` + testkit.TenantID(2) + `","criticality":9}`, http.StatusBadRequest},
		{"bad asset type", headers("EamAssetMaintain"),
			`{"name":"Pump","assetType":"pump","siteId":"` + testkit.TenantID(2) + `","criticality":3}`, http.StatusBadRequest},
		{"unknown field", headers("EamAssetMaintain"), `{"nope":1}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := call(t, h, http.MethodPost, "/v1/eam/assets", tc.body, tc.hdr); rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

func TestActivateAsset_RequiresIfMatch(t *testing.T) {
	h := newTestServer()
	id := testkit.TenantID(3)
	rec := call(t, h, http.MethodPost, "/v1/eam/assets/"+id+"/activate", ``, headers("EamAssetMaintain"))
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428", rec.Code)
	}
}

func TestWorkOrderStatus_RejectsUnknownStatus(t *testing.T) {
	h := newTestServer()
	hdr := headers("EamPlanner")
	hdr["If-Match"] = `"1"`
	rec := call(t, h, http.MethodPost, "/v1/eam/work-orders/"+testkit.TenantID(4)+"/status", `{"to":"BOGUS"}`, hdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestKPIs_RequiresWindow(t *testing.T) {
	h := newTestServer()
	rec := call(t, h, http.MethodGet, "/v1/eam/kpis?asset="+testkit.TenantID(5), ``, headers("EamInquire"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestValidation_BeforeTheStore(t *testing.T) {
	h := newTestServer()
	asset := testkit.TenantID(7)
	withIfMatch := func(duties string) map[string]string {
		hdr := headers(duties)
		hdr["If-Match"] = `"1"`
		return hdr
	}
	cases := []struct {
		name, method, path, body string
		hdr                      map[string]string
		status                   int
	}{
		{"round orders are not enabled", http.MethodPost, "/v1/eam/work-orders",
			`{"assetId":"` + asset + `","workOrderType":"ROUND","currency":"USD"}`, headers("EamPlanner"), http.StatusBadRequest},
		{"priority out of range", http.MethodPost, "/v1/eam/work-orders",
			`{"assetId":"` + asset + `","workOrderType":"CORRECTIVE","priority":7,"currency":"USD"}`, headers("EamPlanner"), http.StatusBadRequest},
		{"downtime only on completion", http.MethodPost, "/v1/eam/work-orders/" + asset + "/status",
			`{"to":"RELEASED","downtimeMinutes":30}`, withIfMatch("EamTechnician"), http.StatusBadRequest},
		{"negative downtime", http.MethodPost, "/v1/eam/work-orders/" + asset + "/status",
			`{"to":"COMPLETED","downtimeMinutes":-1}`, withIfMatch("EamTechnician"), http.StatusBadRequest},
		{"status needs a planner or technician", http.MethodPost, "/v1/eam/work-orders/" + asset + "/status",
			`{"to":"RELEASED"}`, withIfMatch("EamInquire"), http.StatusForbidden},
		{"iot requests come from rules", http.MethodPost, "/v1/eam/requests",
			`{"assetId":"` + asset + `","source":"IOT","severity":2,"description":"x"}`, headers("EamRequestCreate"), http.StatusBadRequest},
		{"labor hours must be a positive decimal", http.MethodPost, "/v1/eam/work-orders/" + asset + "/labor",
			`{"hours":"0","rate":"45.00"}`, headers("EamTechnician"), http.StatusBadRequest},
		{"list limit", http.MethodGet, "/v1/eam/work-orders?limit=500", ``, headers("EamInquire"), http.StatusBadRequest},
		{"list status", http.MethodGet, "/v1/eam/work-orders?status=DONE", ``, headers("EamInquire"), http.StatusBadRequest},
		{"list cursor", http.MethodGet, "/v1/eam/assets?cursor=abc", ``, headers("EamInquire"), http.StatusBadRequest},
		{"lists need EamInquire", http.MethodGet, "/v1/eam/assets", ``, headers("EamPlanner"), http.StatusForbidden},
		{"detail id must be a UUID", http.MethodGet, "/v1/eam/work-orders/WO-1", ``, headers("EamInquire"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := call(t, h, tc.method, tc.path, tc.body, tc.hdr); rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
		})
	}
}

// TestHTTP_ReadingAndWorkOrderFlow runs the HTTP layer against a database:
// status codes, replay headers and problem codes as clients see them.
func TestHTTP_ReadingAndWorkOrderFlow(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := New(store.New(pool), nil).Routes()
	all := "EamAssetMaintain,EamCounterRegister,EamPlanner,EamTechnician,EamInquire"
	post := func(path, body, key string, extra map[string]string) *httptest.ResponseRecorder {
		hdr := headers(all)
		hdr["Idempotency-Key"] = key
		for k, v := range extra {
			hdr[k] = v
		}
		return call(t, h, http.MethodPost, path, body, hdr)
	}
	var asset struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	rec := post("/v1/eam/assets", `{"name":"Pump","assetType":"PUMP","siteId":"`+testkit.TenantID(9)+`","criticality":5}`, "a1", nil)
	decodeInto(t, rec, http.StatusCreated, &asset)
	rec = post("/v1/eam/work-orders", `{"assetId":"`+asset.ID+`","workOrderType":"CORRECTIVE","currency":"USD"}`, "w0", nil)
	expectCode(t, rec, http.StatusUnprocessableEntity, "EAM-AST-001")
	rec = post("/v1/eam/assets/"+asset.ID+"/activate", ``, "act", map[string]string{"If-Match": `"1"`})
	decodeInto(t, rec, http.StatusOK, &asset)

	var counter struct{ ID string }
	decodeInto(t, post("/v1/eam/assets/"+asset.ID+"/counters", `{"name":"hours","unit":"h"}`, "c1", nil), http.StatusCreated, &counter)
	reading := `{"value":12300,"readingAt":"2026-10-21T00:00:00Z"}`
	if rec := post("/v1/eam/counters/"+counter.ID+"/readings", reading, "r1", nil); rec.Code != http.StatusCreated {
		t.Fatalf("reading: %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/v1/eam/counters/"+counter.ID+"/readings", reading, "r1", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", rec.Code, rec.Header())
	}
	rec = post("/v1/eam/counters/"+counter.ID+"/readings", `{"value":12250,"readingAt":"2026-10-21T01:00:00Z"}`, "r2", nil)
	expectCode(t, rec, http.StatusUnprocessableEntity, "EAM-CTR-001") // AC-44.6

	var wo struct {
		ID       string `json:"id"`
		Priority int    `json:"priority"`
		Version  int    `json:"version"`
	}
	decodeInto(t, post("/v1/eam/work-orders", `{"assetId":"`+asset.ID+`","workOrderType":"CORRECTIVE","currency":"USD"}`, "w1", nil),
		http.StatusCreated, &wo)
	if wo.Priority != 1 {
		t.Fatalf("corrective priority on criticality 5 = %d, want 1 (BR-44.15)", wo.Priority)
	}
	rec = post("/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"SCHEDULED"}`, "s1", map[string]string{"If-Match": `"9"`})
	expectCode(t, rec, http.StatusPreconditionFailed, "conflict_version")

	list := call(t, h, http.MethodGet, "/v1/eam/work-orders?asset="+asset.ID, ``, headers(all))
	var page struct {
		Items []struct{ ID string } `json:"items"`
	}
	decodeInto(t, list, http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].ID != wo.ID {
		t.Fatalf("work order list = %+v", page)
	}
	missing := call(t, h, http.MethodGet, "/v1/eam/assets/"+testkit.TenantID(42), ``, headers(all))
	expectCode(t, missing, http.StatusNotFound, "EAM-NF-001")
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, status int, v any) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

func expectCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != status || p.Code != code {
		t.Fatalf("got %d %q, want %d %q (body %s)", rec.Code, p.Code, status, code, rec.Body.String())
	}
}
