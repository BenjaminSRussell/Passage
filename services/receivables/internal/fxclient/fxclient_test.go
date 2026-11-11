package fxclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func testSigner(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	s, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "receivables", "PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
	})
	if err != nil || s == nil {
		t.Fatalf("signer: %v %v", s, err)
	}
	return s
}

func TestRate_AsksForSpotAndMapsStatuses(t *testing.T) {
	date := time.Date(2027, 3, 8, 0, 0, 0, 0, time.UTC)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u", LegalEntityID: "00000000-0000-0000-0000-0000000000e1"})
	for name, tc := range map[string]struct {
		status  int
		body    string
		want    string
		wantErr error
	}{
		"ok":        {status: 200, body: `{"rate":"1.1234"}`, want: "1.1234"},
		"no rate":   {status: 422, body: `{"code":"FX-0001"}`, wantErr: ErrNoRate},
		"not found": {status: 404, body: `{}`, wantErr: ErrNoRate},
	} {
		t.Run(name, func(t *testing.T) {
			var query string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := New(srv.URL, nil, testSigner(t)).Rate(ctx, "00000000-0000-0000-0000-0000000000e1", "EUR", "USD", date)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.String() != tc.want || query != "date=2027-03-08&from=EUR&to=USD&type=SPOT" {
				t.Fatalf("got %s %v query %q", got, err, query)
			}
		})
	}
	if r, err := New("http://unused", nil, testSigner(t)).Rate(ctx, "x", "USD", "USD", date); err != nil || r.String() != "1" {
		t.Errorf("same currency: %v %v", r, err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	if _, err := New(srv.URL, nil, testSigner(t)).Rate(ctx, "x", "EUR", "USD", date); err == nil || errors.Is(err, ErrNoRate) {
		t.Errorf("a server error is not 'no rate': %v", err)
	}
}
