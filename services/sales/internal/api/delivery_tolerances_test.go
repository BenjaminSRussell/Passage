package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type toleranceStore struct {
	store.Store
	created  *store.NewDeliveryTolerance
	replayed bool
	filter   *store.DeliveryToleranceFilter
	replaced domain.DeliveryTolerance
	version  int
	deleted  string
	resolved store.ResolvedTolerance
	resolve  [3]string
}

func (s *toleranceStore) CreateDeliveryTolerance(_ context.Context, in store.NewDeliveryTolerance) (store.DeliveryTolerance, bool, error) {
	s.created = &in
	return store.DeliveryTolerance{ID: ids.New(), LegalEntityID: in.LegalEntityID, ProductID: in.ProductID, CustomerID: in.CustomerID,
		OverdeliveryPct: in.Tolerance.OverdeliveryPct, UnderdeliveryPct: in.Tolerance.UnderdeliveryPct, PartialDelivery: in.Tolerance.PartialDelivery, Version: 1}, !s.replayed, nil
}

func (s *toleranceStore) ListDeliveryTolerances(_ context.Context, f store.DeliveryToleranceFilter) ([]store.DeliveryTolerance, error) {
	s.filter = &f
	return []store.DeliveryTolerance{}, nil
}

func (s *toleranceStore) GetDeliveryTolerance(_ context.Context, id string) (store.DeliveryTolerance, error) {
	return store.DeliveryTolerance{ID: id, Version: 2}, nil
}

func (s *toleranceStore) ReplaceDeliveryTolerance(_ context.Context, id string, version int, t domain.DeliveryTolerance, _ string, _ time.Time) (store.DeliveryTolerance, error) {
	s.replaced, s.version = t, version
	return store.DeliveryTolerance{ID: id, OverdeliveryPct: t.OverdeliveryPct, UnderdeliveryPct: t.UnderdeliveryPct, PartialDelivery: t.PartialDelivery, Version: version + 1}, nil
}

func (s *toleranceStore) DeleteDeliveryTolerance(_ context.Context, id, _ string) error {
	s.deleted = id
	return nil
}

func (s *toleranceStore) ResolveDeliveryTolerance(_ context.Context, le, product, customer string) (store.ResolvedTolerance, error) {
	s.resolve = [3]string{le, product, customer}
	return s.resolved, nil
}

