package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

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
