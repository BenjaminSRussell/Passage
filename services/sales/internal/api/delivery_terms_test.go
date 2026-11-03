package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type termsStore struct {
	store.Store
	created  *store.NewDeliveryTerms
	replayed bool
	filter   *store.DeliveryTermsFilter
	replaced domain.DeliveryTerms
	version  int
}

func (s *termsStore) CreateDeliveryTerms(_ context.Context, in store.NewDeliveryTerms) (store.DeliveryTerms, bool, error) {
	s.created = &in
	return store.DeliveryTerms{ID: ids.New(), LegalEntityID: in.LegalEntityID, CustomerID: in.CustomerID, DeliveryTerms: in.Terms, Version: 1}, !s.replayed, nil
}

func (s *termsStore) ListDeliveryTerms(_ context.Context, f store.DeliveryTermsFilter) ([]store.DeliveryTerms, error) {
	s.filter = &f
	return []store.DeliveryTerms{}, nil
}

func (s *termsStore) GetDeliveryTerms(_ context.Context, id string) (store.DeliveryTerms, error) {
	return store.DeliveryTerms{ID: id, Version: 2}, nil
}

func (s *termsStore) ReplaceDeliveryTerms(_ context.Context, id string, version int, t domain.DeliveryTerms, _ string, _ time.Time) (store.DeliveryTerms, error) {
	s.replaced, s.version = t, version
	return store.DeliveryTerms{ID: id, DeliveryTerms: t, Version: version + 1}, nil
}

