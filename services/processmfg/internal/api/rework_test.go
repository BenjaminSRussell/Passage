package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	reworkID         = "88888888-0000-4000-8000-0000000000e1"
	reworkBatch      = "88888888-0000-4000-8000-0000000000b1"
	reworkOrdersPath = "/v1/production/rework-orders"
)

func (f *fakeStore) CreateReworkOrder(_ context.Context, in store.NewRework, idem store.Idem) (store.ReworkOrder, bool, error) {
	f.gotRework, f.gotIdem = in, idem
	return store.ReworkOrder{ID: reworkID, Version: 1, Status: "planned"}, !f.replay, f.err
}

func (f *fakeStore) ListReworkOrders(_ context.Context, filter store.ReworkFilter) ([]store.ReworkOrder, error) {
	f.gotReworkFilt = filter
	return nil, f.err
}

func (f *fakeStore) GetReworkOrder(context.Context, string) (store.ReworkOrder, error) {
	return store.ReworkOrder{ID: reworkID, Version: 1}, f.err
}

func (f *fakeStore) ReceiveReworkOrder(_ context.Context, id, number string, version int, canRelease bool, idem store.Idem) (store.ReworkOrder, bool, error) {
	f.gotItem, f.gotVersion, f.gotIdem, f.gotRelease = number, version, idem, canRelease
	return store.ReworkOrder{ID: id, Version: version + 1, Status: "received"}, !f.replay, f.err
}

const reworkBody = `{"formulaId":"` + formulaID + `","batchSize":"80","sourceBatchId":"` + reworkBatch + `","sourceVersion":3,"sourceQty":"100","reworkQty":"80","outputQty":"78"}`

func TestCreateRework_PassesPlanWithDefaults(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "POST", path: reworkOrdersPath, body: reworkBody, duties: DutyBatchOrderWrite, headers: key("rw-1")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := fs.gotRework
	if got.LegalEntityID != entityID || got.SourceVersion != 3 || got.SourceBatchID != reworkBatch || !got.InputQty.Equal(dec("80")) || !got.OutputQty.Equal(dec("78")) {
		t.Fatalf("command %+v", got)
	}
	// FR-24.18 defaults: keep the number, require a quality order, quarantine.
	if !got.Params.KeepBatchNumber || !got.Params.RequireQualityOrder || got.Params.OutputDisposition != "quarantine" || got.Params.Capitalize {
		t.Fatalf("params %+v", got.Params)
	}
	if fs.gotIdem.Key != "rw-1" {
		t.Fatalf("idempotency %+v", fs.gotIdem)
	}
}

func TestCreateRework_OverridesParams(t *testing.T) {
	fs := &fakeStore{}
	body := strings.Replace(reworkBody, `"outputQty":"78"`, `"outputQty":"78","params":{"keepBatchNumber":false,"requireQualityOrder":false,"outputDisposition":"on-hold","capitalize":true}`, 1)
	rec := call{method: "POST", path: reworkOrdersPath, body: body, duties: DutyBatchOrderWrite, headers: key("rw-2")}.do(newRouter(fs))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	p := fs.gotRework.Params
	if p.KeepBatchNumber || p.RequireQualityOrder || p.OutputDisposition != "on-hold" || !p.Capitalize {
		t.Fatalf("params %+v", p)
	}
}

