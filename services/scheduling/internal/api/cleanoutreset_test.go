package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func (f *fakeStore) ListCleanoutCounters(context.Context) ([]store.CleanoutCounter, error) {
	return []store.CleanoutCounter{}, f.err
}

func (f *fakeStore) GetCleanoutCounter(_ context.Context, rid string) (store.CleanoutCounterDetail, error) {
	return store.CleanoutCounterDetail{CleanoutCounter: store.CleanoutCounter{ResourceID: rid}, Resets: []store.CleanoutReset{}}, f.err
}

func (f *fakeStore) ResetCleanoutCounter(_ context.Context, in store.CleanoutResetInput, idem store.Idem) (store.CleanoutResetResult, bool, error) {
	f.lastReset = in
	f.lastIdem = idem
	if f.err != nil {
		return store.CleanoutResetResult{}, false, f.err
	}
	return store.CleanoutResetResult{CleanoutReset: store.CleanoutReset{ResourceID: in.ResourceID, Reason: in.Reason}, StaleCampaignIDs: []string{}}, !f.replay, nil
}

const counterPath = "/v1/scheduling/resources/" + parentOrder + "/cleanout-counter"

func TestResetCleanoutCounter_ValidatesAndRequiresKeyAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	ok := `{"batchesSince":3,"minutesSince":180,"reason":" unplanned wash-down "}`
	if rec := do(h, http.MethodPost, counterPath+":reset", ok, allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("without a key: status %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, counterPath+":reset", ok, "scheduling.read", "k0"); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: status %d, want 403", rec.Code)
	}
	bad := map[string]string{
		"batchesSince": `{"minutesSince":0,"reason":"x"}`,
		"minutesSince": `{"batchesSince":0,"minutesSince":-1,"reason":"x"}`,
		"reason":       `{"batchesSince":0,"minutesSince":0,"reason":"  "}`,
	}
	for field, body := range bad {
		rec := do(h, http.MethodPost, counterPath+":reset", body, allDuties, "k-"+field)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: status %d body %s", field, rec.Code, rec.Body)
		}
	}
	long := `{"batchesSince":0,"minutesSince":0,"reason":"` + strings.Repeat("x", 201) + `"}`
	if rec := do(h, http.MethodPost, counterPath+":reset", long, allDuties, "k-long"); rec.Code != http.StatusBadRequest {
		t.Errorf("reason over 200 characters: status %d", rec.Code)
	}
	// The limit is 200 characters, as the table checks, not 200 bytes.
	accented := `{"batchesSince":0,"minutesSince":0,"reason":"` + strings.Repeat("é", 200) + `"}`
	if rec := do(h, http.MethodPost, counterPath+":reset", accented, allDuties, "k-accented"); rec.Code != http.StatusCreated {
		t.Errorf("reason of 200 accented characters: status %d body %s", rec.Code, rec.Body)
	}
	rec := do(h, http.MethodPost, counterPath+":reset", ok, allDuties, "k1")
	if rec.Code != http.StatusCreated || f.lastReset.ResourceID != parentOrder || f.lastReset.SeenBatches != 3 || f.lastReset.SeenMinutes != 180 || f.lastReset.Reason != "unplanned wash-down" {
		t.Fatalf("status %d, reset %+v", rec.Code, f.lastReset)
	}
	first := f.lastIdem.Fingerprint
	if rec := do(h, http.MethodPost, "/v1/scheduling/resources/"+dependentOrder+"/cleanout-counter:reset", ok, allDuties, "k1"); rec.Code != http.StatusCreated || f.lastIdem.Fingerprint == first {
		t.Fatalf("other resource: status %d, fingerprint reused %v", rec.Code, f.lastIdem.Fingerprint == first)
	}
	if rec := do(newRouter(&fakeStore{replay: true}), http.MethodPost, counterPath+":reset", ok, allDuties, "k1"); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d", rec.Code)
	}
	moved := httpx.NewProblem(http.StatusConflict, store.CodeCleanoutCountSeen, "moved")
	if rec := do(newRouter(&fakeStore{err: moved}), http.MethodPost, counterPath+":reset", ok, allDuties, "k2"); rec.Code != http.StatusConflict {
		t.Fatalf("moved counter: status %d", rec.Code)
	}
}

func TestCleanoutCounterReads(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := do(h, http.MethodGet, "/v1/scheduling/cleanout-counters", "", "scheduling.read", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("list: status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/cleanout-counters", "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list without the duty: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, counterPath, "", "scheduling.read", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), parentOrder) {
		t.Fatalf("get: status %d body %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/resources/nope/cleanout-counter", "", "scheduling.read", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}
