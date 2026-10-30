package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// fakeStore records what the handlers ask of the store. Methods a test does
// not set panic through the nil embedded interface.
type fakeStore struct {
	store.Store
	order        store.Order
	released     bool
	filter       store.OrderFilter
	profileLE    string
	profile      store.CreditProfile
	listedLE     *string
	releaseCalls int
}

func (f *fakeStore) GetOrder(context.Context, string) (store.Order, error) { return f.order, nil }

func (f *fakeStore) ReleaseHold(_ context.Context, _, _ string, _ int, _ string, _ time.Time) (store.ConfirmResult, error) {
	f.releaseCalls++
	return store.ConfirmResult{Order: f.order, Confirmed: true}, nil
}

func (f *fakeStore) ListOrders(_ context.Context, filter store.OrderFilter) (store.OrderPage, error) {
	f.filter = filter
	return store.OrderPage{Items: []store.OrderSummary{}}, nil
}

func (f *fakeStore) GetCreditProfile(_ context.Context, le, _ string) (store.CreditProfile, error) {
	f.profileLE = le
	return f.profile, nil
}

func (f *fakeStore) ListCreditProfiles(_ context.Context, le string, _ int) ([]store.CreditProfile, error) {
	f.listedLE = &le
	return []store.CreditProfile{f.profile}, nil
}

func serve(t *testing.T, s store.Store, method, path string, p tenant.Principal, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	a := New(s)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	a.Register(r)
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func principal(duties ...string) tenant.Principal {
	p := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u1", Duties: map[string]struct{}{tenant.DutyAllScope: {}}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return p
}

// TestReleaseHold_DutyPerHoldType checks PRD §5.27.17: CREDIT holds need
// CreditRelease and the other hold types need SalesOrderHold, even though the
// route admits either duty.
func TestReleaseHold_DutyPerHoldType(t *testing.T) {
	orderID, holdID := ids.New(), ids.New()
	path := orders + "/" + orderID + "/holds/" + holdID + ":release"
	tests := []struct {
		holdType string
		duty     string
		want     int
	}{
		{domain.HoldCredit, dutyCreditRelease, http.StatusOK},
		{domain.HoldCredit, dutyHold, http.StatusForbidden},
		{domain.HoldFraud, dutyHold, http.StatusOK},
		{domain.HoldFraud, dutyCreditRelease, http.StatusForbidden},
		{domain.HoldManual, dutyCreditRelease, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.holdType+" with "+tc.duty, func(t *testing.T) {
			f := &fakeStore{order: store.Order{ID: orderID, Version: 2, Holds: []store.Hold{{ID: holdID, HoldType: tc.holdType, Status: domain.HoldActive}}}}
			rec := serve(t, f, http.MethodPost, path, principal(tc.duty), map[string]string{"If-Match": `"2"`})
			if rec.Code != tc.want {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if (tc.want == http.StatusOK) != (f.releaseCalls == 1) {
				t.Fatalf("store release calls %d", f.releaseCalls)
			}
			if tc.want == http.StatusOK && !strings.Contains(rec.Body.String(), `"confirmed":true`) {
				t.Fatalf("release response must carry confirmed: %s", rec.Body.String())
			}
		})
	}
}

func TestReleaseHold_RequiresIfMatch(t *testing.T) {
	f := &fakeStore{}
	rec := serve(t, f, http.MethodPost, orders+"/"+ids.New()+"/holds/"+ids.New()+":release", principal(dutyCreditRelease), nil)
	if rec.Code != http.StatusPreconditionRequired || f.releaseCalls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, f.releaseCalls)
	}
}

func TestListOrders_ValidatesAndPassesFilters(t *testing.T) {
	customer := ids.New()
	f := &fakeStore{}
	rec := serve(t, f, http.MethodGet, orders+"?status=DRAFT&hold=CREDIT&customerId="+customer+"&limit=10", principal(dutyRead), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.filter != (store.OrderFilter{Status: "DRAFT", HoldType: "CREDIT", CustomerID: customer, Limit: 10}) {
		t.Fatalf("filter %+v", f.filter)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must be an empty array: %s", rec.Body.String())
	}
	for _, q := range []string{"status=SHIPPING", "hold=LEGAL", "customerId=x", "limit=0", "limit=201", "cursor=abc"} {
		if rec := serve(t, &fakeStore{}, http.MethodGet, orders+"?"+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", q, rec.Code)
		}
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, orders, principal(dutyWrite), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list without the read duty: %d", rec.Code)
	}
}

func TestCreditProfile_LegalEntityAndWireDates(t *testing.T) {
	le, customer := ids.New(), ids.New()
	review := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	f := &fakeStore{profile: store.CreditProfile{LegalEntityID: le, CustomerID: customer, Version: 3,
		CreditProfile: domain.CreditProfile{Limit: decimal.NewFromInt(10000), Currency: "USD", LimitType: domain.LimitBalance, ReviewDate: &review}}}
	rec := serve(t, f, http.MethodGet, creditProfiles+"/"+customer+"?legalEntityId="+le, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || f.profileLE != le {
		t.Fatalf("status %d le %q: %s", rec.Code, f.profileLE, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"reviewDate":"2027-06-30"`) || !strings.Contains(body, `"limit":"10000"`) || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("wire form %s etag %s", body, rec.Header().Get("ETag"))
	}
	if rec := serve(t, f, http.MethodGet, creditProfiles+"/"+customer, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no legal entity: %d", rec.Code)
	}
	scoped := principal(dutyRead)
	scoped.LegalEntityID = ids.New()
	if rec := serve(t, f, http.MethodGet, creditProfiles+"/"+customer+"?legalEntityId="+le, scoped, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity than the caller's scope: %d", rec.Code)
	}
	if rec := serve(t, f, http.MethodGet, creditProfiles, principal(dutyRead), nil); rec.Code != http.StatusOK || f.listedLE == nil || *f.listedLE != "" {
		t.Fatalf("list across the caller's scope: %d %v", rec.Code, f.listedLE)
	}
}

func TestLegalEntityScope_RefusesCallerWithoutScope(t *testing.T) {
	noScope := principal(dutyRead, dutyWrite)
	delete(noScope.Duties, tenant.DutyAllScope)
	le, customer := ids.New(), ids.New()
	if rec := serve(t, &fakeStore{}, http.MethodGet, creditProfiles+"/"+customer+"?legalEntityId="+le, noScope, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("credit profile get: %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, creditProfiles, noScope, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("credit profile list: %d", rec.Code)
	}
	if rec := serveBody(t, &fakeStore{}, http.MethodPost, orders, orderBody(le), noScope, map[string]string{idempotencyKey: "k-noscope"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create order: %d %s", rec.Code, rec.Body.String())
	}
}
