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
		status  int
		body    string
		wantErr error
		want    string
	}{
		"ok":          {status: 200, body: `{"rate":"0.915"}`, want: "0.915"},
		"no rate":     {status: 422, body: `{}`, wantErr: store.ErrRateUnavailable},
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
		case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
			t.Errorf("%s: want %v, got %v", name, tc.wantErr, err)
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
