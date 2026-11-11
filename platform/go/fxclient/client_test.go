package fxclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func testSigner(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	s, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "consolidation", "PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
	})
	if err != nil || s == nil {
		t.Fatalf("signer: %v %v", s, err)
	}
	return s
}

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func TestRate_AsksForTheTypeAndMapsAnswers(t *testing.T) {
	date := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		status  int
		body    string
		want    string
		missing bool
	}{
		"ok":      {status: 200, body: `{"rate":"1.0875"}`, want: "1.0875"},
		"missing": {status: 422, body: `{}`, missing: true},
		"server":  {status: 500, body: `{}`},
		"zero":    {status: 200, body: `{"rate":"0"}`},
	} {
		var query string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query = r.URL.RawQuery
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c, err := New(srv.URL, testSigner(t), nil)
		if err != nil {
			t.Fatal(err)
		}
		rate, err := c.Rate(principalCtx(), "00000000-0000-0000-0000-0000000000e1", "closing", "GBP", "EUR", date)
		srv.Close()
		switch {
		case tc.want != "" && (err != nil || rate.String() != tc.want):
			t.Errorf("%s: %s %v", name, rate, err)
		case tc.want == "" && err == nil:
			t.Errorf("%s: want an error", name)
		}
		if tc.missing != errors.Is(err, ErrRateMissing) {
			t.Errorf("%s: missing = %v for %v", name, tc.missing, err)
		}
		if query != "date=2026-03-31&from=GBP&to=EUR&type=closing" {
			t.Errorf("%s: query %q", name, query)
		}
	}
}

func TestRate_SameCurrencyNeedsNoCall(t *testing.T) {
	c, err := New("http://127.0.0.1:1", testSigner(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Rate(principalCtx(), "", "closing", "EUR", "EUR", time.Now())
	if err != nil || !r.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("%s %v", r, err)
	}
	if _, err := c.Rate(context.Background(), "", "closing", "EUR", "USD", time.Now()); err == nil {
		t.Fatal("want an error without a principal")
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string { return map[string]string{"FX_URL": "http://fx"}[k] }); err == nil {
		t.Fatal("want an error without a signing key")
	}
}

func TestFake(t *testing.T) {
	f := Fake{}
	f.Set("average", "GBP", "EUR", decimal.RequireFromString("1.15"))
	if r, err := f.Rate(context.Background(), "", "average", "GBP", "EUR", time.Now()); err != nil || r.String() != "1.15" {
		t.Fatalf("%s %v", r, err)
	}
	if _, err := f.Rate(context.Background(), "", "closing", "GBP", "EUR", time.Now()); !errors.Is(err, ErrRateMissing) {
		t.Fatalf("want missing, got %v", err)
	}
}
