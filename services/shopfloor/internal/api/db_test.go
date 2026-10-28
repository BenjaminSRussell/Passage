package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

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
	first := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", body, allDuty, key("w-1"))
	second := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", body, allDuty, key("w-1"))
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("create: %d %d %s", first.Code, second.Code, second.Body.String())
	}
	var w1, w2 store.Worker
	_ = json.Unmarshal(first.Body.Bytes(), &w1)
	_ = json.Unmarshal(second.Body.Bytes(), &w2)
	if w1.ID == "" || w1.ID != w2.ID {
		t.Fatalf("replay created a second worker: %s vs %s", w1.ID, w2.ID)
	}
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", `{"partyId":"`+jobID+`","name":"Other","costRate":"1","currency":"USD"}`, allDuty, key("w-1")); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with another body: %d", rec.Code)
	}

	regs := fmt.Sprintf(`{"items":[{"id":"0195f0a0-0000-7000-8000-0000000000a1","workerId":%q,"kind":"clock-in","startedAt":"2026-10-20T09:00:00Z"},
		{"id":"0195f0a0-0000-7000-8000-0000000000a2","workerId":%q,"kind":"clock-out","startedAt":"2026-10-20T19:30:00Z"}]}`, w1.ID, w1.ID)
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", regs, allDuty); rec.Code != http.StatusOK {
		t.Fatalf("registrations: %d %s", rec.Code, rec.Body.String())
	}
	rangeBody := fmt.Sprintf(`{"workerId":%q,"from":"2026-10-20","to":"2026-10-20"}`, w1.ID)
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/pay:calculate", rangeBody, allDuty, key("c-1")); rec.Code != http.StatusOK {
		t.Fatalf("calculate: %d %s", rec.Code, rec.Body.String())
	}
	list := call(t, h, http.MethodGet, "/v1/shopfloor/worker-days?from=2026-10-20&to=2026-10-20&status=calculated", "", allDuty)
	var days struct{ Items []store.WorkerDay }
	_ = json.Unmarshal(list.Body.Bytes(), &days)
	if len(days.Items) != 1 || days.Items[0].WorkerName != "Ana Silva" || len(days.Items[0].PayLines) != 2 {
		t.Fatalf("worker-days: %s", list.Body.String())
	}
	approve := callWith(t, h, http.MethodPost, "/v1/shopfloor/pay:approve", rangeBody, allDuty, key("a-1"))
	replay := callWith(t, h, http.MethodPost, "/v1/shopfloor/pay:approve", rangeBody, allDuty, key("a-1"))
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

	// An hour-long idle threshold: the sparse counts below stay run time (BR-25.6).
	body := fmt.Sprintf(`{"machineId":"M-101","resourceId":%q,"siteId":%q,"idealCycleSeconds":"60","idleThresholdSeconds":3600}`, resourceID, siteID)
	first := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, floorDuty, key("m-1"))
	again := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, floorDuty, key("m-1"))
	if first.Code != http.StatusCreated || again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("machine: %d %d %s", first.Code, again.Code, again.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"idleThresholdSeconds":3600`) {
		t.Fatalf("idle threshold not stored: %s", first.Body.String())
	}
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, floorDuty, key("m-2")); rec.Code != http.StatusConflict || problemCode(t, rec) != store.CodeMachineExists {
		t.Fatalf("second machine on the resource: %d %s", rec.Code, rec.Body.String())
	}

	// Recent device times, so nothing is older than the 7-day limit.
	t0 := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	ts := func(min int) string { return t0.Add(time.Duration(min) * time.Minute).Format(time.RFC3339) }
	events := fmt.Sprintf(`{"events":[
		{"machineId":"M-101","seq":1,"kind":"state","ts":%q,"state":"RUNNING"},
		{"machineId":"M-101","seq":2,"kind":"count","ts":%q,"goodCount":9,"rejectCount":1},
		{"machineId":"M-101","seq":3,"kind":"state","ts":%q,"state":"DOWN"}]}`, ts(0), ts(10), ts(30))
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", events, floorDuty)
	var res store.IngestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Accepted != 3 || res.AndonsRaised != 1 || res.DowntimesOpen != 1 {
		t.Fatalf("ingest %d %s", rec.Code, rec.Body.String())
	}

	var andon itemsResponse[store.Andon]
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/andon?siteId="+siteID, "", floorDuty)
	_ = json.Unmarshal(rec.Body.Bytes(), &andon)
	if len(andon.Items) != 1 || andon.Items[0].ResourceID != resourceID {
		t.Fatalf("andon %s", rec.Body.String())
	}
	var downs itemsResponse[store.Downtime]
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/downtimes?open=true", "", floorDuty)
	_ = json.Unmarshal(rec.Body.Bytes(), &downs)
	if len(downs.Items) != 1 || !downs.Items[0].AndonRaised {
		t.Fatalf("downtimes %s", rec.Body.String())
	}
	rec = callWith(t, h, http.MethodPost, "/v1/shopfloor/downtimes/"+downs.Items[0].ID+":code", `{"reasonCode":"JAM"}`, floorDuty, key("d-1"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reasonCode":"JAM"`) {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}

	rec = call(t, h, http.MethodGet, "/v1/shopfloor/oee?resourceIds="+resourceID+"&from="+ts(0)+"&to="+ts(60)+"&bucket=60m", "", floorDuty)
	var series itemsResponse[oeeItem]
	_ = json.Unmarshal(rec.Body.Bytes(), &series)
	if rec.Code != http.StatusOK || len(series.Items) != 1 {
		t.Fatalf("oee %d %s", rec.Code, rec.Body.String())
	}
	if b := series.Items[0]; b.Availability.String() != "0.5" || b.TotalCount != 10 || b.Quality.String() != "0.9" {
		t.Fatalf("bucket %+v", b)
	}
}

