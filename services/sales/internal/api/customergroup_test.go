package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/internal/upstream"
)

type fakeGroups struct {
	group string
	err   error
}

func (f fakeGroups) CustomerGroup(context.Context, string, string) (string, error) {
	return f.group, f.err
}

func TestResolveGroup(t *testing.T) {
	typed := func(s string) *string { return &s }
	tests := []struct {
		name     string
		src      CustomerGroupSource
		supplied *string
		status   int
		code     string
	}{
		{"party group, nothing typed", fakeGroups{group: "g1"}, nil, 0, ""},
		{"typed equals party", fakeGroups{group: "g1"}, typed("g1"), 0, ""},
		{"typed differs is refused", fakeGroups{group: "g1"}, typed("VIP"), http.StatusUnprocessableEntity, codeCustomerGroupMismatch},
		{"party failure is 503, typed group not used", fakeGroups{err: errors.New("boom")}, typed("VIP"), http.StatusServiceUnavailable, codeCustomerGroupUnavailable},
		{"party not configured is 503", nil, typed("VIP"), http.StatusServiceUnavailable, codeCustomerGroupUnavailable},
		{"unknown customer", fakeGroups{err: upstream.ErrCustomerUnknown}, nil, http.StatusUnprocessableEntity, codeCustomerUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := New(nil)
			a.groups = tc.src
			got, err := a.resolveGroup(context.Background(), ids.New(), ids.New(), tc.supplied)
			if tc.status == 0 {
				if err != nil || got == nil || *got != "g1" {
					t.Fatalf("got %v %v", got, err)
				}
				return
			}
			var p *httpx.Problem
			if got != nil || !errors.As(err, &p) || p.Status != tc.status || p.Code != tc.code {
				t.Fatalf("want %d %s, got %v %v", tc.status, tc.code, got, err)
			}
			if tc.status == http.StatusServiceUnavailable && strings.Contains(p.Detail, "boom") {
				t.Fatalf("the upstream error leaks into the problem: %s", p.Detail)
			}
		})
	}
}

// A rep typing another group on a new order is refused and nothing is stored.
func TestCreateOrder_TypedGroupDifferingFromPartyIsRefused(t *testing.T) {
	s := &slice3Store{}
	a := New(s).WithCustomerGroups(fakeGroups{group: "OTHER"})
	rec := serveWith(a, http.MethodPost, orders, orderBody(testLE), principal(dutyWrite))
	if rec.Code != http.StatusUnprocessableEntity || s.newOrder != nil {
		t.Fatalf("status %d stored %v body %s", rec.Code, s.newOrder, rec.Body.String())
	}
	a = New(s).WithCustomerGroups(fakeGroups{err: errors.New("down")})
	if rec = serveWith(a, http.MethodPost, orders, orderBody(testLE), principal(dutyWrite)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("party down: status %d", rec.Code)
	}
}

func (s *slice4Store) GetQuotation(context.Context, string) (store.Quotation, error) {
	return store.Quotation{LegalEntityID: testLE, CustomerID: ids.New()}, nil
}

// :convert reads the group from the party service for the quotation's customer.
func TestConvertQuotation_UsesPartyGroup(t *testing.T) {
	s := &slice4Store{}
	a := New(s).WithCustomerGroups(fakeGroups{group: "party-group"})
	if rec := serveBody2(a, quotations+"/"+ids.New()+":convert"); rec.Code != http.StatusCreated || s.convert == nil || s.convert.CustomerGroup == nil || *s.convert.CustomerGroup != "party-group" {
		t.Fatalf("status %d convert %+v", rec.Code, s.convert)
	}
}

func serveBody2(a *API, path string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), principal(dutyQuotation))))
		})
	})
	a.Register(r)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("If-Match", `"2"`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// :convert refuses with 503 and converts nothing when the party service is down.
func TestConvertQuotation_PartyDownConvertsNothing(t *testing.T) {
	s := &slice4Store{}
	a := New(s).WithCustomerGroups(fakeGroups{err: errors.New("timeout")})
	if rec := serveBody2(a, quotations+"/"+ids.New()+":convert"); rec.Code != http.StatusServiceUnavailable || s.convert != nil ||
		strings.Contains(rec.Body.String(), "timeout") {
		t.Fatalf("status %d convert %+v body %s", rec.Code, s.convert, rec.Body.String())
	}
}

// groupQuoteStore is a quotation change store whose quotation has a customer.
type groupQuoteStore struct{ quoteChangeStore }

func (s *groupQuoteStore) GetQuotation(context.Context, string) (store.Quotation, error) {
	return store.Quotation{LegalEntityID: testLE, CustomerID: ids.New()}, nil
}

// Replacing the lines checks them against the customer's current party group;
// a validity-only change never calls party.
func TestChangeQuotation_ReplacedLinesUsePartyGroup(t *testing.T) {
	path := quotations + "/" + ids.New()
	match := map[string]string{"If-Match": `"4"`}
	lines := `{"lines":[{"productId":"` + ids.New() + `","quantity":"2","unit":"EA","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() +
		`","unitPrice":"10.00","requestedShipDate":"2099-03-10"}]}`
	s := &groupQuoteStore{}
	if rec := serveBody(t, s, http.MethodPatch, path, lines, principal(dutyQuotation), match); rec.Code != http.StatusOK ||
		s.change == nil || s.change.CustomerGroup == nil || *s.change.CustomerGroup != "EU-RETAIL" {
		t.Fatalf("lines change: %d %+v", rec.Code, s.change)
	}
	s = &groupQuoteStore{}
	if rec := serveBody(t, s, http.MethodPatch, path, `{"probabilityPct":70}`, principal(dutyQuotation), match); rec.Code != http.StatusOK || s.change.CustomerGroup != nil {
		t.Fatalf("probability-only change: %d %+v", rec.Code, s.change)
	}
	s = &groupQuoteStore{}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), principal(dutyQuotation))))
		})
	})
	New(s).WithCustomerGroups(fakeGroups{err: upstream.ErrCustomerUnknown}).Register(r)
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(lines))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"4"`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || s.change != nil || !strings.Contains(rec.Body.String(), codeCustomerUnknown) {
		t.Fatalf("unknown customer: %d %s stored %+v", rec.Code, rec.Body.String(), s.change)
	}
}

// A release order is checked for the customer's party group like any other
// order: party down or the customer unknown releases nothing.
func TestReleaseOrder_PartyGroupRequired(t *testing.T) {
	path := agreements + "/" + ids.New() + ":release-order"
	body := `{"quantity":"40","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","requestedShipDate":"2027-04-15"}`
	for _, tc := range []struct {
		src    fakeGroups
		status int
		code   string
	}{
		{fakeGroups{err: errors.New("timeout")}, http.StatusServiceUnavailable, codeCustomerGroupUnavailable},
		{fakeGroups{err: upstream.ErrCustomerUnknown}, http.StatusUnprocessableEntity, codeCustomerUnknown},
	} {
		s := &slice3Store{releaseFresh: true}
		a := New(s).WithCustomerGroups(tc.src)
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), principal(dutyAgreement))))
			})
		})
		a.Register(r)
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(idempotencyKey, "r1")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != tc.status || s.release != nil || !strings.Contains(rec.Body.String(), tc.code) || strings.Contains(rec.Body.String(), "timeout") {
			t.Fatalf("want %d %s: got %d %s, released %+v", tc.status, tc.code, rec.Code, rec.Body.String(), s.release)
		}
	}
}
