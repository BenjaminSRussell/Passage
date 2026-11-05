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

// TestSlice5Routes_RejectBeforeStore covers validation and duties of the round,
// reminder and KPI routes; every case returns before the store.
func TestSlice5Routes_RejectBeforeStore(t *testing.T) {
	h := newTestServer()
	id := testkit.TenantID(7)
	site := testkit.TenantID(8)
	stop := `{"sequence":1,"assetId":"` + id + `","jobType":"INSPECT"}`
	round := func(code, siteID, stops string) string {
		return `{"code":"` + code + `","name":"Weekly","siteId":"` + siteID + `","stops":[` + stops + `]}`
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
		{"round needs plan admin", http.MethodPost, "/v1/eam/rounds", round("WEEKLY", site, stop), headers("eam.work.plan"), 403, "AUTH-0003"},
		{"round needs idempotency key", http.MethodPost, "/v1/eam/rounds", round("WEEKLY", site, stop), noKey("eam.plan.admin"), 400, "idempotency_key_required"},
		{"round rejects a lowercase code", http.MethodPost, "/v1/eam/rounds", round("weekly", site, stop), headers("eam.plan.admin"), 400, "EAM-VAL-001"},
		{"round needs a site", http.MethodPost, "/v1/eam/rounds", round("WEEKLY", "plant", stop), headers("eam.plan.admin"), 400, "EAM-VAL-001"},
		{"round stop needs an asset UUID", http.MethodPost, "/v1/eam/rounds",
			round("WEEKLY", site, `{"sequence":1,"assetId":"P-1","jobType":"INSPECT"}`), headers("eam.plan.admin"), 400, "EAM-VAL-001"},
		{"round rejects unknown fields", http.MethodPost, "/v1/eam/rounds",
			`{"code":"WEEKLY","name":"W","siteId":"` + site + `","stops":[],"color":"red"}`, headers("eam.plan.admin"), 400, "EAM-VAL-001"},
		{"round list needs inquire", http.MethodGet, "/v1/eam/rounds", ``, headers("eam.plan.admin"), 403, "AUTH-0003"},
		{"round list rejects a bad cursor", http.MethodGet, "/v1/eam/rounds?cursor=x", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"round read needs a UUID", http.MethodGet, "/v1/eam/rounds/WEEKLY", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"round order needs planner", http.MethodPost, "/v1/eam/rounds/" + id + "/work-orders", `{}`, headers("eam.plan.admin"), 403, "AUTH-0003"},
		{"round order needs idempotency key", http.MethodPost, "/v1/eam/rounds/" + id + "/work-orders", `{}`, noKey("eam.work.plan"), 400, "idempotency_key_required"},
		{"round order rejects a bad currency", http.MethodPost, "/v1/eam/rounds/" + id + "/work-orders", `{"currency":"usd"}`, headers("eam.work.plan"), 400, "EAM-VAL-001"},
		{"work order API refuses ROUND", http.MethodPost, "/v1/eam/work-orders", `{"assetId":"` + id + `","workOrderType":"ROUND","currency":"USD"}`,
			headers("eam.work.plan"), 400, "EAM-VAL-001"},
		{"stop outcome needs technician", http.MethodPost, "/v1/eam/work-orders/" + id + "/stops/1", `{"outcome":"DONE"}`, headers("eam.work.plan"), 403, "AUTH-0003"},
		{"stop outcome needs idempotency key", http.MethodPost, "/v1/eam/work-orders/" + id + "/stops/1", `{"outcome":"DONE"}`, noKey("eam.work-order.execute"), 400, "idempotency_key_required"},
		{"stop sequence is a number", http.MethodPost, "/v1/eam/work-orders/" + id + "/stops/first", `{"outcome":"DONE"}`, headers("eam.work-order.execute"), 400, "EAM-VAL-001"},
		{"stop sequence is at most 50", http.MethodPost, "/v1/eam/work-orders/" + id + "/stops/51", `{"outcome":"DONE"}`, headers("eam.work-order.execute"), 400, "EAM-VAL-001"},
		{"stop outcome is DONE or SKIPPED", http.MethodPost, "/v1/eam/work-orders/" + id + "/stops/1", `{"outcome":"PARTIAL"}`, headers("eam.work-order.execute"), 400, "EAM-VAL-001"},
		{"overdue window is 1-72 h", http.MethodGet, "/v1/eam/work-orders/emergency-overdue?hours=73", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"overdue needs inquire", http.MethodGet, "/v1/eam/work-orders/emergency-overdue", ``, headers("eam.work.plan"), 403, "AUTH-0003"},
		{"plan reviews need inquire", http.MethodGet, "/v1/eam/assets/plan-reviews", ``, headers("eam.asset.maintain"), 403, "AUTH-0003"},
		{"cost needs a window", http.MethodGet, "/v1/eam/kpis/cost?from=2026-01-01T00:00:00Z", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"cost window runs forward", http.MethodGet, "/v1/eam/kpis/cost?from=2026-02-01T00:00:00Z&to=2026-01-01T00:00:00Z", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"cost rejects a date without time", http.MethodGet, "/v1/eam/kpis/cost?from=2026-01-01&to=2026-02-01", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"cost rejects a bad asset type", http.MethodGet, "/v1/eam/kpis/cost?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&assetType=pump", ``,
			headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"backlog rejects a bad site", http.MethodGet, "/v1/eam/kpis/backlog?site=PLANT", ``, headers("eam.inquire"), 400, "EAM-VAL-001"},
		{"plan line rejects a bad round id", http.MethodPost, "/v1/eam/plans",
			`{"code":"RD","name":"R","currency":"USD","lines":[{"kind":"ROUND","roundId":"WEEKLY"}]}`, headers("eam.plan.admin"), 400, "EAM-VAL-001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expectCode(t, call(t, h, tc.method, tc.path, tc.body, tc.hdr), tc.status, tc.code)
		})
	}
}

