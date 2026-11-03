package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

type opCalls struct {
	patch   store.OperationPatch
	version int
	deleted string
	unsched string
}

var opState = map[*fakeStore]*opCalls{}

func calls(f *fakeStore) *opCalls {
	if opState[f] == nil {
		opState[f] = &opCalls{}
	}
	return opState[f]
}

func (f *fakeStore) GetOperation(_ context.Context, id string) (store.Operation, error) {
	if f.err != nil {
		return store.Operation{}, f.err
	}
	return store.Operation{ID: id, Version: 4, Minutes: 60, ResourceIDs: []string{}, SecondaryResourceIDs: []string{}}, nil
}

func (f *fakeStore) UpdateOperation(_ context.Context, id string, version int, patch store.OperationPatch, idem store.Idem) (store.OperationUpdate, bool, error) {
	f.lastIdem = idem
	c := calls(f)
	c.patch, c.version = patch, version
	if f.err != nil {
		return store.OperationUpdate{}, false, f.err
	}
	return store.OperationUpdate{Operation: store.Operation{ID: id, Version: version + 1}, JobsRemoved: patch.Minutes != nil}, f.replay, nil
}

func (f *fakeStore) DeleteOperation(_ context.Context, id string, version int, idem store.Idem) (store.OperationRemoval, bool, error) {
	f.lastIdem = idem
	calls(f).deleted = id
	calls(f).version = version
	if f.err != nil {
		return store.OperationRemoval{}, false, f.err
	}
	return store.OperationRemoval{OperationID: id}, f.replay, nil
}

func (f *fakeStore) UnscheduleOrder(_ context.Context, orderID string, idem store.Idem) (store.OrderUnschedule, bool, error) {
	f.lastIdem = idem
	calls(f).unsched = orderID
	if f.err != nil {
		return store.OrderUnschedule{}, false, f.err
	}
	return store.OrderUnschedule{OrderID: orderID, Operations: []int{10}, KeptFirmed: []int{}}, f.replay, nil
}

func send(h http.Handler, method, path, body, duties, key, ifMatch string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", tenantID)
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const opPath = "/v1/scheduling/operations/" + parentOrder

func TestUpdateOperation_ValidatesAndNeedsKeyVersionAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	ok := `{"minutes":90,"priority":5,"resourceIds":["` + parentOrder + `"]}`
	if rec := send(h, http.MethodPatch, opPath, ok, allDuties, "", `"1"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := send(h, http.MethodPatch, opPath, ok, allDuties, "k", ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := send(h, http.MethodPatch, opPath, ok, "scheduling.read", "k", `"1"`); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: %d", rec.Code)
	}
	bad := map[string]string{
		"body":         `{}`,
		"minutes":      `{"minutes":0}`,
		"priority":     `{"priority":1001}`,
		"setupMinutes": `{"setupMinutes":-1}`,
		"resourceIds":  `{"resourceIds":["nope"]}`,
	}
	for field, body := range bad {
		rec := send(h, http.MethodPatch, opPath, body, allDuties, "k-"+field, `"1"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body)
		}
	}
	if rec := send(h, http.MethodPatch, opPath, `{"resourceIds":[]}`, allDuties, "k-empty", `"1"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty candidates: %d", rec.Code)
	}
	rec := send(h, http.MethodPatch, opPath, ok, allDuties, "k1", `"3"`)
	c := calls(f)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || c.version != 3 || c.patch.Minutes == nil || *c.patch.Minutes != 90 ||
		len(c.patch.ResourceIDs) != 1 || !strings.Contains(rec.Body.String(), `"jobsRemoved":true`) {
		t.Fatalf("status %d etag %s patch %+v body %s", rec.Code, rec.Header().Get("ETag"), c.patch, rec.Body)
	}
	first := f.lastIdem.Fingerprint
	send(h, http.MethodPatch, opPath, ok, allDuties, "k1", `"4"`)
	if f.lastIdem.Fingerprint == first {
		t.Fatal("the version is part of the fingerprint")
	}
	if rec := send(newRouter(&fakeStore{replay: true}), http.MethodPatch, opPath, ok, allDuties, "k1", `"3"`); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header missing: %d", rec.Code)
	}
	stale := httpx.NewProblem(http.StatusPreconditionFailed, store.CodeOperationVersion, "changed")
	if rec := send(newRouter(&fakeStore{err: stale}), http.MethodPatch, opPath, ok, allDuties, "k2", `"1"`); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	if rec := send(h, http.MethodPatch, "/v1/scheduling/operations/nope", ok, allDuties, "k3", `"1"`); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestGetOperation_SendsVersionAsETag(t *testing.T) {
	h := newRouter(&fakeStore{})
	rec := send(h, http.MethodGet, opPath, "", "scheduling.read", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("get: %d %s", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := send(h, http.MethodGet, opPath, "", "scheduling.capacity.write", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no read duty: %d", rec.Code)
	}
	if rec := send(h, http.MethodGet, "/v1/scheduling/operations/x", "", "scheduling.read", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestDeleteOperation_NeedsKeyVersionAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	if rec := send(h, http.MethodDelete, opPath, "", allDuties, "", `"1"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, opPath, "", allDuties, "k", ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, opPath, "", "scheduling.read", "k", `"1"`); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty only: %d", rec.Code)
	}
	if rec := send(h, http.MethodDelete, opPath, "", allDuties, "k", `"2"`); rec.Code != http.StatusOK || calls(f).deleted != parentOrder || calls(f).version != 2 {
		t.Fatalf("delete: %d %+v", rec.Code, calls(f))
	}
	firmed := httpx.NewProblem(http.StatusConflict, store.CodeOperationFirmed, "firmed")
	if rec := send(newRouter(&fakeStore{err: firmed}), http.MethodDelete, opPath, "", allDuties, "k", `"2"`); rec.Code != http.StatusConflict {
		t.Fatalf("firmed: %d", rec.Code)
	}
	if rec := send(newRouter(&fakeStore{replay: true}), http.MethodDelete, opPath, "", allDuties, "k", `"2"`); rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header missing")
	}
}

func TestUnscheduleOrder_NeedsKeyAndDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/scheduling/orders/" + parentOrder + ":unschedule"
	if rec := send(h, http.MethodPost, path, "", allDuties, "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := send(h, http.MethodPost, path, "", "scheduling.run", "k", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("run duty only: %d", rec.Code)
	}
	rec := send(h, http.MethodPost, path, "", allDuties, "k", "")
	if rec.Code != http.StatusOK || calls(f).unsched != parentOrder || !strings.Contains(rec.Body.String(), `"operations":[10]`) {
		t.Fatalf("unschedule: %d %s", rec.Code, rec.Body)
	}
	if rec := send(h, http.MethodPost, "/v1/scheduling/orders/nope:unschedule", "", allDuties, "k", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}