// End to end over HTTP and Postgres (BR-25.1, BR-25.7): an offline kiosk whose
// clock is 7 minutes behind replays a two-job bundle; the server corrects the
// times, records the offset, stores the 3:1 split, and the supervisor reads the
// bundle back. A later batch adding a closed job to the bundle is SF-0113.
func TestHTTP_OfflineBundleReplay(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	server := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	h := newRouterAt(store.New(app), func() time.Time { return server })
	created := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", `{"partyId":"`+jobID+`","name":"Ben Ode","costRate":"20","currency":"USD"}`,
		allDuty, map[string]string{"Idempotency-Key": "w-bundle"})
	var w store.Worker
	_ = json.Unmarshal(created.Body.Bytes(), &w)
	item := func(id, job, good string) string {
		return fmt.Sprintf(`{"id":%q,"workerId":%q,"kind":"job-stop","jobId":%q,"bundleId":%q,"startedAt":"2026-10-20T09:00:00Z","stoppedAt":"2026-10-20T11:00:00Z","qtyGood":%q,"offline":true}`,
			id, w.ID, job, bundleID, good)
	}
	batch := fmt.Sprintf(`{"deviceSentAt":"2026-10-20T11:53:00Z","items":[%s,%s]}`, item(regID, jobID, "30"), item(regID2, jobID2, "10"))
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", batch, allDuty)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"clockOffsetSeconds":420`) || !strings.Contains(rec.Body.String(), `"clockCorrected":true`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	list := call(t, h, http.MethodGet, "/v1/shopfloor/registrations?bundleId="+bundleID, "", allDuty)
	var out struct{ Items []store.RegistrationView }
	_ = json.Unmarshal(list.Body.Bytes(), &out)
	if len(out.Items) != 2 {
		t.Fatalf("bundle: %s", list.Body.String())
	}
	hours := map[string]string{}
	for _, r := range out.Items {
		if r.StartedAt.UTC().Format(time.RFC3339) != "2026-10-20T09:07:00Z" || r.ClockOffsetSeconds == nil || *r.ClockOffsetSeconds != 420 {
			t.Fatalf("row not corrected: %+v", r)
		}
		hours[r.ID] = r.Hours.StringFixed(2)
	}
	if hours[regID] != "1.50" || hours[regID2] != "0.50" {
		t.Fatalf("shares %v", hours)
	}
	late := fmt.Sprintf(`{"items":[%s]}`, item("0195f0a0-0000-7000-8000-000000000014", "0195f0a0-0000-7000-8000-000000000015", "5"))
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", late, allDuty); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != "SF-0113" {
		t.Fatalf("late member: %d %s", rec.Code, rec.Body.String())
	}
}

