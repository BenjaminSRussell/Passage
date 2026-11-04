package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

type maintainFake struct {
	fakeStore
	updated     *store.WorkerInput
	version     int
	deactivated string
	reactivated bool
	failWith    error
}

func (f *maintainFake) UpdateWorker(_ context.Context, id string, version int, in store.WorkerInput, _ string, _ store.Idem) (store.Worker, error) {
	if f.failWith != nil {
		return store.Worker{}, f.failWith
	}
	f.updated, f.version = &in, version
	return store.Worker{ID: id, Name: in.Name, BadgeID: in.BadgeID, CostRate: in.CostRate, Currency: in.Currency, Active: true, Version: version + 1}, nil
}

func (f *maintainFake) DeactivateWorker(_ context.Context, id string, version int, reason, _ string, _ store.Idem) (store.Worker, error) {
	if f.failWith != nil {
		return store.Worker{}, f.failWith
	}
	f.deactivated, f.version = reason, version
	return store.Worker{ID: id, Active: false, Version: version + 1, DeactivationReason: &reason}, nil
}

func (f *maintainFake) ReactivateWorker(_ context.Context, id string, version int, _ string, _ store.Idem) (store.Worker, error) {
	if f.failWith != nil {
		return store.Worker{}, f.failWith
	}
	f.reactivated, f.version = true, version
	return store.Worker{ID: id, Active: true, Version: version + 1}, nil
}

var _ Store = (*maintainFake)(nil)

const updateBody = `{"name":" Ann Lee ","badgeId":"B-9","userId":null,"costRate":"22.50","currency":"USD"}`

func TestWorkerMaintenance_UpdateDeactivateReactivate(t *testing.T) {
	f := &maintainFake{}
	h := newRouter(f)
	base := "/v1/shopfloor/workers/" + workerID
	hdr := map[string]string{"Idempotency-Key": "w-1", "If-Match": `"3"`}

	rec := callWith(t, h, http.MethodPut, base, updateBody, allDuty, hdr)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || f.version != 3 || f.updated == nil || f.updated.Name != "Ann Lee" ||
		f.updated.CostRate.String() != "22.5" || f.updated.BadgeID == nil || *f.updated.BadgeID != "B-9" {
		t.Fatalf("update: %d %s %+v", rec.Code, rec.Body.String(), f.updated)
	}
	rec = callWith(t, h, http.MethodPost, base+":deactivate", `{"reason":" left the company "}`, allDuty, hdr)
	if rec.Code != http.StatusOK || f.deactivated != "left the company" || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("deactivate: %d %s %q", rec.Code, rec.Body.String(), f.deactivated)
	}
	rec = callWith(t, h, http.MethodPost, base+":reactivate", ``, allDuty, hdr)
	if rec.Code != http.StatusOK || !f.reactivated {
		t.Fatalf("reactivate: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWorkerMaintenance_ValidationAndHeaders(t *testing.T) {
	h := newRouter(&maintainFake{})
	base := "/v1/shopfloor/workers/" + workerID
	hdr := map[string]string{"Idempotency-Key": "w-2", "If-Match": `"1"`}
	cases := []struct {
		path, body, field string
	}{
		{base, `{"name":"","costRate":"1","currency":"USD"}`, "name"},
		{base, `{"name":"A","badgeId":"","costRate":"1","currency":"USD"}`, "badgeId"},
		{base, `{"name":"A","costRate":"-1","currency":"USD"}`, "costRate"},
		{base, `{"name":"A","costRate":"1","currency":"usd"}`, "currency"},
		{base + ":deactivate", `{"reason":"  "}`, "reason"},
	}
	for _, c := range cases {
		method := http.MethodPut
		if c.field == "reason" {
			method = http.MethodPost
		}
		rec := callWith(t, h, method, c.path, c.body, allDuty, hdr)
		var p struct {
			Errors map[string]string `json:"errors"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s %s: %d errors=%v", c.path, c.field, rec.Code, p.Errors)
		}
	}
	if rec := callWith(t, h, http.MethodPut, base, updateBody, allDuty, map[string]string{"Idempotency-Key": "w-3"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPut, base, updateBody, allDuty, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPut, "/v1/shopfloor/workers/nope", updateBody, allDuty, hdr); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	for _, path := range []string{base, base + ":deactivate", base + ":reactivate"} {
		method := http.MethodPost
		if path == base {
			method = http.MethodPut
		}
		if rec := callWith(t, h, method, path, `{"reason":"x"}`, "shopfloor.view", hdr); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without the setup duty: %d", path, rec.Code)
		}
	}
}

func TestWorkerMaintenance_StoreProblemsPassThrough(t *testing.T) {
	base := "/v1/shopfloor/workers/" + workerID
	hdr := map[string]string{"Idempotency-Key": "w-4", "If-Match": `"1"`}
	cases := []struct {
		err    error
		method string
		path   string
		body   string
		status int
	}{
		{httpx.NewProblem(http.StatusConflict, store.CodeBadgeTaken, "badge taken"), http.MethodPut, base, updateBody, http.StatusConflict},
		{httpx.NewProblem(http.StatusConflict, store.CodeWorkerClockedIn, "clocked in"), http.MethodPost, base + ":deactivate", `{"reason":"x"}`, http.StatusConflict},
		{httpx.NewProblem(http.StatusConflict, store.CodeWorkerActive, "active"), http.MethodPost, base + ":reactivate", ``, http.StatusConflict},
		{httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionMismatch, "stale"), http.MethodPut, base, updateBody, http.StatusPreconditionFailed},
	}
	for _, c := range cases {
		h := newRouter(&maintainFake{failWith: c.err})
		rec := callWith(t, h, c.method, c.path, c.body, allDuty, hdr)
		if rec.Code != c.status {
			t.Errorf("%s: %d %s", c.path, rec.Code, rec.Body.String())
		}
	}
}
