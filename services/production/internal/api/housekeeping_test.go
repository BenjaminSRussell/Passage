package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// housekeepingFake answers the two scans with canned orders and records their arguments.
type housekeepingFake struct {
	stale, ended []store.HousekeepingOrder
	total        int
	scans        []store.HousekeepingScan
	err          error
	// replay answers HousekeepingReplay; replayErr refuses the key.
	replay     []store.HousekeepingOrder
	replayErr  error
	replayArgs []string
}

func (f *fakeStore) StaleCreatedOrders(_ context.Context, in store.HousekeepingScan, days int) ([]store.HousekeepingOrder, int, error) {
	f.hk.scans = append(f.hk.scans, in)
	if f.hk.total == 0 {
		return f.hk.stale, len(f.hk.stale), f.hk.err
	}
	return f.hk.stale, f.hk.total, f.hk.err
}

func (f *fakeStore) FullyReportedOrders(_ context.Context, in store.HousekeepingScan) ([]store.HousekeepingOrder, int, error) {
	f.hk.scans = append(f.hk.scans, in)
	return f.hk.ended, len(f.hk.ended), f.hk.err
}

func (f *fakeStore) HousekeepingReplay(_ context.Context, le, key, hash string) ([]store.HousekeepingOrder, error) {
	f.hk.replayArgs = append(f.hk.replayArgs, le+"|"+key+"|"+hash)
	return f.hk.replay, f.hk.replayErr
}

func TestHousekeeping_CancelStale(t *testing.T) {
	first, second := ids.New(), ids.New()
	f := &fakeStore{hk: housekeepingFake{stale: []store.HousekeepingOrder{{OrderID: first, OrderNumber: "PO-000001", Version: 1}, {OrderID: second, OrderNumber: "PO-000002", Version: 3}}}}
	_, h := newServer(t, f)
	post := func(body string, key string, duties ...string) *httptest.ResponseRecorder {
		c := call{method: http.MethodPost, path: "/v1/production/orders:cancel-stale", duties: duties, body: body}
		if key != "" {
			c.headers = map[string]string{"Idempotency-Key": key}
		}
		return do(h, c)
	}
	// a dry run needs no Idempotency-Key and changes nothing
	rec := post(`{"dryRun":true}`, "", DutyRelease)
	if rec.Code != http.StatusOK || len(f.bulks) != 0 {
		t.Fatalf("dry run: %d bulks %d", rec.Code, len(f.bulks))
	}
	var dry housekeepingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &dry)
	if dry.Job != jobCancelStale || !dry.DryRun || dry.Total != 2 || len(dry.Candidates) != 2 || dry.LegalEntityID != entityID || len(dry.Results) != 0 {
		t.Fatalf("dry %+v", dry)
	}
	// a run cancels each candidate through the bulk path and reports partial failure as PROD-0022
	if len(f.hk.replayArgs) != 0 {
		t.Fatalf("a dry run looks up no replay: %v", f.hk.replayArgs)
	}
	rec = post(`{}`, "k1", DutyRelease)
	if rec.Code != http.StatusOK || len(f.bulks) != 1 || f.bulks[0].Command != domain.CmdCancel || len(f.bulks[0].OrderIDs) != 2 || f.bulks[0].IdempotencyKey != "k1" {
		t.Fatalf("run: %d %+v", rec.Code, f.bulks)
	}
	// each order is pinned to the version the scan saw, under the job's own request hash
	if b := f.bulks[0]; b.Versions[first] != 1 || b.Versions[second] != 3 || b.RequestHash != "housekeeping:"+jobCancelStale {
		t.Fatalf("pinned: %+v", b)
	}
	if len(f.hk.replayArgs) != 1 || f.hk.replayArgs[0] != entityID+"|k1|housekeeping:"+jobCancelStale {
		t.Fatalf("replay lookup: %v", f.hk.replayArgs)
	}
	var run housekeepingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	if run.Succeeded != 1 || run.Failed != 1 || run.Code != domain.CodeBulkPartial || run.Truncated {
		t.Fatalf("run %+v", run)
	}
	expectProblem(t, post(`{}`, "", DutyRelease), http.StatusBadRequest, "COM-0001")
	expectProblem(t, post(`{}`, "k2", DutyRead), http.StatusForbidden, "AUTH-0003")
	expectProblem(t, post(`{"legalEntityId":"nope"}`, "k3", DutyRelease), http.StatusBadRequest, "COM-0001")
	expectProblem(t, post(`{"legalEntityId":"`+ids.New()+`"}`, "k4", DutyRelease), http.StatusBadRequest, "COM-0001")
	expectProblem(t, post(`{"orderIds":["nope"]}`, "k5", DutyRelease), http.StatusBadRequest, "COM-0001")
	expectProblem(t, post(`{"orderIds":["`+first+`","`+first+`"]}`, "k6", DutyRelease), http.StatusBadRequest, "COM-0001")
	// the reviewed orders narrow the scan
	if rec := post(`{"orderIds":["`+first+`"]}`, "k7", DutyRelease); rec.Code != http.StatusOK {
		t.Fatalf("reviewed: %d %s", rec.Code, rec.Body)
	}
	if last := f.hk.scans[len(f.hk.scans)-1]; len(last.OrderIDs) != 1 || last.OrderIDs[0] != first || last.LegalEntityID != entityID || last.Limit != domain.MaxBulkOrders {
		t.Fatalf("scan %+v", last)
	}
}

