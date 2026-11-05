package compliance_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/compliance"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const entity = "33333333-3333-4333-8333-333333333333"

func ask(t *testing.T, status int, body string) ([]string, *http.Request, error) {
	t.Helper()
	var seen *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "11111111-1111-4111-8111-111111111111", LegalEntityID: entity, UserID: "service:receiving-erasure"})
	got, err := compliance.New(srv.URL, nil).Covering(ctx, domain.HoldQuery{LegalEntityID: entity, SubjectRef: "party:42", DataClass: "AP_PAYEE", RecordRef: "r-1"})
	return got, seen, err
}

func TestCovering_SendsTheTargetAndReturnsTheHolds(t *testing.T) {
	got, req, err := ask(t, http.StatusOK, `{"held":true,"holdIds":["h-1","h-2"]}`)
	if err != nil || len(got) != 2 || got[0] != "h-1" {
		t.Fatalf("got %v %v", got, err)
	}
	q := req.URL.Query()
	if req.Method != http.MethodGet || req.URL.Path != "/v1/privacy/holds:check" || q.Get("legalEntityId") != entity || q.Get("subjectRef") != "party:42" || q.Get("dataClass") != "AP_PAYEE" || q.Get("recordRef") != "r-1" {
		t.Fatalf("request %s %s", req.Method, req.URL)
	}
	if free, _, err := ask(t, http.StatusOK, `{"held":false,"holdIds":[]}`); err != nil || len(free) != 0 {
		t.Fatalf("no hold: %v %v", free, err)
	}
	if unnamed, _, err := ask(t, http.StatusOK, `{"held":true,"holdIds":[]}`); err != nil || len(unnamed) != 1 {
		t.Fatalf("a hold without ids is still a hold: %v %v", unnamed, err)
	}
}

func TestCovering_FailsClosedOnAnythingButAHoldCheck(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"forbidden":  {http.StatusForbidden, `{"code":"AUTH-0003"}`},
		"server":     {http.StatusInternalServerError, `{}`},
		"not a body": {http.StatusOK, `not json`},
		"no verdict": {http.StatusOK, `{"holdIds":[]}`},
	} {
		if got, _, err := ask(t, tc.status, tc.body); err == nil || got != nil {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}