// FR-25.17 over HTTP and Postgres: a kiosk quality result is stored once per
// (source, sourceMessageId), the w33 consumer reports a failure under an
// Idempotency-Key (a retried report answers from the stored outcome and does not
// count twice), and the monitor finds the message by status and destination. Kafka
// is delivered at commit, so a message with other destinations pending is
// partially delivered from the start.
func TestHTTP_ProcessMessageMonitor(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	duties := "shopfloor.process.create,shopfloor.process.deliver,shopfloor.view"
	body := fmt.Sprintf(`{"source":"kiosk","sourceMessageId":"pi-1","categoryCode":"quality-result","orderId":%q,"occurredAt":"2026-10-20T09:00:00Z",
		"values":[{"name":"ph","type":"numeric","value":"7.9","min":"6.5","max":"7.5"}]}`, jobID)
	first := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages", body, duties)
	again := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages", body, duties)
	if first.Code != http.StatusCreated || again.Code != http.StatusOK {
		t.Fatalf("create %d / replay %d: %s", first.Code, again.Code, again.Body.String())
	}
	var created, replayed store.ProcessIngest
	_ = json.Unmarshal(first.Body.Bytes(), &created)
	_ = json.Unmarshal(again.Body.Bytes(), &replayed)
	if !replayed.Duplicate || replayed.Message.ID != created.Message.ID || !created.Message.OutOfLimit {
		t.Fatalf("created %+v replayed %+v", created, replayed)
	}
	report := "/v1/shopfloor/process-messages/" + created.Message.ID + "/deliveries/quality:report"
	key := map[string]string{"Idempotency-Key": "w33-1"}
	r1 := callWith(t, h, http.MethodPost, report, `{"outcome":"failed","error":"QMS unavailable"}`, duties, key)
	r2 := callWith(t, h, http.MethodPost, report, `{"outcome":"failed","error":"QMS unavailable"}`, duties, key)
	if r1.Code != http.StatusOK || r2.Code != http.StatusOK || r2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("report %d / replay %d %s", r1.Code, r2.Code, r2.Body.String())
	}
	list := call(t, h, http.MethodGet, "/v1/shopfloor/process-messages?status=partially-delivered&destination=quality", "", duties)
	var out struct{ Items []store.ProcessMessage }
	_ = json.Unmarshal(list.Body.Bytes(), &out)
	if len(out.Items) != 1 {
		t.Fatalf("monitor: %s", list.Body.String())
	}
	for _, d := range out.Items[0].Deliveries {
		if d.Destination == "quality" && (d.Attempts != 1 || d.LastError == nil || *d.LastError != "QMS unavailable") {
			t.Fatalf("quality delivery %+v", d)
		}
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages/"+created.Message.ID+":reprocess", `{"destination":"quality","reason":"try"}`, duties); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != "SF-0116" {
		t.Fatalf("reprocess of a pending destination: %d %s", rec.Code, rec.Body.String())
	}
}

