package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/features"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

type stubFeatures struct {
	state map[string]bool
	err   error
}

func (s stubFeatures) Enabled(context.Context, string) (map[string]bool, error) {
	return s.state, s.err
}

func gatedRouter(source features.Source) http.Handler {
	r := httpx.NewRouter()
	New(&fakeStore{formula: ac241Formula()}).WithFeatureGate(features.Require(source, "process-manufacturing")).Register(r)
	return r
}

func listFormulas(h http.Handler) *httptest.ResponseRecorder {
	return call{method: "GET", path: "/v1/production/formulas", duties: DutyRead}.do(h)
}

func TestFeatureGateRefusesRoutesWhileProcessManufacturingIsOff(t *testing.T) {
	rec := listFormulas(gatedRouter(stubFeatures{state: map[string]bool{"process-manufacturing": false}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
	p := problemOf(t, rec)
	if p.Code != features.CodeDisabled || p.Errors["feature"] != "process-manufacturing" {
		t.Fatalf("problem = %+v", p)
	}

	post := call{method: "POST", path: "/v1/production/formulas", body: formulaBody, duties: DutyFormulaWrite, headers: key("f-1")}.do(gatedRouter(stubFeatures{state: map[string]bool{"process-manufacturing": false}}))
	if post.Code != http.StatusForbidden || problemOf(t, post).Code != features.CodeDisabled {
		t.Fatalf("a write must be refused too, got %d: %s", post.Code, post.Body)
	}
}

func TestFeatureGateLeavesTheServiceAloneWhenOnOrUnknown(t *testing.T) {
	for name, source := range map[string]stubFeatures{
		"on":                   {state: map[string]bool{"process-manufacturing": true}},
		"identity unreachable": {err: errors.New("identity unreachable")},
	} {
		if rec := listFormulas(gatedRouter(source)); rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200: %s", name, rec.Code, rec.Body)
		}
	}
}

func TestFeatureGateDoesNotBlockReadiness(t *testing.T) {
	h := gatedRouter(stubFeatures{state: map[string]bool{"process-manufacturing": false}})
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := call{method: "GET", path: path}.do(h)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

func TestWithoutAFeatureGateRoutesBehaveAsBefore(t *testing.T) {
	if rec := listFormulas(newRouter(&fakeStore{formula: ac241Formula()})); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
