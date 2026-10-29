package ledger_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
)

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u1", Duties: map[string]struct{}{"ledger.balance.read": {}}})
}

func TestAccountBalance(t *testing.T) {
	var gotPath, gotQuery, gotTenant, gotDuties string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotTenant, gotDuties = r.URL.Path, r.URL.RawQuery, r.Header.Get(tenant.HeaderTenant), r.Header.Get(tenant.HeaderDuties)
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[{"account":"2000","debit":"10.00","credit":"110.50"},{"account":"2001","debit":"1.00","credit":"2.00"}]}`))
	}))
	defer srv.Close()
	bal, err := ledger.New(srv.URL+"/", nil).AccountBalance(ctx(), "led-1", "2000", "2027-06")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/ledger/balances" || gotTenant != testkit.TenantID(1) || gotDuties != "ledger.balance.read" {
		t.Fatalf("path=%s tenant=%s duties=%s", gotPath, gotTenant, gotDuties)
	}
	for _, want := range []string{"ledger=led-1", "period_to=2027-06", "account_from=2000", "account_to=2000"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %s lacks %s", gotQuery, want)
		}
	}
	if bal.Currency != "USD" || bal.Credit.String() != "110.5" || bal.Debit.String() != "10" {
		t.Fatalf("balance = %+v (only the requested account counts)", bal)
	}
}

func TestAccountBalance_AccountWithNoRowsIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"currency":"USD","rows":[]}`)) }))
	defer srv.Close()
	bal, err := ledger.New(srv.URL, nil).AccountBalance(ctx(), "l", "2000", "2027-06")
	if err != nil || bal.Currency != "USD" || !bal.Credit.IsZero() || !bal.Debit.IsZero() {
		t.Fatalf("bal=%+v err=%v", bal, err)
	}
}

func TestAccountBalance_Failures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		ctx    context.Context
	}{
		{"forbidden", http.StatusForbidden, `{}`, ctx()},
		{"server error", http.StatusInternalServerError, `{}`, ctx()},
		{"not json", http.StatusOK, `nope`, ctx()},
		{"no currency", http.StatusOK, `{"rows":[]}`, ctx()},
		{"bad amount", http.StatusOK, `{"currency":"USD","rows":[{"account":"2000","debit":"x","credit":"1"}]}`, ctx()},
		{"no principal", http.StatusOK, `{"currency":"USD"}`, context.Background()},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := ledger.New(srv.URL, nil).AccountBalance(tc.ctx, "l", "2000", "2027-06"); err == nil {
				t.Fatal("want error")
			}
		})
	}
	if _, err := ledger.New("http://127.0.0.1:1", nil).AccountBalance(ctx(), "l", "2000", "2027-06"); err == nil {
		t.Fatal("unreachable ledger must error")
	}
}
