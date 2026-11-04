package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const otherRunID = "22222222-2222-4222-8222-222222222222"

func (f *fakeStore) CompareRuns(_ context.Context, baseID, otherID string) (store.RunComparison, error) {
	f.lastCompare = [2]string{baseID, otherID}
	if f.err != nil {
		return store.RunComparison{}, f.err
	}
	return store.RunComparison{Base: store.RunSide{Run: store.Run{ID: baseID}}, Other: store.RunSide{Run: store.Run{ID: otherID}},
		ChangedCount: 2, Added: []store.CompareJob{}, Removed: []store.CompareJob{}, Changed: []store.CompareChange{}}, nil
}

func (f *fakeStore) RollbackRun(_ context.Context, runID string, idem store.Idem) (store.RollbackOutcome, bool, error) {
	f.lastRollback, f.lastIdem = runID, idem
	if f.err != nil {
		return store.RollbackOutcome{}, false, f.err
	}
	return store.RollbackOutcome{Run: store.Run{ID: otherRunID, Scope: "rollback", RollbackOf: &runID}, Restored: 4, Skipped: []store.SkippedJob{}}, f.replay, nil
}

func TestCompareRuns_PassesBothRunsAndRefusesBadInput(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodGet, "/v1/scheduling/runs/"+strings.ToUpper(resID)+"/compare?to="+otherRunID, "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changedCount":2`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastCompare != [2]string{resID, otherRunID} {
		t.Fatalf("store got %v", f.lastCompare)
	}
	for name, path := range map[string]string{
		"no to":    "/v1/scheduling/runs/" + resID + "/compare",
		"bad to":   "/v1/scheduling/runs/" + resID + "/compare?to=x",
		"same run": "/v1/scheduling/runs/" + resID + "/compare?to=" + resID,
	} {
		if rec := do(h, http.MethodGet, path, "", "scheduling.read", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/runs/x/compare?to="+otherRunID, "", "scheduling.read", ""); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/v1/scheduling/runs/"+resID+"/compare?to="+otherRunID, "", "other.duty", ""); rec.Code != http.StatusForbidden {
		t.Errorf("without the read duty: status %d", rec.Code)
	}
}

func TestRollbackRun_NeedsRunDutyAndIdempotencyKey(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/scheduling/runs/" + strings.ToUpper(resID) + ":rollback"
	if rec := do(h, http.MethodPost, path, "{}", "scheduling.run", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("without a key: status %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, path, "{}", "scheduling.read", "k1"); rec.Code != http.StatusForbidden {
		t.Errorf("without the run duty: status %d", rec.Code)
	}
	rec := do(h, http.MethodPost, path, "{}", "scheduling.run", "k1")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"restored":4`) || !strings.Contains(rec.Body.String(), `"scope":"rollback"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if f.lastRollback != resID || f.lastIdem.Key != "k1" {
		t.Fatalf("store got %q %+v", f.lastRollback, f.lastIdem)
	}
	first := f.lastIdem.Fingerprint
	do(h, http.MethodPost, "/v1/scheduling/runs/"+otherRunID+":rollback", "{}", "scheduling.run", "k1")
	if f.lastIdem.Fingerprint == first {
		t.Errorf("the fingerprint must include the run in the path")
	}
	f.replay = true
	rec = do(h, http.MethodPost, path, "{}", "scheduling.run", "k1")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay: status %d header %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
	if rec := do(h, http.MethodPost, "/v1/scheduling/runs/x:rollback", "{}", "scheduling.run", "k2"); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
}
