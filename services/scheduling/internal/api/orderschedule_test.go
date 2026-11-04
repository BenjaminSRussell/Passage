package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const scheduleOrderID = "00000000-0000-0000-0000-0000000000c1"

func scheduleOrderPath(id string) string { return "/v1/scheduling/orders/" + id + ":schedule" }

func TestScheduleOrder_RunsTheOrderScopeWithDefaults(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodPost, scheduleOrderPath(strings.ToUpper(scheduleOrderID)), `{}`, "scheduling.run", "o1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got := f.lastRun
	if got.Scope != "order" || got.OrderID != scheduleOrderID || got.Mode != "jobs" || !got.Finite || got.Backward || len(got.OperationIDs) != 0 {
		t.Fatalf("run request = %+v, want scope order for the lower-cased order, jobs, finite, forward", got)
	}
	if got.HorizonDays != DefaultConfig.HorizonDays {
		t.Fatalf("horizon %d, want the configured default %d", got.HorizonDays, DefaultConfig.HorizonDays)
	}
}

func TestScheduleOrder_PassesOptions(t *testing.T) {
	f := &fakeStore{}
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Minute)
	body := `{"finite":false,"direction":"backward","horizonDays":90,"start":"` + start.Format(time.RFC3339) + `"}`
	if rec := do(newRouter(f), http.MethodPost, scheduleOrderPath(scheduleOrderID), body, "scheduling.run", "o2"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	got := f.lastRun
	if got.Finite || !got.Backward || got.HorizonDays != 90 || !got.Start.Equal(start) {
		t.Fatalf("run request = %+v, want infinite, backward, 90 days from %s", got, start)
	}
}

func TestScheduleOrder_FingerprintsTheOrder(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	do(h, http.MethodPost, scheduleOrderPath(scheduleOrderID), `{}`, "scheduling.run", "same")
	first := f.lastIdem
	do(h, http.MethodPost, scheduleOrderPath("00000000-0000-0000-0000-0000000000c2"), `{}`, "scheduling.run", "same")
	if first.Key != "same" || first.Fingerprint == f.lastIdem.Fingerprint {
		t.Fatalf("one key on two orders must fingerprint differently: %+v vs %+v", first, f.lastIdem)
	}
}

func TestScheduleOrder_ReplayIsFlagged(t *testing.T) {
	rec := do(newRouter(&fakeStore{replay: true}), http.MethodPost, scheduleOrderPath(scheduleOrderID), `{}`, "scheduling.run", "o3")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
}

func TestScheduleOrder_Refusals(t *testing.T) {
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	cases := []struct {
		name, path, body, duties, key string
		status                        int
		field                         string
	}{
		{"maintain schedule alone is not enough", scheduleOrderPath(scheduleOrderID), `{}`, "scheduling.read,scheduling.schedule.write", "r1", http.StatusForbidden, ""},
		{"no idempotency key", scheduleOrderPath(scheduleOrderID), `{}`, "scheduling.run", "", http.StatusBadRequest, ""},
		{"order id is not a uuid", scheduleOrderPath("PO-1"), `{}`, "scheduling.run", "r2", http.StatusNotFound, ""},
		{"unknown direction", scheduleOrderPath(scheduleOrderID), `{"direction":"sideways"}`, "scheduling.run", "r3", http.StatusBadRequest, "direction"},
		{"horizon too short", scheduleOrderPath(scheduleOrderID), `{"horizonDays":7}`, "scheduling.run", "r4", http.StatusBadRequest, "horizonDays"},
		{"start before today", scheduleOrderPath(scheduleOrderID), `{"start":"` + yesterday + `"}`, "scheduling.run", "r5", http.StatusBadRequest, "start"},
	}
	for _, c := range cases {
		f := &fakeStore{}
		rec := do(newRouter(f), http.MethodPost, c.path, c.body, c.duties, c.key)
		if rec.Code != c.status {
			t.Fatalf("%s: status %d, want %d (body %s)", c.name, rec.Code, c.status, rec.Body)
		}
		if c.field != "" && !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Fatalf("%s: body %s does not name %s", c.name, rec.Body, c.field)
		}
		if f.lastRun.Scope != "" {
			t.Fatalf("%s: the store was called with %+v", c.name, f.lastRun)
		}
	}
}

func TestScheduleOrder_StoreProblemPassesThrough(t *testing.T) {
	f := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeOrderHasNoOperations, "no operations are known for that order")}
	rec := do(newRouter(f), http.MethodPost, scheduleOrderPath(scheduleOrderID), `{}`, "scheduling.run", "p1")
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != store.CodeOrderHasNoOperations {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	f.err = errors.New("database down")
	if rec := do(newRouter(f), http.MethodPost, scheduleOrderPath(scheduleOrderID), `{}`, "scheduling.run", "p2"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: status %d", rec.Code)
	}
}
