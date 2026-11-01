package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

const (
	mergeA = "55555555-0000-4000-8000-0000000000a1"
	mergeB = "55555555-0000-4000-8000-0000000000a2"
)

type mergeFake struct {
	fakeStore
	gotMerge store.MergeInput
	result   store.MergeResult
	gen      store.Genealogy
}

func (f *mergeFake) MergeBatches(_ context.Context, in store.MergeInput, idem store.Idem) (store.MergeResult, bool, error) {
	f.gotMerge, f.gotIdem = in, idem
	return f.result, !f.replay, f.err
}

func (f *mergeFake) GetGenealogy(context.Context, string) (store.Genealogy, error) {
	return f.gen, f.err
}

func mergeBody(mod func(string) string) string {
	b := `{"targetBatchNumber":" L-MERGED ","mergeOverride":["grade"],"sources":[
		{"batchId":"` + strings.ToUpper(mergeA) + `","version":3,"warehouseId":"W1","qty":"200","unitCost":"1.50"},
		{"batchId":"` + mergeB + `","version":1,"warehouseId":"W1","cwUnit":"kg","qty":"300","unitCost":"2"}]}`
	if mod != nil {
		return mod(b)
	}
	return b
}

func TestMergeStored_PassesCanonicalSourcesAndKey(t *testing.T) {
	f := &mergeFake{result: store.MergeResult{Qty: dec("500")}}
	rec := call{method: "POST", path: mergeStoredPath, body: mergeBody(nil), duties: DutyBatchMerge, headers: key("m-1")}.do(newRouter(f))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	in := f.gotMerge
	if in.TargetNumber != "L-MERGED" || len(in.Sources) != 2 || in.Sources[0].BatchID != mergeA || in.Sources[0].Version != 3 ||
		in.Sources[1].CWUnit != "kg" || !in.Sources[1].Qty.Equal(dec("300")) || !in.Override["grade"] || f.gotIdem.Key != "m-1" {
		t.Fatalf("input %+v idem %+v", in, f.gotIdem)
	}
	f.replay = true
	rec = call{method: "POST", path: mergeStoredPath, body: mergeBody(nil), duties: DutyBatchMerge, headers: key("m-1")}.do(newRouter(f))
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay %d %v", rec.Code, rec.Header())
	}
}

func TestMergeStored_Rejects(t *testing.T) {
	tests := map[string]string{
		"one source":     `{"targetBatchNumber":"X","sources":[{"batchId":"` + mergeA + `","version":1,"warehouseId":"W","qty":"1","unitCost":"1"}]}`,
		"no number":      mergeBody(func(b string) string { return strings.Replace(b, `" L-MERGED "`, `"  "`, 1) }),
		"repeated batch": mergeBody(func(b string) string { return strings.Replace(b, mergeB, mergeA, 1) }),
		"not a uuid":     mergeBody(func(b string) string { return strings.Replace(b, mergeB, "nope", 1) }),
		"version zero":   mergeBody(func(b string) string { return strings.Replace(b, `"version":1`, `"version":0`, 1) }),
		"no warehouse": mergeBody(func(b string) string {
			return strings.Replace(b, `"warehouseId":"W1","cwUnit":"kg"`, `"warehouseId":"","cwUnit":"kg"`, 1)
		}),
		"zero quantity":  mergeBody(func(b string) string { return strings.Replace(b, `"qty":"300"`, `"qty":"0"`, 1) }),
		"negative cost":  mergeBody(func(b string) string { return strings.Replace(b, `"unitCost":"2"`, `"unitCost":"-2"`, 1) }),
		"seven decimals": mergeBody(func(b string) string { return strings.Replace(b, `"qty":"200"`, `"qty":"1.1234567"`, 1) }),
		"long override":  mergeBody(func(b string) string { return strings.Replace(b, `["grade"]`, `["`+strings.Repeat("x", 41)+`"]`, 1) }),
		"unknown field": mergeBody(func(b string) string {
			return strings.Replace(b, `{"targetBatchNumber"`, `{"extra":1,"targetBatchNumber"`, 1)
		}),
	}
	for name, body := range tests {
		f := &mergeFake{}
		rec := call{method: "POST", path: mergeStoredPath, body: body, duties: DutyBatchMerge, headers: key("k")}.do(newRouter(f))
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Code != "COM-0001" || len(f.gotMerge.Sources) != 0 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestMergeStored_KeyDutyAndStoreErrors(t *testing.T) {
	f := &mergeFake{}
	if rec := post(mergeStoredPath, mergeBody(nil), DutyBatchMerge).do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("missing key: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: mergeStoredPath, body: mergeBody(nil), duties: DutyRead, headers: key("k")}).do(newRouter(f)); rec.Code != http.StatusForbidden {
		t.Errorf("without merge duty: %d", rec.Code)
	}
	f.err = httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "stale")
	rec := call{method: "POST", path: mergeStoredPath, body: mergeBody(nil), duties: DutyBatchMerge, headers: key("k")}.do(newRouter(f))
	if rec.Code != http.StatusConflict || problemOf(t, rec).Code != store.CodeVersionConflict {
		t.Errorf("stale: %d %s", rec.Code, rec.Body)
	}
}

func TestGenealogy_ReadsWithReadDuty(t *testing.T) {
	f := &mergeFake{gen: store.Genealogy{BatchID: mergeA, Parents: []store.GenealogyLink{{BatchID: mergeB, BatchNumber: "P-1"}}, Children: []store.GenealogyLink{}}}
	path := "/v1/production/batches/" + mergeA + "/genealogy"
	rec := call{method: "GET", path: path, duties: DutyRead}.do(newRouter(f))
	got := decodeBody[store.Genealogy](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || len(got.Parents) != 1 || got.Parents[0].BatchNumber != "P-1" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "GET", path: path, duties: "x"}).do(newRouter(f)); rec.Code != http.StatusForbidden {
		t.Errorf("without duty: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: "/v1/production/batches/nope/genealogy", duties: DutyRead}).do(newRouter(f)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
}