// A retry under a used Idempotency-Key replays that run's orders and does not scan again; a key
// used for something else is refused.
func TestHousekeeping_RetryReplaysTheEarlierRun(t *testing.T) {
	moved := ids.New()
	f := &fakeStore{hk: housekeepingFake{replay: []store.HousekeepingOrder{{OrderID: moved, Status: domain.Cancelled, Version: 2}},
		stale: []store.HousekeepingOrder{{OrderID: ids.New()}}}}
	_, h := newServer(t, f)
	c := call{method: http.MethodPost, path: "/v1/production/orders:cancel-stale", duties: []string{DutyRelease}, body: `{}`, headers: map[string]string{"Idempotency-Key": "k1"}}
	rec := do(h, c)
	var out housekeepingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || !out.Replayed || rec.Header().Get("Idempotent-Replayed") != "true" || len(f.hk.scans) != 0 {
		t.Fatalf("replay: %d %+v scans %d", rec.Code, out, len(f.hk.scans))
	}
	if len(f.bulks) != 1 || len(f.bulks[0].OrderIDs) != 1 || f.bulks[0].OrderIDs[0] != moved || out.Total != 1 || out.Truncated {
		t.Fatalf("replayed orders: %+v %+v", f.bulks, out)
	}
	f.hk.replayErr = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "used")
	expectProblem(t, do(h, c), http.StatusUnprocessableEntity, store.CodeIdempotencyReuse)
	if len(f.bulks) != 1 {
		t.Fatalf("a refused key moves nothing: %d", len(f.bulks))
	}
}

func TestHousekeeping_TruncatedAndNothingToDo(t *testing.T) {
	f := &fakeStore{hk: housekeepingFake{stale: []store.HousekeepingOrder{{OrderID: ids.New()}}, total: 900}}
	_, h := newServer(t, f)
	rec := do(h, call{method: http.MethodPost, path: "/v1/production/orders:cancel-stale", duties: []string{DutyRelease}, body: `{"dryRun":true}`})
	var out housekeepingResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Truncated || out.Total != 900 || len(out.Candidates) != 1 {
		t.Fatalf("truncated %+v", out)
	}
	// nothing selected: no bulk call, empty (not null) results
	f = &fakeStore{}
	_, h = newServer(t, f)
	rec = do(h, call{method: http.MethodPost, path: "/v1/production/orders:auto-end", duties: []string{DutyEnd}, body: `{}`, headers: map[string]string{"Idempotency-Key": "k"}})
	if rec.Code != http.StatusOK || len(f.bulks) != 0 || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("empty run: %d %s", rec.Code, rec.Body)
	}
	var empty map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &empty)
	if empty["results"] == nil || empty["candidates"] == nil {
		t.Fatalf("results must be arrays: %s", rec.Body)
	}
}

func TestHousekeeping_AutoEndNeedsTheEndDuty(t *testing.T) {
	f := &fakeStore{hk: housekeepingFake{ended: []store.HousekeepingOrder{{OrderID: ids.New()}}}}
	_, h := newServer(t, f)
	c := call{method: http.MethodPost, path: "/v1/production/orders:auto-end", body: `{}`, headers: map[string]string{"Idempotency-Key": "e1"}}
	c.duties = []string{DutyRelease}
	expectProblem(t, do(h, c), http.StatusForbidden, "AUTH-0003")
	c.duties = []string{DutyEnd}
	if rec := do(h, c); rec.Code != http.StatusOK || len(f.bulks) != 1 || f.bulks[0].Command != domain.CmdEnd || f.bulks[0].ZeroOutputDuty {
		t.Fatalf("auto-end: %d %+v", rec.Code, f.bulks)
	}
}
