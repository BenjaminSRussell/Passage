package fx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/fx"
)

var day = time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: testkit.TenantID(1), LegalEntityID: "00000000-0000-0000-0000-0000000000a1", UserID: "u1",
		Duties: map[string]struct{}{"View exchange rates": {}},
	})
}

func TestRate_RequestAndParse(t *testing.T) {
	var gotPath, gotQuery, gotTenant, gotEntity, gotDuties string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotTenant, gotEntity, gotDuties = r.Header.Get(tenant.HeaderTenant), r.Header.Get(tenant.HeaderLegalEntity), r.Header.Get(tenant.HeaderDuties)
		_, _ = w.Write([]byte(`{"rateType":"spot","from":"EUR","to":"USD","date":"2027-03-31","path":"direct","rate":"1.0825","hops":[]}`))
	}))
	defer srv.Close()
	rate, err := fx.New(srv.URL+"/", "", nil).Rate(ctx(), "EUR", "USD", day)
	if err != nil {
		t.Fatal(err)
	}
	if rate.String() != "1.0825" {
		t.Fatalf("rate = %s", rate)
	}
	if gotPath != "/v1/fx/rates" || gotTenant != testkit.TenantID(1) || gotEntity != "00000000-0000-0000-0000-0000000000a1" || gotDuties != "View exchange rates" {
		t.Fatalf("path=%s tenant=%s entity=%s duties=%s", gotPath, gotTenant, gotEntity, gotDuties)
	}
	for _, want := range []string{"type=spot", "from=EUR", "to=USD", "date=2027-03-31"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %s lacks %s", gotQuery, want)
		}
	}
}

func TestRate_ConfiguredRateType(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"rate":"2"}`))
	}))
	defer srv.Close()
	if _, err := fx.New(srv.URL, "closing", nil).Rate(ctx(), "EUR", "USD", day); err != nil || !strings.Contains(gotQuery, "type=closing") {
		t.Fatalf("query=%s err=%v", gotQuery, err)
	}
}

func TestRate_Failures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		ctx        context.Context
		wantMissed bool
		wantStatus int
	}{
		{"no rate for the pair", http.StatusUnprocessableEntity, `{"code":"LEDGER_RATE_MISSING"}`, ctx(), true, 0},
		{"forbidden", http.StatusForbidden, `{}`, ctx(), false, http.StatusForbidden},
		{"server fault", http.StatusInternalServerError, `{}`, ctx(), false, http.StatusInternalServerError},
		{"unparseable body", http.StatusOK, `not json`, ctx(), false, 0},
		{"zero rate", http.StatusOK, `{"rate":"0"}`, ctx(), false, 0},
		{"missing rate", http.StatusOK, `{}`, ctx(), false, 0},
		{"no principal", http.StatusOK, `{"rate":"1"}`, context.Background(), false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := fx.New(srv.URL, "", nil).Rate(tc.ctx, "EUR", "USD", day)
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, fx.ErrRateUnavailable) != tc.wantMissed {
				t.Errorf("ErrRateUnavailable = %v, want %v (%v)", !tc.wantMissed, tc.wantMissed, err)
			}
			var se *fx.StatusError
			if tc.wantStatus != 0 && (!errors.As(err, &se) || se.Status != tc.wantStatus) {
				t.Errorf("err = %v, want StatusError %d", err, tc.wantStatus)
			}
		})
	}
}

func TestRate_TransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	if _, err := fx.New(srv.URL, "", nil).Rate(ctx(), "EUR", "USD", day); err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestRate_DoesNotFollowRedirectsWithTenantHeaders(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()
	for name, client := range map[string]*http.Client{"default": nil, "custom": {}} {
		_, err := fx.New(redirector.URL, "", client).Rate(ctx(), "EUR", "USD", day)
		var se *fx.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusFound || leaked {
			t.Fatalf("%s client: err=%v leaked=%v, want the 302 returned without following it", name, err, leaked)
		}
	}
}
