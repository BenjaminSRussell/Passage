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

type paramState struct {
	params  store.Parameters
	patch   store.ParametersPatch
	version int
	reads   int
}

var paramCalls = map[*fakeStore]*paramState{}

func pstate(f *fakeStore) *paramState {
	if paramCalls[f] == nil {
		paramCalls[f] = &paramState{params: store.Parameters{DefaultMode: "jobs", DefaultDirection: "forward", DefaultFinite: true,
			DragSnapMinutes: 5, PropagationOrders: 500, PropagationLevels: 5, Version: 1}}
	}
	return paramCalls[f]
}

func (f *fakeStore) GetParameters(context.Context) (store.Parameters, error) {
	pstate(f).reads++
	if f.err != nil {
		return store.Parameters{}, f.err
	}
	return pstate(f).params, nil
}

func (f *fakeStore) UpdateParameters(_ context.Context, version int, patch store.ParametersPatch, idem store.Idem) (store.Parameters, bool, error) {
	f.lastIdem = idem
	st := pstate(f)
	st.patch, st.version = patch, version
	if f.err != nil {
		return store.Parameters{}, false, f.err
	}
	p := st.params
	p.Version = version + 1
	if patch.DragSnapMinutes != nil {
		p.DragSnapMinutes = *patch.DragSnapMinutes
	}
	return p, f.replay, nil
}

const parametersPath = "/v1/scheduling/parameters"

func TestParameters_GetNeedsReadDutyAndSendsETag(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := send(h, http.MethodGet, parametersPath, "", "other.duty", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: %d", rec.Code)
	}
	rec := send(h, http.MethodGet, parametersPath, "", "scheduling.read", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag=%q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	var p store.Parameters
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.DefaultDirection != "forward" || p.PropagationOrders != 500 {
		t.Fatalf("body: %+v err=%v", p, err)
	}
}

func TestParameters_PatchNeedsTheSetupDutyKeyAndVersion(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"dragSnapMinutes":15}`
	for _, duty := range []string{"scheduling.read", "scheduling.capacity.write", "scheduling.schedule.write", "scheduling.run"} {
		if rec := send(h, http.MethodPatch, parametersPath, body, duty, "k1", `"1"`); rec.Code != http.StatusForbidden {
			t.Fatalf("%s: %d, want 403", duty, rec.Code)
		}
	}
	if rec := send(h, http.MethodPatch, parametersPath, body, "scheduling.setup.write", "", `"1"`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := send(h, http.MethodPatch, parametersPath, body, "scheduling.setup.write", "k1", ""); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	rec := send(h, http.MethodPatch, parametersPath, body, "scheduling.setup.write", "k1", `"1"`)
	st := pstate(f)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || st.version != 1 || st.patch.DragSnapMinutes == nil || *st.patch.DragSnapMinutes != 15 || st.patch.DefaultMode != nil {
		t.Fatalf("patch: %d etag=%q version=%d body=%s", rec.Code, rec.Header().Get("ETag"), st.version, rec.Body)
	}
	if f.lastIdem.Key != "k1" || f.lastIdem.Fingerprint == "" {
		t.Fatalf("idempotency not passed: %+v", f.lastIdem)
	}
}

func TestParameters_PatchValidation(t *testing.T) {
	h := newRouter(&fakeStore{})
	cases := map[string]string{
		`{}`:                         "body",
		`{"defaultMode":"weekly"}`:   "defaultMode",
		`{"defaultDirection":"up"}`:  "defaultDirection",
		`{"dragSnapMinutes":0}`:      "dragSnapMinutes",
		`{"dragSnapMinutes":61}`:     "dragSnapMinutes",
		`{"propagationOrders":0}`:    "propagationOrders",
		`{"propagationOrders":5001}`: "propagationOrders",
		`{"propagationLevels":21}`:   "propagationLevels",
		`{"propagationLevels":-1}`:   "propagationLevels",
	}
	for body, field := range cases {
		rec := send(h, http.MethodPatch, parametersPath, body, "scheduling.setup.write", "k", `"1"`)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Errorf("%s: %d %s, want 400 naming %s", body, rec.Code, rec.Body, field)
		}
	}
}

func TestParameters_VersionConflictAndReplay(t *testing.T) {
	f := &fakeStore{err: httpx.NewProblem(http.StatusPreconditionFailed, store.CodeParametersVersion, "changed")}
	h := newRouter(f)
	rec := send(h, http.MethodPatch, parametersPath, `{"defaultFinite":false}`, "scheduling.setup.write", "k", `"1"`)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), store.CodeParametersVersion) {
		t.Fatalf("conflict: %d %s", rec.Code, rec.Body)
	}
	g := &fakeStore{replay: true}
	rec = send(newRouter(g), http.MethodPatch, parametersPath, `{"defaultFinite":false}`, "scheduling.setup.write", "k", `"1"`)
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", rec.Code, rec.Header())
	}
}

func TestParameters_RunsTakeTheDefaultsTheRequestLeavesOut(t *testing.T) {
	f := &fakeStore{}
	st := pstate(f)
	st.params.DefaultDirection, st.params.DefaultFinite, st.params.DefaultMode = "backward", false, "operations"
	h := newRouter(f)
	rec := send(h, http.MethodPost, "/v1/scheduling/runs", `{"scope":"site"}`, "scheduling.run", "r1", "")
	if rec.Code != http.StatusCreated || !f.lastRun.Backward || f.lastRun.Finite || f.lastRun.Mode != "operations" {
		t.Fatalf("site run: %d %+v", rec.Code, f.lastRun)
	}
	// Explicit choices win and the parameters are not read.
	before := st.reads
	rec = send(h, http.MethodPost, "/v1/scheduling/runs", `{"scope":"site","mode":"jobs","direction":"forward","finite":true}`, "scheduling.run", "r2", "")
	if rec.Code != http.StatusCreated || f.lastRun.Backward || !f.lastRun.Finite || f.lastRun.Mode != "jobs" || st.reads != before {
		t.Fatalf("explicit run: %d %+v reads=%d", rec.Code, f.lastRun, st.reads-before)
	}
	// The per-order command stays in jobs mode and takes direction and finite.
	rec = send(h, http.MethodPost, scheduleOrderPath(ids.New()), `{}`, "scheduling.run", "r3", "")
	if rec.Code != http.StatusCreated || !f.lastRun.Backward || f.lastRun.Finite || f.lastRun.Mode != "jobs" {
		t.Fatalf("order run: %d %+v", rec.Code, f.lastRun)
	}
	// A parameters read failure fails the run instead of using other defaults.
	bad := &fakeStore{err: httpx.NewProblem(http.StatusServiceUnavailable, "X", "down")}
	if rec := send(newRouter(bad), http.MethodPost, "/v1/scheduling/runs", `{"scope":"site"}`, "scheduling.run", "r4", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("read failure: %d", rec.Code)
	}
}