// Slice 5 over HTTP and Postgres: the retry sweep publishes a failed destination
// again once its wait has passed; a supervisor deletes the message once the
// destination is in error; the monitor pages with a cursor and no longer lists it,
// while reading it by id shows who deleted it and why.
func TestHTTP_ProcessMessageSweepDeleteAndPaging(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	h := newRouterAt(store.New(app), func() time.Time { return now })
	duties := "shopfloor.process.create,shopfloor.process.deliver,shopfloor.view"
	var ids []string
	for i := 0; i < 3; i++ {
		body := fmt.Sprintf(`{"source":"edge","sourceMessageId":"pcs-%d","categoryCode":"quality-result","orderId":%q,"occurredAt":"2026-10-20T09:00:00Z","values":[]}`, i, jobID)
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages", body, duties)
		var res store.ProcessIngest
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("create %d: %d %s", i, rec.Code, rec.Body.String())
		}
		ids = append(ids, res.Message.ID)
		now = now.Add(time.Second)
	}
	target := ids[0]
	report := "/v1/shopfloor/process-messages/" + target + "/deliveries/quality:report"
	if rec := callWith(t, h, http.MethodPost, report, `{"outcome":"failed","error":"QMS down"}`, duties, map[string]string{"Idempotency-Key": "f-0"}); rec.Code != http.StatusOK {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	sweep := "/v1/shopfloor/process-messages:retry-due"
	if rec := call(t, h, http.MethodPost, sweep, "", duties); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("sweep before the wait: %d %s", rec.Code, rec.Body.String())
	}
	now = now.Add(time.Minute)
	if rec := call(t, h, http.MethodPost, sweep, "", duties); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"destination":"quality"`) {
		t.Fatalf("sweep after the wait: %d %s", rec.Code, rec.Body.String())
	}

	del := "/v1/shopfloor/process-messages/" + target + ":delete"
	if rec := callWith(t, h, http.MethodPost, del, `{"reason":"duplicate"}`, duties, map[string]string{"Idempotency-Key": "del-0"}); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != "SF-0119" {
		t.Fatalf("delete while retrying: %d %s", rec.Code, rec.Body.String())
	}
	for i := 1; i < 5; i++ {
		if rec := callWith(t, h, http.MethodPost, report, `{"outcome":"failed"}`, duties, map[string]string{"Idempotency-Key": fmt.Sprintf("f-%d", i)}); rec.Code != http.StatusOK {
			t.Fatalf("report %d: %d", i, rec.Code)
		}
	}
	first := callWith(t, h, http.MethodPost, del, `{"reason":"sample lost"}`, duties, map[string]string{"Idempotency-Key": "del-1"})
	again := callWith(t, h, http.MethodPost, del, `{"reason":"sample lost"}`, duties, map[string]string{"Idempotency-Key": "del-1"})
	if first.Code != http.StatusOK || again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("delete %d / replay %d: %s", first.Code, again.Code, again.Body.String())
	}

	got := call(t, h, http.MethodGet, "/v1/shopfloor/process-messages/"+target, "", duties)
	var m store.ProcessMessage
	_ = json.Unmarshal(got.Body.Bytes(), &m)
	if m.DeletedAt == nil || m.DeletedBy == nil || *m.DeletedBy != "supervisor-1" || *m.DeleteReason != "sample lost" {
		t.Fatalf("deleted message read back: %s", got.Body.String())
	}
	var page struct {
		Items      []store.ProcessMessage `json:"items"`
		NextCursor *string                `json:"nextCursor"`
	}
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/process-messages?limit=1", "", duties)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].ID != ids[2] || page.NextCursor == nil {
		t.Fatalf("first page: %s", rec.Body.String())
	}
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/process-messages?limit=1&cursor="+*page.NextCursor, "", duties)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].ID != ids[1] || page.NextCursor != nil {
		t.Fatalf("second page (the deleted message is not listed): %s", rec.Body.String())
	}
}

// End to end over HTTP and Postgres (FR-25.1, FR-25.14, FR-25.10): a badge
// signs a worker in on the kiosk, the kiosk clocks them in, the supervisor board
// lists them, and the rollup job stores the OEE of every registered machine.
func TestHTTP_KioskBoardAndRollup(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	key := func(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }
	duties := allDuty + "," + DutyKioskLogin + "," + DutyOEERollup + "," + DutyIngest

	rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", `{"partyId":"`+jobID+`","name":"Ana Silva","badgeId":"B-42","costRate":"20","currency":"USD"}`, duties, key("w-1"))
	var w store.Worker
	_ = json.Unmarshal(rec.Body.Bytes(), &w)
	if rec.Code != http.StatusCreated {
		t.Fatalf("worker %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":"B-99"}`, duties); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeBadgeUnknown {
		t.Fatalf("unknown badge %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":"B-42"}`, duties)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"workerId":"`+w.ID+`"`) || !strings.Contains(rec.Body.String(), `"clockedInAt":null`) {
		t.Fatalf("badge %d %s", rec.Code, rec.Body.String())
	}
	clockIn := fmt.Sprintf(`{"items":[{"id":"0195f0a0-0000-7000-8000-0000000000c1","workerId":%q,"kind":"clock-in","startedAt":"2026-10-21T07:00:00Z"}]}`, w.ID)
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", clockIn, duties); rec.Code != http.StatusOK {
		t.Fatalf("clock-in %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/clocked-in", "", duties)
	var board itemsResponse[store.ClockedWorker]
	_ = json.Unmarshal(rec.Body.Bytes(), &board)
	if rec.Code != http.StatusOK || len(board.Items) != 1 || board.Items[0].Name != "Ana Silva" || len(board.Items[0].Jobs) != 0 {
		t.Fatalf("board %d %s", rec.Code, rec.Body.String())
	}

	body := fmt.Sprintf(`{"machineId":"M-201","resourceId":%q,"idealCycleSeconds":"60"}`, resourceID)
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, duties, key("m-1")); rec.Code != http.StatusCreated {
		t.Fatalf("machine %d %s", rec.Code, rec.Body.String())
	}
	t0 := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	ts := func(min int) string { return t0.Add(time.Duration(min) * time.Minute).Format(time.RFC3339) }
	events := fmt.Sprintf(`{"events":[
		{"machineId":"M-201","seq":1,"kind":"state","ts":%q,"state":"RUNNING"},
		{"machineId":"M-201","seq":2,"kind":"count","ts":%q,"goodCount":4},
		{"machineId":"M-201","seq":3,"kind":"count","ts":%q,"goodCount":4},
		{"machineId":"M-201","seq":4,"kind":"state","ts":%q,"state":"OFF"}]}`, ts(0), ts(4), ts(8), ts(15))
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", events, duties); rec.Code != http.StatusOK {
		t.Fatalf("ingest %d %s", rec.Code, rec.Body.String())
	}
	window := fmt.Sprintf(`{"from":%q,"to":%q}`, ts(0), ts(30))
	for i, want := range []int{1, 0} {
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/oee:rollup", window, duties)
		var res store.OEERollupResult
		_ = json.Unmarshal(rec.Body.Bytes(), &res)
		if rec.Code != http.StatusOK || res.Computed != 1 || res.Changed != want {
			t.Fatalf("rollup %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	// 0-8 run with counts every 4 minutes, 8-15 idle (7 min without a count to OFF).
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/oee?resourceIds="+resourceID+"&from="+ts(0)+"&to="+ts(30)+"&bucket=15m", "", duties)
	var series itemsResponse[oeeItem]
	_ = json.Unmarshal(rec.Body.Bytes(), &series)
	if len(series.Items) != 1 || !series.Items[0].RunMin.Equal(decimal.NewFromInt(8)) || !series.Items[0].DownMin.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("oee %s", rec.Body.String())
	}
}

// FR-25.3 to FR-25.5 over HTTP and Postgres: the schedule writes a job with a mandatory
// numeric check, the operator sees it on the dispatch list, a finish with 13 against
// limits 10 to 12 is SF-0005 (AC-25.9), the supervisor's override finishes it, and a
// resend of the operator's feedback is a 200 replay.
func TestHTTP_JobDispatchFeedbackAndOverride(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	operator := DutyRegister
	supervisor := DutyRegister + "," + DutyCheckOverride
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/jobs", upsertBody, DutyJobSchedule); rec.Code != http.StatusCreated {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}
	list := call(t, h, http.MethodGet, "/v1/shopfloor/jobs?resourceId="+jobResource, "", operator)
	var jobs struct{ Items []store.Job }
	_ = json.Unmarshal(list.Body.Bytes(), &jobs)
	if list.Code != http.StatusOK || len(jobs.Items) != 1 || jobs.Items[0].Status != store.JobReleased || jobs.Items[0].PlannedQty.String() != "100" {
		t.Fatalf("dispatch list: %d %s", list.Code, list.Body.String())
	}

	report := `{"feedbackId":"0195f0a0-0000-7000-8000-0000000000f1","goodQuantity":"40.25"}`
	if rec := call(t, h, http.MethodPost, feedbackPath(), report, operator); rec.Code != http.StatusCreated {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, feedbackPath(), report, operator); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	finish := `{"feedbackId":"0195f0a0-0000-7000-8000-0000000000f2","finished":true,"checks":[{"code":"len","value":"13"}]%s}`
	rec := call(t, h, http.MethodPost, feedbackPath(), fmt.Sprintf(finish, ""), operator)
	if code, _ := problemOf(t, rec.Body.Bytes()); rec.Code != http.StatusUnprocessableEntity || code != "SF-0005" {
		t.Fatalf("blocked finish: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, http.MethodPost, feedbackPath(), fmt.Sprintf(finish, `,"override":{"reason":"Customer deviation D-17 approved"}`), supervisor)
	var out store.FeedbackResult
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || out.Job.Status != store.JobFinished || out.Job.GoodQty.String() != "40.25" ||
		len(out.Job.Results) != 1 || out.Job.Results[0].Pass {
		t.Fatalf("override: %d %s", rec.Code, rec.Body.String())
	}
	detail := call(t, h, http.MethodGet, "/v1/shopfloor/jobs/"+jobID, "", operator)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"status":"finished"`) {
		t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
	}
}

// FR-25.3 and BR-25.7 over HTTP and Postgres: a kiosk job-start moves the job to
// started; the next start answers with the auto-stopped job and SF-0002; the badge
// session lists the open job; a start for a finished job is flagged with SF-0004.
func TestHTTP_JobStartAutoStopAndFinishedConflict(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := newRouter(store.New(app))
	duties := allDuty + "," + DutyKioskLogin + "," + DutyJobSchedule
	rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/workers", `{"partyId":"`+jobID+`","name":"Ana Silva","badgeId":"B-7","costRate":"20","currency":"USD"}`,
		duties, map[string]string{"Idempotency-Key": "w-7"})
	var w store.Worker
	_ = json.Unmarshal(rec.Body.Bytes(), &w)
	second := "0195f0a0-0000-7000-8000-0000000000e2"
	for _, id := range []string{jobID, second} {
		body := strings.Replace(upsertBody, jobID, id, 1)
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/jobs", body, duties); rec.Code != http.StatusCreated {
			t.Fatalf("plan %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	start := func(regID, job, at string) registrationResponse {
		body := fmt.Sprintf(`{"items":[{"id":%q,"workerId":%q,"kind":"job-start","jobId":%q,"startedAt":%q}]}`, regID, w.ID, job, at)
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", body, duties)
		var out registrationResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || len(out.Items) != 1 {
			t.Fatalf("start %s: %d %s", job, rec.Code, rec.Body.String())
		}
		return out
	}
	first := start("0195f0a0-0000-7000-8000-0000000000d1", jobID, "2026-10-21T08:00:00Z")
	if first.Items[0].Status != store.StatusAccepted || len(first.Items[0].AutoStopped) != 0 {
		t.Fatalf("first start %+v", first.Items[0])
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/jobs/"+jobID, "", duties); !strings.Contains(rec.Body.String(), `"status":"started"`) {
		t.Fatalf("job after start %s", rec.Body.String())
	}
	next := start("0195f0a0-0000-7000-8000-0000000000d2", second, "2026-10-21T09:15:00Z")
	got := next.Items[0]
	if len(got.AutoStopped) != 1 || got.AutoStopped[0].JobID != jobID || got.AutoStopped[0].Hours.String() != "1.25" ||
		len(got.Warnings) != 1 || got.Warnings[0].Code != "SF-0002" {
		t.Fatalf("auto-stop %+v", got)
	}
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":"B-7"}`, duties)
	if !strings.Contains(rec.Body.String(), `"openJobs":[{"jobId":"`+second+`"`) {
		t.Fatalf("session %s", rec.Body.String())
	}

	finish := `{"feedbackId":"0195f0a0-0000-7000-8000-0000000000f9","finished":true,"checks":[{"code":"len","value":"11"}]}`
	if rec := call(t, h, http.MethodPost, feedbackPath(), finish, duties); rec.Code != http.StatusCreated {
		t.Fatalf("finish %d %s", rec.Code, rec.Body.String())
	}
	conflict := start("0195f0a0-0000-7000-8000-0000000000d3", jobID, "2026-10-21T10:00:00Z").Items[0]
	if len(conflict.Flags) != 1 || conflict.Flags[0] != "job-finished-conflict" || conflict.Warnings[0].Code != "SF-0004" {
		t.Fatalf("conflict %+v", conflict)
	}
}
