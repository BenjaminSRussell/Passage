package fxrates

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const testEntity = "00000000-0000-0000-0000-0000000000e1"

func signer(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	s, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "ledger", "PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
	})
	if err != nil || s == nil {
		t.Fatalf("signer: %v %v", s, err)
	}
	return s
}

func principalContext() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u", LegalEntityID: testEntity})
}

func TestRate_AsksForSpotAndMapsStatuses(t *testing.T) {
	date := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		status        int
		body          string
		wantRejection string
		refused       bool
		want          string
	}{
		"ok":          {status: 200, body: `{"rate":"0.915"}`, want: "0.915"},
		"no rate":     {status: 422, body: `{"code":"LEDGER_RATE_MISSING","detail":"none"}`, wantRejection: "LEDGER_RATE_MISSING"},
		"future":      {status: 422, body: `{"code":"COM-0003","serviceCode":"FX_RATE_DATE_FUTURE"}`, wantRejection: "FX_RATE_DATE_FUTURE"},
		"forbidden":   {status: 403, body: `{}`, refused: true},
		"bad request": {status: 400, body: `{}`, refused: true},
		"server":      {status: 500, body: `{}`},
		"unusable":    {status: 200, body: `{"rate":"0"}`},
		"not decimal": {status: 200, body: `{"rate":"x"}`},
	} {
		var query string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		rate, err := New(srv.URL, nil, signer(t)).Rate(principalContext(), testEntity, "USD", "EUR", date)
		srv.Close()
		switch {
		case tc.want != "" && (err != nil || rate.String() != tc.want):
			t.Errorf("%s: rate %s, err %v", name, rate, err)
		case tc.want == "" && err == nil:
			t.Errorf("%s: want an error", name)
		}
		var rejected *store.RateRejected
		var refused *store.RateRefused
		if tc.wantRejection != "" && (!errors.As(err, &rejected) || rejected.Code != tc.wantRejection) {
			t.Errorf("%s: want rejection %s, got %v", name, tc.wantRejection, err)
		}
		if tc.refused != errors.As(err, &refused) {
			t.Errorf("%s: refused = %v for %v", name, tc.refused, err)
		}
		if query != "date=2026-03-31&from=USD&to=EUR&type=spot" {
			t.Errorf("%s: query %q", name, query)
		}
	}
}

func TestRate_NeedsAPrincipal(t *testing.T) {
	if _, err := New("http://fx", nil, signer(t)).Rate(context.Background(), testEntity, "USD", "EUR", time.Now()); err == nil {
		t.Fatal("want an error without a principal")
	}
}
