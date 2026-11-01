package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

type stubGate struct{}

func (stubGate) Status(context.Context, string, string, time.Time, string) (string, error) {
	return "closed", nil
}

func TestPeriodGate_MiddlewareCarriesTheConfiguredGateOnly(t *testing.T) {
	var seen context.Context
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() })
	probe := func(a *API) bool {
		a.periodGate(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		_, err := store.PeriodGateStatus(seen)
		return err == nil
	}
	if probe(New(nil)) {
		t.Fatal("an API without a close client must not attach a gate")
	}
	if !probe(New(nil).WithPeriodGate(stubGate{})) {
		t.Fatal("an API with a close client must attach its gate")
	}
}
