package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// Order pegs on the fake store (FR-23.8); lastPeg records what reached it.
func (f *fakeStore) CreatePeg(_ context.Context, in store.Peg, _ store.Idem) (store.Peg, bool, error) {
	f.lastPeg = in
	if f.err != nil {
		return store.Peg{}, false, f.err
	}
	in.ID = ids.New()
	return in, !f.replay, nil
}

func (f *fakeStore) ListPegs(_ context.Context, orderID string) ([]store.Peg, error) {
	f.lastPeg = store.Peg{ParentOrderID: orderID}
	return []store.Peg{}, f.err
}

func (f *fakeStore) DeletePeg(_ context.Context, id string) error {
	f.lastPeg = store.Peg{ID: id}
	return f.err
}

const (
	parentOrder    = "00000000-0000-0000-0000-0000000000c1"
	dependentOrder = "00000000-0000-0000-0000-0000000000c2"
)

func TestCreatePeg_ValidatesAndRequiresKeyAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"parentOrderId":"` + strings.ToUpper(parentOrder) + `","dependentOrderId":"` + dependentOrder + `"}`
	if rec := do(h, http.MethodPost, "/v1/scheduling/order-pegs", body, allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("without a key: status %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/v1/scheduling/order-pegs", body, "scheduling.read", "k0"); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: status %d, want 403", rec.Code)
	}
	cases := map[string]string{
		"parentOrderId":    `{"parentOrderId":"x","dependentOrderId":"` + dependentOrder + `"}`,
		"dependentOrderId": `{"parentOrderId":"` + parentOrder + `","dependentOrderId":"` + parentOrder + `"}`,
	}
	for field, bad := range cases {
		rec := do(h, http.MethodPost, "/v1/scheduling/order-pegs", bad, allDuties, "k1")
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[field] == "" {
			t.Fatalf("%s: status %d: %s", field, rec.Code, rec.Body)
		}
	}
	rec := do(h, http.MethodPost, "/v1/scheduling/order-pegs", body, allDuties, "k2")
	if rec.Code != http.StatusCreated || f.lastPeg.ParentOrderID != parentOrder || f.lastPeg.DependentOrderID != dependentOrder {
		t.Fatalf("status %d, peg %+v (ids must be lower-cased)", rec.Code, f.lastPeg)
	}
}

func TestCreatePeg_ReplayAndStoreProblems(t *testing.T) {
	body := `{"parentOrderId":"` + parentOrder + `","dependentOrderId":"` + dependentOrder + `"}`
	rec := do(newRouter(&fakeStore{replay: true}), http.MethodPost, "/v1/scheduling/order-pegs", body, allDuties, "k1")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: status %d headers %v", rec.Code, rec.Header())
	}
	cycle := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodePegCycle, "loop")
	rec = do(newRouter(&fakeStore{err: cycle}), http.MethodPost, "/v1/scheduling/order-pegs", body, allDuties, "k2")
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != store.CodePegCycle {
		t.Fatalf("cycle: status %d: %s", rec.Code, rec.Body)
	}
}

func TestListAndDeletePegs(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	if rec := do(h, http.MethodGet, "/v1/scheduling/order-pegs?orderId=nope", "", allDuties, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad filter: status %d", rec.Code)
	}
	rec := do(h, http.MethodGet, "/v1/scheduling/order-pegs?orderId="+parentOrder, "", "scheduling.read", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) || f.lastPeg.ParentOrderID != parentOrder {
		t.Fatalf("list: status %d body %s filter %q", rec.Code, rec.Body, f.lastPeg.ParentOrderID)
	}
	if rec := do(h, http.MethodDelete, "/v1/scheduling/order-pegs/"+parentOrder, "", "scheduling.read", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("delete with read duty: status %d", rec.Code)
	}
	if rec := do(h, http.MethodDelete, "/v1/scheduling/order-pegs/"+parentOrder, "", allDuties, ""); rec.Code != http.StatusNoContent || f.lastPeg.ID != parentOrder {
		t.Fatalf("delete: status %d, id %q", rec.Code, f.lastPeg.ID)
	}
	missing := httpx.NewProblem(http.StatusNotFound, store.CodePegNotFound, "order peg not found")
	if rec := do(newRouter(&fakeStore{err: missing}), http.MethodDelete, "/v1/scheduling/order-pegs/"+parentOrder, "", allDuties, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: status %d", rec.Code)
	}
}

func TestMove_PropagateReachesTheStoreAndDependentsAreAlwaysAList(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", `{"start":"2026-01-05T09:30:00Z","dryRun":true,"propagate":true}`, allDuties, "")
	if rec.Code != http.StatusOK || !f.lastMove.Propagate {
		t.Fatalf("status %d, propagate reached store = %v", rec.Code, f.lastMove.Propagate)
	}
	var out moveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Dependents == nil || !strings.Contains(rec.Body.String(), `"dependents":[]`) {
		t.Fatalf("dependents must be an empty list, body %s", rec.Body)
	}
	// The fingerprint covers the body, so the same key with and without
	// propagate is two different requests.
	do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", `{"start":"2026-01-05T09:30:00Z"}`, allDuties, "same")
	plain := f.lastIdem.Fingerprint
	do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", `{"start":"2026-01-05T09:30:00Z","propagate":true}`, allDuties, "same")
	if plain == f.lastIdem.Fingerprint {
		t.Fatal("propagate must change the request fingerprint")
	}
}

func TestCreateCapacityBlock_Recurrence(t *testing.T) {
	f := &fakeStore{}
	ok := `{"resourceId":"` + resID + `","code":"MON-WHITE","recurrence":{"rule":" freq=weekly;byday=MO ","startDate":"2027-01-04","startMinute":360,"endMinute":840}}`
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/capacity-blocks", ok, allDuties, "k1")
	if rec.Code != http.StatusCreated || f.lastBlock.Recurrence == nil || f.lastBlock.Recurrence.Rule != "FREQ=WEEKLY;BYDAY=MO" || f.lastBlock.StartAt != nil {
		t.Fatalf("status %d, block %+v", rec.Code, f.lastBlock)
	}
	bad := map[string]string{
		"recurrence":   `{"resourceId":"` + resID + `","code":"B1","recurrence":{"rule":"FREQ=MONTHLY","startDate":"2027-01-04","startMinute":360,"endMinute":840}}`,
		"recurrence ":  `{"resourceId":"` + resID + `","code":"B1","recurrence":{"rule":"FREQ=DAILY","startDate":"04/01/2027","startMinute":360,"endMinute":840}}`,
		"recurrence  ": `{"resourceId":"` + resID + `","code":"B1","recurrence":{"rule":"FREQ=DAILY","startDate":"2027-01-04","startMinute":900,"endMinute":840}}`,
		"windows": `{"resourceId":"` + resID + `","code":"B1","startAt":"2027-01-04T06:00:00Z","endAt":"2027-01-04T14:00:00Z",
			"recurrence":{"rule":"FREQ=DAILY","startDate":"2027-01-04","startMinute":360,"endMinute":840}}`,
	}
	for field, body := range bad {
		rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/capacity-blocks", body, allDuties, "k2")
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[strings.TrimSpace(field)] == "" {
			t.Fatalf("%q: status %d: %s", field, rec.Code, rec.Body)
		}
	}
}
