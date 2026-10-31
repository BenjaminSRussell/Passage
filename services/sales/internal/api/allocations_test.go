package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type quotaStore struct {
	fakeStore
	created  *store.NewAllocationQuota
	replayed bool
	filter   *store.AllocationFilter
	changed  decimal.Decimal
	version  int
	err      error
}

func (s *quotaStore) CreateAllocationQuota(_ context.Context, in store.NewAllocationQuota) (store.AllocationQuota, bool, error) {
	s.created = &in
	return store.AllocationQuota{ID: ids.New(), ProductID: in.ProductID, QuotaQty: in.Quantity, Version: 1}, !s.replayed, s.err
}

func (s *quotaStore) ListAllocationQuotas(_ context.Context, f store.AllocationFilter) ([]store.AllocationQuota, error) {
	s.filter = &f
	return []store.AllocationQuota{}, nil
}

func (s *quotaStore) ChangeAllocationQuota(_ context.Context, id string, version int, qty decimal.Decimal, _ string, _ time.Time) (store.AllocationQuota, error) {
	s.changed, s.version = qty, version
	return store.AllocationQuota{ID: id, QuotaQty: qty, Version: version + 1}, s.err
}

func TestBuildAllocationQuota_Table(t *testing.T) {
	bad := "C1"
	cases := []struct {
		name string
		req  allocationQuotaRequest
		keys []string
	}{
		{"valid for every customer", allocationQuotaRequest{LegalEntityID: testLE, ProductID: ids.New(), PeriodStart: "2027-03-01", PeriodEnd: "2027-03-31", QuotaQty: "100"}, nil},
		{"ids must be UUIDs", allocationQuotaRequest{LegalEntityID: "le", ProductID: "FG", CustomerID: &bad, PeriodStart: "2027-03-01", PeriodEnd: "2027-03-31", QuotaQty: "1"},
			[]string{"legalEntityId", "productId", "customerId"}},
		{"dates and quantity", allocationQuotaRequest{LegalEntityID: testLE, ProductID: ids.New(), PeriodStart: "March", PeriodEnd: "2027-13-01", QuotaQty: "-2"},
			[]string{"periodStart", "periodEnd", "quotaQty"}},
		{"end before start and fractional quantity", allocationQuotaRequest{LegalEntityID: testLE, ProductID: ids.New(), PeriodStart: "2027-03-31", PeriodEnd: "2027-03-01", QuotaQty: "1.1234567"},
			[]string{"periodEnd", "quotaQty"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := buildAllocationQuota(tc.req)
			if len(got) != len(tc.keys) {
				t.Fatalf("fields %v, want keys %v", got, tc.keys)
			}
			for _, k := range tc.keys {
				if _, ok := got[k]; !ok {
					t.Fatalf("missing %q in %v", k, got)
				}
			}
		})
	}
}

func TestAllocationQuotaHandlers(t *testing.T) {
	s := &quotaStore{}
	product := ids.New()
	body := `{"legalEntityId":"` + testLE + `","productId":"` + product + `","periodStart":"2027-03-01","periodEnd":"2027-03-31","quotaQty":"100"}`
	key := map[string]string{idempotencyKey: "q-1"}
	if rec := serveBody(t, s, http.MethodPost, allocationQuota, body, principal(dutyRead), key); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs sales.allocation.maintain: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, allocationQuota, body, principal(dutyAllocation), nil); rec.Code != http.StatusBadRequest || s.created != nil {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, allocationQuota, body, principal(dutyAllocation), key)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || s.created.Actor != "u1" || s.created.IdempotencyKey != "q-1" || s.created.RequestHash == "" {
		t.Fatalf("create: %d %+v", rec.Code, s.created)
	}
	s.replayed = true
	if rec := serveBody(t, s, http.MethodPost, allocationQuota, body, principal(dutyAllocation), key); rec.Code != http.StatusOK {
		t.Fatalf("replay answers 200: %d", rec.Code)
	}
	scoped := principal(dutyAllocation)
	scoped.LegalEntityID = ids.New()
	s.created = nil
	if rec := serveBody(t, s, http.MethodPost, allocationQuota, body, scoped, key); rec.Code != http.StatusForbidden || s.created != nil {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}

	if rec := serve(t, s, http.MethodGet, allocationQuota+"?productId="+product+"&onDate=2027-03-15&limit=10", principal(dutyRead), nil); rec.Code != http.StatusOK ||
		s.filter.ProductID != product || s.filter.OnDate != "2027-03-15" || s.filter.Limit != 10 || s.filter.LegalEntityID != testLE {
		t.Fatalf("list: %d %+v", rec.Code, s.filter)
	}
	for _, q := range []string{"?productId=x", "?customerId=x", "?onDate=soon", "?limit=0"} {
		if rec := serve(t, s, http.MethodGet, allocationQuota+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := serve(t, s, http.MethodGet, allocationQuota+"/nope", principal(dutyRead), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}

	id := ids.New()
	patch := `{"quotaQty":"120"}`
	if rec := serveBody(t, s, http.MethodPatch, allocationQuota+"/"+id, patch, principal(dutyAllocation), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("If-Match is required: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPatch, allocationQuota+"/"+id, patch, principal(dutyAllocation), map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || s.version != 3 || !s.changed.Equal(decimal.NewFromInt(120)) || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPatch, allocationQuota+"/"+id, `{"quotaQty":"0"}`, principal(dutyAllocation), map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "quotaQty") {
		t.Fatalf("zero quota: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPatch, allocationQuota+"/"+id, patch, principal(dutyRead), map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("patch needs sales.allocation.maintain: %d", rec.Code)
	}
}