func TestCreateRework_Rejects(t *testing.T) {
	tests := []struct {
		name, body, duties string
		hdr                map[string]string
		status             int
		field              string
	}{
		{"no key", reworkBody, DutyBatchOrderWrite, nil, 400, ""},
		{"read duty cannot write", reworkBody, DutyRead, key("k"), 403, ""},
		{"formula not a uuid", strings.Replace(reworkBody, formulaID, "F1", 1), DutyBatchOrderWrite, key("k"), 400, "formulaId"},
		{"no version", strings.Replace(reworkBody, `"sourceVersion":3`, `"sourceVersion":0`, 1), DutyBatchOrderWrite, key("k"), 400, "sourceVersion"},
		{"quantity with 7 decimals", strings.Replace(reworkBody, `"reworkQty":"80"`, `"reworkQty":"0.0000001"`, 1), DutyBatchOrderWrite, key("k"), 400, "reworkQty"},
		{"foreign legal entity", strings.Replace(reworkBody, `"batchSize"`, `"legalEntityId":"99999999-0000-4000-8000-000000000009","batchSize"`, 1), DutyBatchOrderWrite, key("k"), 403, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "POST", path: reworkOrdersPath, body: tc.body, duties: tc.duties, headers: tc.hdr}.do(newRouter(fs))
			p := problemOf(t, rec)
			if rec.Code != tc.status {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if _, bad := p.Errors[tc.field]; tc.field != "" && !bad {
				t.Fatalf("no error for %s: %+v", tc.field, p.Errors)
			}
		})
	}
}

func TestCreateRework_ReplayIs200(t *testing.T) {
	rec := call{method: "POST", path: reworkOrdersPath, body: reworkBody, duties: DutyBatchOrderWrite, headers: key("rw-3")}.do(newRouter(&fakeStore{replay: true}))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("status %d %v", rec.Code, rec.Header())
	}
}

func TestReceiveRework(t *testing.T) {
	fs := &fakeStore{}
	path := reworkOrdersPath + "/" + reworkID + ":receive"
	rec := call{method: "POST", path: path, body: `{"version":1,"newBatchNumber":"L-2"}`, duties: DutyBatchOrderWrite, headers: key("rc-1")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotItem != "L-2" || fs.gotVersion != 1 || fs.gotRelease {
		t.Fatalf("status %d number %q version %d release %v: %s", rec.Code, fs.gotItem, fs.gotVersion, fs.gotRelease, rec.Body)
	}
	// The disposition duty is what lets the store receive an output planned as available.
	fs = &fakeStore{}
	rec = call{method: "POST", path: path, body: `{"version":1}`, duties: DutyBatchOrderWrite + "," + DutyBatchDisposition, headers: key("rc-4")}.do(newRouter(fs))
	if rec.Code != http.StatusOK || !fs.gotRelease {
		t.Fatalf("with disposition duty: status %d release %v", rec.Code, fs.gotRelease)
	}
	for name, body := range map[string]string{"no version": `{"newBatchNumber":"L"}`, "padded number": `{"version":1,"newBatchNumber":" L "}`} {
		if rec := (call{method: "POST", path: path, body: body, duties: DutyBatchOrderWrite, headers: key("rc-2")}.do(newRouter(&fakeStore{}))); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := (call{method: "POST", path: path, body: `{"version":1}`, duties: DutyRead, headers: key("rc-3")}.do(newRouter(&fakeStore{}))); rec.Code != http.StatusForbidden {
		t.Errorf("read duty: status %d", rec.Code)
	}
}

func TestListRework_Filters(t *testing.T) {
	fs := &fakeStore{}
	rec := call{method: "GET", path: reworkOrdersPath + "?status=planned&sourceBatchId=" + reworkBatch, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK || fs.gotReworkFilt.Status != "planned" || fs.gotReworkFilt.SourceBatchID != reworkBatch {
		t.Fatalf("status %d filter %+v", rec.Code, fs.gotReworkFilt)
	}
	for _, q := range []string{"?status=done", "?sourceBatchId=x"} {
		if rec := (call{method: "GET", path: reworkOrdersPath + q, duties: DutyRead}.do(newRouter(&fakeStore{}))); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
	if rec := (call{method: "GET", path: reworkOrdersPath + "/" + reworkID, duties: DutyRead}.do(newRouter(&fakeStore{}))); rec.Code != http.StatusOK {
		t.Errorf("get: status %d", rec.Code)
	}
}