func TestDeliveryTermsHandlers(t *testing.T) {
	s := &termsStore{}
	customer := ids.New()
	body := `{"legalEntityId":"` + testLE + `","customerId":"` + customer + `","transportDays":2,"receivingDays":[1,2,3,4,5]}`
	key := map[string]string{idempotencyKey: "t-1"}
	if rec := serveBody(t, s, http.MethodPost, deliveryTerms, body, principal(dutyRead), key); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs sales.parameter.maintain: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, deliveryTerms, body, principal(dutyParameter), nil); rec.Code != http.StatusBadRequest || s.created != nil {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	for name, bad := range map[string]string{
		"no weekdays":        `{"legalEntityId":"` + testLE + `","customerId":"` + customer + `","transportDays":2,"receivingDays":[]}`,
		"weekday 8":          `{"legalEntityId":"` + testLE + `","customerId":"` + customer + `","transportDays":2,"receivingDays":[8]}`,
		"transport too long": `{"legalEntityId":"` + testLE + `","customerId":"` + customer + `","transportDays":61,"receivingDays":[1]}`,
		"customer not uuid":  `{"legalEntityId":"` + testLE + `","customerId":"C1","transportDays":2,"receivingDays":[1]}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, deliveryTerms, bad, principal(dutyParameter), key); rec.Code != http.StatusBadRequest || s.created != nil {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	rec := serveBody(t, s, http.MethodPost, deliveryTerms, body, principal(dutyParameter), key)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || s.created.Actor != "u1" || s.created.IdempotencyKey != "t-1" || s.created.RequestHash == "" {
		t.Fatalf("create: %d %+v", rec.Code, s.created)
	}
	s.replayed = true
	if rec := serveBody(t, s, http.MethodPost, deliveryTerms, body, principal(dutyParameter), key); rec.Code != http.StatusOK {
		t.Fatalf("replay answers 200: %d", rec.Code)
	}
	scoped := principal(dutyParameter)
	scoped.LegalEntityID = ids.New()
	s.created = nil
	if rec := serveBody(t, s, http.MethodPost, deliveryTerms, body, scoped, key); rec.Code != http.StatusForbidden || s.created != nil {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}

	if rec := serve(t, s, http.MethodGet, deliveryTerms+"?customerId="+customer+"&limit=10", principal(dutyRead), nil); rec.Code != http.StatusOK ||
		s.filter.CustomerID != customer || s.filter.Limit != 10 || s.filter.LegalEntityID != testLE {
		t.Fatalf("list: %d %+v", rec.Code, s.filter)
	}
	for _, q := range []string{"?customerId=x", "?limit=0"} {
		if rec := serve(t, s, http.MethodGet, deliveryTerms+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	if rec := serve(t, s, http.MethodGet, deliveryTerms+"/nope", principal(dutyRead), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	id := ids.New()
	if rec := serve(t, s, http.MethodGet, deliveryTerms+"/"+id, principal(dutyRead), nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d", rec.Code)
	}

	put := `{"transportDays":4,"receivingDays":[2,4]}`
	if rec := serveBody(t, s, http.MethodPut, deliveryTerms+"/"+id, put, principal(dutyParameter), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("If-Match is required: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPut, deliveryTerms+"/"+id, put, principal(dutyParameter), map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || s.version != 3 || s.replaced.TransportDays != 4 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPut, deliveryTerms+"/"+id, `{"transportDays":4,"receivingDays":[]}`, principal(dutyParameter), map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "receivingDays") {
		t.Fatalf("empty weekdays: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serveBody(t, s, http.MethodPut, deliveryTerms+"/"+id, put, principal(dutyRead), map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("put needs sales.parameter.maintain: %d", rec.Code)
	}
}

func TestParameters_DeliveryDateControl(t *testing.T) {
	path := parameters + "?legalEntityId=" + testLE
	s := &parametersStore{}
	base := `"quotationExpiryDays":30,"quotationProbabilityDefault":50,"creditReleaseValidityDays":7,"creditHoldSlaBusinessDays":2,"defaultOrderType":"ORDER"`
	ifm := map[string]string{"If-Match": `"0"`}
	rec := serveBody(t, s, http.MethodPut, path, `{`+base+`,"deliveryDateControlDefault":"SALES_LEAD_TIME","salesLeadTimeDays":4}`, principal(dutyParameter), ifm)
	if rec.Code != http.StatusOK || s.saved.DeliveryDateControlDefault != "SALES_LEAD_TIME" || s.saved.SalesLeadTimeDays != 4 {
		t.Fatalf("save: %d %s %+v", rec.Code, rec.Body.String(), s.saved)
	}
	rec = serveBody(t, s, http.MethodPut, path, `{`+base+`}`, principal(dutyParameter), ifm)
	if rec.Code != http.StatusOK || s.saved.DeliveryDateControlDefault != "NONE" || s.saved.SalesLeadTimeDays != 0 {
		t.Fatalf("absent values default to NONE and 0: %d %+v", rec.Code, s.saved)
	}
	puts := s.puts
	for name, tc := range map[string]struct{ extra, field string }{
		"ATP not active":  {`,"deliveryDateControlDefault":"ATP"`, "deliveryDateControlDefault"},
		"CTP not active":  {`,"deliveryDateControlDefault":"CTP"`, "deliveryDateControlDefault"},
		"unknown control": {`,"deliveryDateControlDefault":"FAST"`, "deliveryDateControlDefault"},
		"lead time over":  {`,"salesLeadTimeDays":366`, "salesLeadTimeDays"},
		"lead time below": {`,"salesLeadTimeDays":-1`, "salesLeadTimeDays"},
	} {
		rec := serveBody(t, s, http.MethodPut, path, `{`+base+tc.extra+`}`, principal(dutyParameter), ifm)
		if rec.Code != http.StatusBadRequest || s.puts != puts || !strings.Contains(rec.Body.String(), tc.field) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestBuildLine_DeliveryDateControl(t *testing.T) {
	valid := func(control *string) orderLineRequest {
		return orderLineRequest{ProductID: ids.New(), Quantity: "1", Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: "5",
			RequestedShipDate: "2027-03-04", DeliveryDateControl: control}
	}
	for _, c := range []string{"NONE", "SALES_LEAD_TIME"} {
		c := c
		f := map[string]string{}
		line, ok := buildLine(0, valid(&c), f, func(k string) string { return k })
		if !ok || len(f) != 0 || line.DeliveryDateControl == nil || *line.DeliveryDateControl != c {
			t.Errorf("%s: ok=%v fields=%v line=%+v", c, ok, f, line)
		}
	}
	for _, c := range []string{"ATP", "CTP", "FAST"} {
		c := c
		f := map[string]string{}
		if _, ok := buildLine(0, valid(&c), f, func(k string) string { return k }); ok || f["deliveryDateControl"] == "" {
			t.Errorf("%s must be refused: ok=%v %v", c, ok, f)
		}
	}
}
