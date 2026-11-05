package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// fakeRule is the rule the fake store reads; the zero value is "none saved".
var (
	fakeRule      store.ReleaseRule
	savedRuleIn   store.ReleaseRuleInput
	savedRuleLE   string
	savedRuleWH   string
	savedRuleBase int
)

func (f *fakeStore) ListReleaseRules(context.Context) ([]store.ReleaseRule, error) {
	return []store.ReleaseRule{{WarehouseID: ids.New(), HorizonDays: 2, RequireFullATP: true, WaveMaxLines: 200, WaveMaxOrders: 50}}, nil
}

func (f *fakeStore) GetReleaseRule(_ context.Context, id string) (store.ReleaseRule, error) {
	if fakeRule.Version > 0 {
		r := fakeRule
		r.WarehouseID = id
		return r, nil
	}
	return store.ReleaseRule{WarehouseID: id, HorizonDays: 2, RequireFullATP: true, WaveMaxLines: 200, WaveMaxOrders: 50}, nil
}

func (f *fakeStore) SaveReleaseRule(_ context.Context, id, le string, version int, in store.ReleaseRuleInput, idem store.Idem) (store.ReleaseRule, error) {
	f.lastIdem = idem
	savedRuleIn, savedRuleLE, savedRuleWH, savedRuleBase = in, le, id, version
	if version != 2 {
		return store.ReleaseRule{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeSettingVersion, "stale")
	}
	return store.ReleaseRule{WarehouseID: id, LegalEntityID: &le, HorizonDays: in.HorizonDays, RequireFullATP: in.RequireFullATP,
		WaveMaxLines: in.WaveMaxLines, WaveMaxOrders: in.WaveMaxOrders, Version: 3}, nil
}

func TestReleaseRule_ReadSaveValidateAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	wh, le := ids.New(), ids.New()
	path := "/v1/fulfillment/release-rules/" + wh
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/release-rules", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"waveMaxLines":200`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, path, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"0"` {
		t.Fatalf("get: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/release-rules/nope", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	body := func(le, extra string) string {
		return `{"legalEntityId":"` + le + `","horizonDays":5,"requireFullAtp":false,"waveMaxLines":120,"waveMaxOrders":30` + extra + `}`
	}
	hdr := func(m map[string]string) map[string]string { return m }
	if rec := do(t, h, http.MethodPut, path, body(le, ""), hdr(map[string]string{"Idempotency-Key": "r-1"})); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body(le, ""), hdr(map[string]string{"If-Match": `"x"`, "Idempotency-Key": "r-1"})); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body(le, ""), hdr(map[string]string{"If-Match": `"2"`})); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	bad := map[string]struct{ body, field string }{
		"legal entity":  {`{"horizonDays":5,"requireFullAtp":true,"waveMaxLines":1,"waveMaxOrders":1}`, "legalEntityId"},
		"horizon high":  {`{"legalEntityId":"` + le + `","horizonDays":31,"requireFullAtp":true,"waveMaxLines":1,"waveMaxOrders":1}`, "horizonDays"},
		"horizon none":  {`{"legalEntityId":"` + le + `","requireFullAtp":true,"waveMaxLines":1,"waveMaxOrders":1}`, "horizonDays"},
		"atp missing":   {`{"legalEntityId":"` + le + `","horizonDays":1,"waveMaxLines":1,"waveMaxOrders":1}`, "requireFullAtp"},
		"lines zero":    {`{"legalEntityId":"` + le + `","horizonDays":1,"requireFullAtp":true,"waveMaxLines":0,"waveMaxOrders":1}`, "waveMaxLines"},
		"lines too big": {`{"legalEntityId":"` + le + `","horizonDays":1,"requireFullAtp":true,"waveMaxLines":2001,"waveMaxOrders":1}`, "waveMaxLines"},
		"orders big":    {`{"legalEntityId":"` + le + `","horizonDays":1,"requireFullAtp":true,"waveMaxLines":1,"waveMaxOrders":501}`, "waveMaxOrders"},
		"orders none":   {`{"legalEntityId":"` + le + `","horizonDays":1,"requireFullAtp":true,"waveMaxLines":1}`, "waveMaxOrders"},
	}
	for name, c := range bad {
		rec := do(t, h, http.MethodPut, path, c.body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "r-bad"})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodPut, path, body(le, ""), hdr(map[string]string{"If-Match": `"1"`, "Idempotency-Key": "r-3"})); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPut, path, body(le, ""), hdr(map[string]string{"If-Match": `"2"`, "Idempotency-Key": "r-4"}))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || !strings.Contains(rec.Body.String(), `"horizonDays":5`) {
		t.Fatalf("save: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if savedRuleLE != le || savedRuleWH != wh || savedRuleBase != 2 || savedRuleIn != (store.ReleaseRuleInput{HorizonDays: 5, RequireFullATP: false, WaveMaxLines: 120, WaveMaxOrders: 30}) {
		t.Fatalf("store got %s %s %d %+v", savedRuleLE, savedRuleWH, savedRuleBase, savedRuleIn)
	}
	if rec := do(t, h, http.MethodPut, path, body(le, ""), hdr(map[string]string{"If-Match": `"2"`, "Idempotency-Key": "r-5", "X-Passage-Duties": "fulfillment.warehouse.view"})); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer save: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, path, "", map[string]string{"X-Passage-Duties": "fulfillment.warehouse.pick"}); rec.Code != http.StatusForbidden {
		t.Fatalf("picker read: %d", rec.Code)
	}
}

