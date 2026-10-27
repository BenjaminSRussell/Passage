package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

// End to end over HTTP and Postgres (skipped without PASSAGE_TEST_DATABASE_URL):
// a worker is created once per Idempotency-Key, clocks a 10.5 h Tuesday, and the
// supervisor calculates, lists and approves the day.
func TestHTTP_WorkerToApprovedDay(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	key := func(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

	body := `{"partyId":"` + jobID + `","name":"Ana Silva","badgeId":"B-1","costRate":"20","currency":"USD"}`
	first := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/workers", body, allDuty, key("w-1"))
	second := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/workers", body, allDuty, key("w-1"))
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("create: %d %d %s", first.Code, second.Code, second.Body.String())
	}
	var w1, w2 store.Worker
	_ = json.Unmarshal(first.Body.Bytes(), &w1)
	_ = json.Unmarshal(second.Body.Bytes(), &w2)
	if w1.ID == "" || w1.ID != w2.ID {
		t.Fatalf("replay created a second worker: %s vs %s", w1.ID, w2.ID)
	}
	if rec := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/workers", `{"partyId":"`+jobID+`","name":"Other","costRate":"1","currency":"USD"}`, allDuty, key("w-1")); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with another body: %d", rec.Code)
	}

	regs := fmt.Sprintf(`{"items":[{"id":"0195f0a0-0000-7000-8000-0000000000a1","workerId":%q,"kind":"clock-in","startedAt":"2026-10-20T09:00:00Z"},
		{"id":"0195f0a0-0000-7000-8000-0000000000a2","workerId":%q,"kind":"clock-out","startedAt":"2026-10-20T19:30:00Z"}]}`, w1.ID, w1.ID)
	if rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/registrations", regs, allDuty); rec.Code != http.StatusOK {
		t.Fatalf("registrations: %d %s", rec.Code, rec.Body.String())
	}
	rangeBody := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, w1.ID)
	if rec := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/pay:calculate", rangeBody, allDuty, key("c-1")); rec.Code != http.StatusOK {
		t.Fatalf("calculate: %d %s", rec.Code, rec.Body.String())
	}
	list := call(t, h, http.MethodGet, "/api/v1/shopfloor/worker-days?from=2026-10-20&to=2026-10-20&status=calculated", "", allDuty)
	var days struct{ Items []store.WorkerDay }
	_ = json.Unmarshal(list.Body.Bytes(), &days)
	if len(days.Items) != 1 || days.Items[0].WorkerName != "Ana Silva" || len(days.Items[0].PayLines) != 2 {
		t.Fatalf("worker-days: %s", list.Body.String())
	}
	approve := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/pay:approve", rangeBody, allDuty, key("a-1"))
	replay := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/pay:approve", rangeBody, allDuty, key("a-1"))
	if approve.Code != http.StatusOK || canonical(t, replay.Body.Bytes()) != canonical(t, approve.Body.Bytes()) {
		t.Fatalf("approve %d %s / replay %s", approve.Code, approve.Body.String(), replay.Body.String())
	}
	var out store.ApproveResult
	_ = json.Unmarshal(approve.Body.Bytes(), &out)
	if len(out.Days) != 1 || !out.Days[0].NewlyApproved {
		t.Fatalf("approve result %+v", out)
	}
}

// canonical re-encodes JSON with sorted keys (jsonb does not keep key order).
func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// End to end over HTTP and Postgres: a machine is registered once per
// Idempotency-Key, the edge posts a DOWN that lasts past the andon threshold,
// the supervisor sees the open downtime and andon, codes the reason, and the
// OEE series reflects the stop.
func TestHTTP_MachineDowntimeAndOEE(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	key := func(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

	body := fmt.Sprintf(`{"machineId":"M-101","resourceId":%q,"siteId":%q,"idealCycleSeconds":"60"}`, resourceID, siteID)
	first := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/machines", body, floorDuty, key("m-1"))
	again := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/machines", body, floorDuty, key("m-1"))
	if first.Code != http.StatusCreated || again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("machine: %d %d %s", first.Code, again.Code, again.Body.String())
	}
	if rec := callWith(t, h, http.MethodPost, "/api/v1/shopfloor/machines", body, floorDuty, key("m-2")); rec.Code != http.StatusConflict || problemCode(t, rec) != store.CodeMachineExists {
		t.Fatalf("second machine on the resource: %d %s", rec.Code, rec.Body.String())
	}

	// Recent device times, so nothing is older than the 7-day limit.
	t0 := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	ts := func(min int) string { return t0.Add(time.Duration(min) * time.Minute).Format(time.RFC3339) }
	events := fmt.Sprintf(`{"events":[
		{"machineId":"M-101","seq":1,"kind":"state","ts":%q,"state":"RUNNING"},
		{"machineId":"M-101","seq":2,"kind":"count","ts":%q,"goodCount":9,"rejectCount":1},
		{"machineId":"M-101","seq":3,"kind":"state","ts":%q,"state":"DOWN"}]}`, ts(0), ts(10), ts(30))
	rec := call(t, h, http.MethodPost, "/api/v1/shopfloor/machine-events", events, floorDuty)
	var res store.IngestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Accepted != 3 || res.AndonsRaised != 1 || res.DowntimesOpen != 1 {
		t.Fatalf("ingest %d %s", rec.Code, rec.Body.String())
	}

	var andon itemsResponse[store.Andon]
	rec = call(t, h, http.MethodGet, "/api/v1/shopfloor/andon?siteId="+siteID, "", floorDuty)
	_ = json.Unmarshal(rec.Body.Bytes(), &andon)
	if len(andon.Items) != 1 || andon.Items[0].ResourceID != resourceID {
		t.Fatalf("andon %s", rec.Body.String())
	}
	var downs itemsResponse[store.Downtime]
	rec = call(t, h, http.MethodGet, "/api/v1/shopfloor/downtimes?open=true", "", floorDuty)
	_ = json.Unmarshal(rec.Body.Bytes(), &downs)
	if len(downs.Items) != 1 || !downs.Items[0].AndonRaised {
		t.Fatalf("downtimes %s", rec.Body.String())
	}
	rec = callWith(t, h, http.MethodPost, "/api/v1/shopfloor/downtimes/"+downs.Items[0].ID+":code", `{"reasonCode":"JAM"}`, floorDuty, key("d-1"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reasonCode":"JAM"`) {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, h, http.MethodGet, "/api/v1/shopfloor/oee?resourceIds="+resourceID+"&from="+ts(0)+"&to="+ts(60)+"&bucket=60m", "", floorDuty)
	var series itemsResponse[oeeItem]
	_ = json.Unmarshal(rec.Body.Bytes(), &series)
	if rec.Code != http.StatusOK || len(series.Items) != 1 {
		t.Fatalf("oee %d %s", rec.Code, rec.Body.String())
	}
	if b := series.Items[0]; b.Availability.String() != "0.5" || b.TotalCount != 10 || b.Quality.String() != "0.9" {
		t.Fatalf("bucket %+v", b)
	}
}