func TestDeliveryToleranceHandlers(t *testing.T) {
	s := &toleranceStore{}
	product, customer := ids.New(), ids.New()
	body := `{"legalEntityId":"` + testLE + `","productId":"` + product + `","customerId":"` + customer + `","overdeliveryPct":"5","underdeliveryPct":"2.5","partialDelivery":"COMPLETE_ONLY"}`
	key := map[string]string{idempotencyKey: "tol-1"}
	if rec := serveBody(t, s, http.MethodPost, deliveryTolerances, body, principal(dutyRead), key); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs sales.parameter.maintain: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, deliveryTolerances, body, principal(dutyParameter), nil); rec.Code != http.StatusBadRequest || s.created != nil {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	pre := `{"legalEntityId":"` + testLE + `","productId":"` + product + `"`
	for name, tc := range map[string]struct{ body, field string }{
		"over 100":           {pre + `,"overdeliveryPct":"100.01"}`, "overdeliveryPct"},
		"negative under":     {pre + `,"underdeliveryPct":"-1"}`, "underdeliveryPct"},
		"three places":       {pre + `,"overdeliveryPct":"1.005"}`, "overdeliveryPct"},
		"not a number":       {pre + `,"overdeliveryPct":"lots"}`, "overdeliveryPct"},
		"unknown policy":     {pre + `,"partialDelivery":"NEVER"}`, "partialDelivery"},
		"customer not uuid":  {pre + `,"customerId":"C1"}`, "customerId"},
		"product not uuid":   {`{"legalEntityId":"` + testLE + `","productId":"FG-1"}`, "productId"},
		"legal entity wrong": {`{"legalEntityId":"le","productId":"` + product + `"}`, "legalEntityId"},
	} {
		rec := serveBody(t, s, http.MethodPost, deliveryTolerances, tc.body, principal(dutyParameter), key)
		if rec.Code != http.StatusBadRequest || s.created != nil || !strings.Contains(rec.Body.String(), tc.field) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := serveBody(t, s, http.MethodPost, deliveryTolerances, body, principal(dutyParameter), key)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || s.created.Actor != "u1" || s.created.IdempotencyKey != "tol-1" ||
		s.created.RequestHash == "" || s.created.Tolerance.PartialDelivery != domain.PartialCompleteOnly || s.created.Tolerance.UnderdeliveryPct.String() != "2.5" {
		t.Fatalf("create: %d %+v", rec.Code, s.created)
	}
	// Absent figures are 0 and the policy is ALLOWED, for every customer.
	if rec := serveBody(t, s, http.MethodPost, deliveryTolerances, pre+`}`, principal(dutyParameter), key); rec.Code != http.StatusCreated ||
		s.created.CustomerID != nil || !s.created.Tolerance.OverdeliveryPct.IsZero() || s.created.Tolerance.PartialDelivery != domain.PartialAllowed {
		t.Fatalf("defaults: %d %+v", rec.Code, s.created)
	}
	s.replayed = true
	if rec := serveBody(t, s, http.MethodPost, deliveryTolerances, body, principal(dutyParameter), key); rec.Code != http.StatusOK {
		t.Fatalf("replay answers 200: %d", rec.Code)
	}
	scoped := principal(dutyParameter)
	scoped.LegalEntityID = ids.New()
	s.created = nil
	if rec := serveBody(t, s, http.MethodPost, deliveryTolerances, body, scoped, key); rec.Code != http.StatusForbidden || s.created != nil {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}

	if rec := serve(t, s, http.MethodGet, deliveryTolerances+"?productId="+product+"&customerId="+customer+"&limit=10", principal(dutyRead), nil); rec.Code != http.StatusOK ||
		s.filter.ProductID != product || s.filter.CustomerID != customer || s.filter.Limit != 10 || s.filter.LegalEntityID != testLE {
		t.Fatalf("list: %d %+v", rec.Code, s.filter)
	}
	for _, q := range []string{"?productId=x", "?customerId=x", "?limit=0"} {
		if rec := serve(t, s, http.MethodGet, deliveryTolerances+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := serve(t, s, http.MethodGet, deliveryTolerances+"/nope", principal(dutyRead), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	id := ids.New()
	if rec := serve(t, s, http.MethodGet, deliveryTolerances+"/"+id, principal(dutyRead), nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d", rec.Code)
	}

	put := `{"overdeliveryPct":"10","underdeliveryPct":"0","partialDelivery":"ONE_SHIPMENT"}`
	if rec := serveBody(t, s, http.MethodPut, deliveryTolerances+"/"+id, put, principal(dutyParameter), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("If-Match is required: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPut, deliveryTolerances+"/"+id, put, principal(dutyParameter), map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || s.version != 3 || s.replaced.OverdeliveryPct.String() != "10" || s.replaced.PartialDelivery != domain.PartialOneShipment || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPut, deliveryTolerances+"/"+id, `{"overdeliveryPct":"101"}`, principal(dutyParameter), map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("put validates: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPut, deliveryTolerances+"/"+id, put, principal(dutyRead), map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("put needs sales.parameter.maintain: %d", rec.Code)
	}

	if rec := serve(t, s, http.MethodDelete, deliveryTolerances+"/"+id, principal(dutyRead), nil); rec.Code != http.StatusForbidden || s.deleted != "" {
		t.Fatalf("delete needs sales.parameter.maintain: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodDelete, deliveryTolerances+"/"+id, principal(dutyParameter), nil); rec.Code != http.StatusNoContent || s.deleted != id {
		t.Fatalf("delete: %d", rec.Code)
	}
}

func TestResolveDeliveryTolerance(t *testing.T) {
	product, customer := ids.New(), ids.New()
	s := &toleranceStore{resolved: store.ResolvedTolerance{ToleranceID: "r1", Source: domain.ToleranceCustomer,
		Tolerance: domain.DeliveryTolerance{OverdeliveryPct: tdec("5"), UnderdeliveryPct: tdec("2"), PartialDelivery: domain.PartialCompleteOnly}}}
	q := "?productId=" + product + "&customerId=" + customer
	if rec := serve(t, s, http.MethodGet, toleranceResolve+q, principal(dutyListing), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("resolve needs sales.order.read: %d", rec.Code)
	}
	rec := serve(t, s, http.MethodGet, toleranceResolve+q+"&orderedQty=1000", principal(dutyRead), nil)
	out := rec.Body.String()
	if rec.Code != http.StatusOK || s.resolve != [3]string{testLE, product, customer} || !strings.Contains(out, `"source":"CUSTOMER"`) ||
		!strings.Contains(out, `"minQty":"980"`) || !strings.Contains(out, `"maxQty":"1050"`) || !strings.Contains(out, `"partialDelivery":"COMPLETE_ONLY"`) {
		t.Fatalf("resolve: %d %s %v", rec.Code, out, s.resolve)
	}
	// Without orderedQty no range is returned.
	rec = serve(t, s, http.MethodGet, toleranceResolve+q, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "minQty") {
		t.Fatalf("no range without orderedQty: %d %s", rec.Code, rec.Body.String())
	}
	for name, bad := range map[string]string{
		"no product":       "?customerId=" + customer,
		"no customer":      "?productId=" + product,
		"quantity zero":    q + "&orderedQty=0",
		"quantity garbage": q + "&orderedQty=many",
		"entity not uuid":  q + "&legalEntityId=le",
	} {
		if rec := serve(t, s, http.MethodGet, toleranceResolve+bad, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	other := ids.New()
	if rec := serve(t, s, http.MethodGet, toleranceResolve+q+"&legalEntityId="+other, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Errorf("another legal entity is out of scope: %d", rec.Code)
	}
}

func tdec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