// A release run takes the warehouse rule when the request names no horizon or
// ATP rule, and the request wins over it.
func TestRunRelease_TakesTheWarehouseRule(t *testing.T) {
	defer func() { fakeRule = store.ReleaseRule{} }()
	fs := &fakeStore{}
	h := newServer(t, fs)
	in5 := time.Now().UTC().AddDate(0, 0, 5).Format("2006-01-02")
	line := func(atp bool) string {
		return `{"orderId":"` + ids.New() + `","orderLineId":"` + ids.New() + `","customerId":"` + ids.New() +
			`","legalEntityId":"` + ids.New() + `","productId":"` + ids.New() + `","unit":"EA","qty":"1","confirmedShipDate":"` + in5 + `","atpOk":` + boolStr(atp) + `}`
	}
	run := func(extra string, atp bool) int {
		fs.released = nil
		body := `{"warehouseId":"` + ids.New() + `"` + extra + `,"lines":[` + line(atp) + `]}`
		if rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", body, map[string]string{"Idempotency-Key": "k" + ids.New()}); rec.Code != http.StatusCreated {
			t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
		}
		return len(fs.released)
	}
	// No saved rule: the process default (2 days) skips a line 5 days out.
	if n := run("", true); n != 0 {
		t.Fatalf("no rule: released %d groups, want 0", n)
	}
	fakeRule = store.ReleaseRule{HorizonDays: 7, RequireFullATP: false, WaveMaxLines: 200, WaveMaxOrders: 50, Version: 1}
	if n := run("", false); n != 1 {
		t.Fatalf("rule (7 days, partial ATP): released %d groups, want 1", n)
	}
	if n := run(`,"horizonDays":1`, true); n != 0 {
		t.Fatalf("request horizon 1 must win over the rule: released %d groups, want 0", n)
	}
	if n := run(`,"requireFullAtp":true`, false); n != 0 {
		t.Fatalf("request ATP rule must win over the rule: released %d groups, want 0", n)
	}
}

// The workbench takes the warehouse's wave limits for a limit the call leaves out.
func TestWaves_TakeTheWarehouseWaveLimits(t *testing.T) {
	defer func() { fakeRule = store.ReleaseRule{} }()
	fs := &fakeStore{}
	h := newServer(t, fs)
	fakeRule = store.ReleaseRule{HorizonDays: 2, RequireFullATP: true, WaveMaxLines: 80, WaveMaxOrders: 12, Version: 4}
	post := func(body string) {
		if rec := do(t, h, http.MethodPost, "/v1/fulfillment/waves:preview", body, nil); rec.Code != http.StatusOK {
			t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
		}
	}
	post(`{"warehouseId":"` + wbWarehouse + `"}`)
	if got := fs.workbench.waveReq.Limits; got.MaxLines != 80 || got.MaxOrders != 12 {
		t.Fatalf("limits = %+v, want the rule's 80 and 12", got)
	}
	post(`{"warehouseId":"` + wbWarehouse + `","maxLines":10}`)
	if got := fs.workbench.waveReq.Limits; got.MaxLines != 10 || got.MaxOrders != 12 {
		t.Fatalf("limits = %+v, want 10 from the call and 12 from the rule", got)
	}
	fakeRule = store.ReleaseRule{}
	post(`{"warehouseId":"` + wbWarehouse + `"}`)
	if got := fs.workbench.waveReq.Limits; got.MaxLines != 200 || got.MaxOrders != 50 {
		t.Fatalf("limits = %+v, want the defaults without a saved rule", got)
	}
}