// TestHTTP_RoundFlow runs a round through the HTTP layer against a database:
// define, open an order, start it, see EAM-RND-001 on completion, record the
// stops (one mandatory skip), complete, and read the ROUND request.
func TestHTTP_RoundFlow(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := New(store.New(pool), nil).Routes()
	all := "eam.asset.maintain,eam.plan.admin,eam.work.plan,eam.work-order.execute,eam.inquire"
	post := func(path, body, key string, extra map[string]string) *httptest.ResponseRecorder {
		hdr := headers(all)
		hdr["Idempotency-Key"] = key
		for k, v := range extra {
			hdr[k] = v
		}
		return call(t, h, http.MethodPost, path, body, hdr)
	}
	site := testkit.TenantID(9)
	var assets [2]struct {
		ID string `json:"id"`
	}
	for i := range assets {
		key := string(rune('a' + i))
		decodeInto(t, post("/v1/eam/assets", `{"name":"Valve","assetType":"VALVE","siteId":"`+site+`","criticality":3}`, "asset-"+key, nil),
			http.StatusCreated, &assets[i])
		if rec := post("/v1/eam/assets/"+assets[i].ID+"/activate", ``, "act-"+key, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK {
			t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
		}
	}
	body := `{"code":"WEEKLY","name":"Weekly valves","siteId":"` + site + `","stops":[` +
		`{"sequence":1,"assetId":"` + assets[0].ID + `","jobType":"INSPECT","mandatory":true},` +
		`{"sequence":2,"assetId":"` + assets[1].ID + `","jobType":"INSPECT","travelMinutes":4}]}`
	var round struct {
		ID            string `json:"id"`
		TravelMinutes int    `json:"travelMinutes"`
	}
	decodeInto(t, post("/v1/eam/rounds", body, "round", nil), http.StatusCreated, &round)
	if round.TravelMinutes != 4 {
		t.Fatalf("travel = %d, want 4", round.TravelMinutes)
	}
	expectCode(t, post("/v1/eam/rounds", body, "round-again", nil), http.StatusConflict, "EAM-DUP-001")
	expectCode(t, post("/v1/eam/rounds", `{"code":"GAP","name":"g","siteId":"`+site+`","stops":[{"sequence":2,"assetId":"`+assets[0].ID+`","jobType":"INSPECT"}]}`, "gap", nil),
		http.StatusUnprocessableEntity, "EAM-RND-003")

	var wo struct {
		ID      string `json:"id"`
		Type    string `json:"workOrderType"`
		Version int    `json:"version"`
	}
	decodeInto(t, post("/v1/eam/rounds/"+round.ID+"/work-orders", ``, "wo", nil), http.StatusCreated, &wo)
	if wo.Type != "ROUND" {
		t.Fatalf("type = %s, want ROUND", wo.Type)
	}
	for i, to := range []string{"SCHEDULED", "RELEASED", "IN_PROGRESS"} {
		decodeInto(t, post("/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"`+to+`"}`, "st-"+to, map[string]string{"If-Match": `"` + string(rune('1'+i)) + `"`}),
			http.StatusOK, &wo)
	}
	match := map[string]string{"If-Match": `"4"`}
	expectCode(t, post("/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"COMPLETED"}`, "done-early", match), http.StatusUnprocessableEntity, "EAM-RND-001")
	expectCode(t, post("/v1/eam/work-orders/"+wo.ID+"/stops/1", `{"outcome":"SKIPPED"}`, "skip-bare", nil), http.StatusUnprocessableEntity, "EAM-RND-002")
	var skipped struct {
		Outcome   string `json:"outcome"`
		RequestID string `json:"requestId"`
	}
	decodeInto(t, post("/v1/eam/work-orders/"+wo.ID+"/stops/1", `{"outcome":"SKIPPED","reason":"flooded"}`, "skip", nil), http.StatusOK, &skipped)
	if skipped.Outcome != "SKIPPED" || skipped.RequestID == "" {
		t.Fatalf("skipped mandatory stop = %+v, want a request", skipped)
	}
	if rec := post("/v1/eam/work-orders/"+wo.ID+"/stops/2", `{"outcome":"DONE"}`, "done-2", nil); rec.Code != http.StatusOK {
		t.Fatalf("stop 2: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"COMPLETED"}`, "done", match); rec.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Stops []struct {
			Sequence int    `json:"sequence"`
			Outcome  string `json:"outcome"`
		} `json:"stops"`
	}
	decodeInto(t, call(t, h, http.MethodGet, "/v1/eam/work-orders/"+wo.ID, ``, headers(all)), http.StatusOK, &detail)
	if len(detail.Stops) != 2 || detail.Stops[0].Outcome != "SKIPPED" || detail.Stops[1].Outcome != "DONE" {
		t.Fatalf("detail stops = %+v", detail.Stops)
	}
	var requests struct {
		Items []struct {
			ID      string `json:"id"`
			Source  string `json:"source"`
			AssetID string `json:"assetId"`
		} `json:"items"`
	}
	decodeInto(t, call(t, h, http.MethodGet, "/v1/eam/requests", ``, headers(all)), http.StatusOK, &requests)
	if len(requests.Items) != 1 || requests.Items[0].Source != "ROUND" || requests.Items[0].AssetID != assets[0].ID {
		t.Fatalf("requests = %+v, want one ROUND request on stop 1's asset", requests.Items)
	}
}
