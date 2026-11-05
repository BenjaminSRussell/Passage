package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

func actionBody(actions string) string {
	return `{"technicianId":"` + uuid1 + `","actions":[` + actions + `]}`
}

func TestRecordActions_RequiresTechnicianDuty(t *testing.T) {
	h := newTestRouter()
	path := "/v1/service/work-orders/" + uuid1 + "/actions"
	for _, duty := range []string{"fieldservice.work-order.maintain", "fieldservice.technician.dispatch", "fieldservice.inquire"} {
		if rec := do(t, h, http.MethodPost, path, actionBody(""), nil, duty); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", duty, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodGet, path, "", nil, "fieldservice.work-order.execute"); rec.Code != http.StatusForbidden {
		t.Errorf("reading the log with fieldservice.work-order.execute only: status %d, want 403", rec.Code)
	}
}

func TestRecordActions_ValidatesEachAction(t *testing.T) {
	h := newTestRouter()
	path := "/v1/service/work-orders/" + uuid1 + "/actions"
	now := time.Now().UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	good := `"id":"` + uuid1 + `","deviceTime":"` + now + `"`
	cases := map[string]string{
		"actions[0]": `{"id":"nope","kind":"WORK_START","deviceTime":"` + now + `"}`,
		"actions[1]": `{` + good + `,"kind":"DANCE"}`,
		"actions[2]": `{"id":"` + uuid1 + `","kind":"HOLD","deviceTime":"` + future + `"}`,
		"actions[3]": `{` + good + `,"kind":"PART_USED","lineId":"` + uuid1 + `","qty":"0"}`,
		"actions[4]": `{` + good + `,"kind":"PART_USED","qty":"1"}`,
		"actions[5]": `{` + good + `,"kind":"SIGNATURE","signedBy":""}`,
		"actions[6]": `{` + good + `,"kind":"NOTE","text":"` + strings.Repeat("x", maxDescription+1) + `"}`,
	}
	var all []string
	for i := 0; i < len(cases); i++ {
		all = append(all, cases["actions["+string(rune('0'+i))+"]"])
	}
	rec := do(t, h, http.MethodPost, path, actionBody(strings.Join(all, ",")), nil, "fieldservice.work-order.execute")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	_, fields := problemOf(t, rec)
	for key := range cases {
		if fields[key] == "" {
			t.Errorf("errors %v lack %s", fields, key)
		}
	}
	empty := do(t, h, http.MethodPost, path, actionBody(""), nil, "fieldservice.work-order.execute")
	if _, fields := problemOf(t, empty); fields["actions"] == "" {
		t.Errorf("an empty batch: %v", fields)
	}
}

func TestTransition_RefusesSchedulingStatuses(t *testing.T) {
	h := newTestRouter()
	for _, to := range []string{"SCHEDULED", "DISPATCHED"} {
		rec := do(t, h, http.MethodPost, "/v1/service/work-orders/"+uuid1+":transition", `{"to":"`+to+`"}`, map[string]string{"If-Match": `"1"`}, "fieldservice.work-order.maintain")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", to, rec.Code, rec.Body.String())
		}
	}
}

// BR-45.6 over HTTP: assign and publish are the only way to DISPATCHED; the device then
// syncs an out-of-order batch, a replay changes nothing, a stale move is superseded
// with SVC-SYNC-001, and the log reads back in merge order.
func TestAPI_ActionLogFlow(t *testing.T) {
	h, ctx := dbRouter(t)
	terr, le := ids.New(), ids.New()
	tech := decodeInto[store.Technician](t, call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"BOB","name":"Bob","legalEntityId":"`+le+`","territoryIds":["`+terr+
		`"],"timeZone":"UTC","shiftDays":[1,2,3,4,5,6,7]}`, map[string]string{"Idempotency-Key": "t"}).Body.String())
	line := ids.New()
	created := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", `{"customerPartyId":"`+ids.New()+`","legalEntityId":"`+le+`","territoryId":"`+terr+
		`","estDurationMinutes":60,"currency":"USD","signatureExempt":true,"lines":[{"id":"`+line+`","kind":"PART","qty":"2","unitPrice":"10"}]}`, map[string]string{"Idempotency-Key": "w"})
	wo := decodeInto[store.WorkOrder](t, created.Body.String())
	path := "/v1/service/work-orders/" + wo.ID

	if rec := call(t, h, ctx, http.MethodPost, path+":transition", `{"to":"DISPATCHED"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("manual DISPATCHED: %d", rec.Code)
	}
	if rec := call(t, h, ctx, http.MethodPost, path+":assign", `{"technicianId":"`+tech.ID+`","start":"2026-10-26T14:00:00Z"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, ctx, http.MethodPost, "/v1/service/dispatch/publish", `{"legalEntityId":"`+le+`","territoryId":"`+terr+`","date":"2026-10-26","timeZone":"UTC"}`,
		map[string]string{"Idempotency-Key": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	// Device times sit after the order was created (earlier ones are refused, SVC-SYNC-006).
	base := time.Now().UTC().Truncate(time.Minute)
	dt := func(m int) string { return base.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	a1, a2, a3, a4 := ids.New(), ids.New(), ids.New(), ids.New()
	batch := `{"technicianId":"` + tech.ID + `","actions":[` +
		`{"id":"` + a4 + `","kind":"COMPLETE","deviceTime":"` + dt(3) + `"},` +
		`{"id":"` + a2 + `","kind":"PART_USED","deviceTime":"` + dt(2) + `","lineId":"` + line + `","qty":"2"},` +
		`{"id":"` + a1 + `","kind":"WORK_START","deviceTime":"` + dt(0) + `"},` +
		`{"id":"` + a3 + `","kind":"TRAVEL_START","deviceTime":"` + dt(1) + `"}]}`
	rec := call(t, h, ctx, http.MethodPost, path+"/actions", batch, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	res := decodeInto[syncResponse](t, rec.Body.String())
	if res.Applied != 3 || res.Superseded != 1 || res.WorkOrder.Status != "COMPLETED" || !strings.HasPrefix(res.Message, "SVC-SYNC-001") {
		t.Fatalf("merge %+v", res)
	}
	if res.Results[0].ID != a1 || res.Results[3].ID != a4 {
		t.Fatalf("results are not in device-time order: %+v", res.Results)
	}
	again := decodeInto[syncResponse](t, call(t, h, ctx, http.MethodPost, path+"/actions", batch, nil).Body.String())
	if again.Duplicates != 4 || again.Applied != 0 || again.WorkOrder.Version != res.WorkOrder.Version {
		t.Fatalf("replay %+v", again)
	}
	list := call(t, h, ctx, http.MethodGet, path+"/actions", "", nil)
	log := decodeInto[struct{ Items []store.Action }](t, list.Body.String())
	if list.Code != http.StatusOK || len(log.Items) != 4 || log.Items[0].Kind != "WORK_START" {
		t.Fatalf("log %d %s", list.Code, list.Body.String())
	}
}
