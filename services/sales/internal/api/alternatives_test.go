package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// alternativeStore records the alternative item calls.
type alternativeStore struct {
	fakeStore
	rule      *domain.AlternativeRule
	ruleActor string
	filter    *store.AlternativeRuleFilter
	rules     []domain.AlternativeRule
	deleted   string
}

func (s *alternativeStore) CreateAlternativeRule(_ context.Context, in domain.AlternativeRule, actor string) (domain.AlternativeRule, error) {
	s.rule, s.ruleActor = &in, actor
	in.ID = ids.New()
	return in, nil
}

func (s *alternativeStore) ListAlternativeRules(_ context.Context, f store.AlternativeRuleFilter) ([]domain.AlternativeRule, error) {
	s.filter = &f
	if s.rules == nil {
		return []domain.AlternativeRule{}, nil
	}
	return s.rules, nil
}

func (s *alternativeStore) DeleteAlternativeRule(_ context.Context, id string) error {
	s.deleted = id
	return nil
}

func TestBuildAlternativeRule_Table(t *testing.T) {
	a, b := ids.New(), ids.New()
	bad, from, to := "C9", "2027-03-02", "2027-03-01"
	zero := 0
	cases := []struct {
		name string
		req  alternativeRuleRequest
		keys []string
	}{
		{"valid, priority defaults to 1", alternativeRuleRequest{LegalEntityID: testLE, OrderedProductID: a, AlternativeProductID: b}, nil},
		{"ids must be UUIDs", alternativeRuleRequest{LegalEntityID: "le", OrderedProductID: "FG-100", AlternativeProductID: "FG-110"},
			[]string{"legalEntityId", "orderedProductId", "alternativeProductId"}},
		{"customer must be a UUID", alternativeRuleRequest{LegalEntityID: testLE, OrderedProductID: a, AlternativeProductID: b, CustomerID: &bad}, []string{"customerId"}},
		{"same product, priority 0, dates reversed", alternativeRuleRequest{LegalEntityID: testLE, OrderedProductID: a, AlternativeProductID: a,
			Priority: &zero, ValidFrom: &from, ValidTo: &to}, []string{"alternativeProductId", "priority", "validTo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule, got := buildAlternativeRule(tc.req)
			if len(got) != len(tc.keys) {
				t.Fatalf("fields %v, want keys %v", got, tc.keys)
			}
			for _, k := range tc.keys {
				if _, ok := got[k]; !ok {
					t.Fatalf("missing %q in %v", k, got)
				}
			}
			if len(tc.keys) == 0 && rule.Priority != 1 {
				t.Fatalf("priority %d, want 1", rule.Priority)
			}
		})
	}
}

func TestAlternativeRuleHandlers(t *testing.T) {
	fg, alt := ids.New(), ids.New()
	le := testLE
	s := &alternativeStore{}
	body := `{"legalEntityId":"` + le + `","orderedProductId":"` + fg + `","alternativeProductId":"` + alt + `","priority":2,"validFrom":"2027-03-01"}`
	if rec := serveBody(t, s, http.MethodPost, alternativeRules, body, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs sales.listing.maintain: %d", rec.Code)
	}
	scoped := principal(dutyListing)
	scoped.LegalEntityID = ids.New()
	if rec := serveBody(t, s, http.MethodPost, alternativeRules, body, scoped, nil); rec.Code != http.StatusForbidden || s.rule != nil {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, alternativeRules, body, principal(dutyListing), nil)
	if rec.Code != http.StatusCreated || s.ruleActor != "u1" || s.rule.Priority != 2 || s.rule.ValidFrom == nil || s.rule.CustomerID != nil {
		t.Fatalf("create: status %d %+v", rec.Code, s.rule)
	}
	if rec := serveBody(t, s, http.MethodPost, alternativeRules, `{"legalEntityId":"`+le+`","orderedProductId":"`+fg+`","alternativeProductId":"`+fg+`"}`,
		principal(dutyListing), nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "alternativeProductId") {
		t.Fatalf("self alternative: %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(t, s, http.MethodGet, alternativeRules+"?legalEntityId="+le+"&orderedProductId="+fg+"&limit=20", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.filter.LegalEntityID != le || len(s.filter.OrderedProductIDs) != 1 || s.filter.Limit != 20 {
		t.Fatalf("list: %d %+v", rec.Code, s.filter)
	}
	if rec := serve(t, s, http.MethodGet, alternativeRules+"?orderedProductId=FG-100", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad product filter: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodDelete, alternativeRules+"/x", principal(dutyListing), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	id := ids.New()
	if rec := serve(t, s, http.MethodDelete, alternativeRules+"/"+id, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("delete needs sales.listing.maintain: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodDelete, alternativeRules+"/"+id, principal(dutyListing), nil); rec.Code != http.StatusNoContent || s.deleted != id {
		t.Fatalf("delete: %d", rec.Code)
	}
}

func TestEvaluateAlternativesHandler(t *testing.T) {
	fg, full, partial, later, unknown, customer := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	le := testLE
	s := &alternativeStore{rules: []domain.AlternativeRule{
		{ID: "r1", LegalEntityID: le, OrderedProductID: fg, AlternativeProductID: partial, Priority: 1},
		{ID: "r2", LegalEntityID: le, OrderedProductID: fg, AlternativeProductID: full, Priority: 2},
		{ID: "r3", LegalEntityID: le, OrderedProductID: fg, AlternativeProductID: later, Priority: 3},
		{ID: "r4", LegalEntityID: le, OrderedProductID: fg, AlternativeProductID: unknown, Priority: 4},
	}}
	bucket := func(date, supply string) string {
		return `[{"date":"` + date + `","supply":"` + supply + `","demand":"0"}]`
	}
	body := func(orderedSupply string) string {
		return `{"legalEntityId":"` + le + `","customerId":"` + customer + `","productId":"` + fg + `","quantity":"25","requested":"2027-03-01","periods":` +
			bucket("2027-03-01", orderedSupply) + `,"alternatives":[` +
			`{"productId":"` + partial + `","periods":` + bucket("2027-03-01", "10") + `},` +
			`{"productId":"` + full + `","periods":` + bucket("2027-03-01", "40") + `},` +
			`{"productId":"` + later + `","periods":` + bucket("2027-03-20", "40") + `}]}`
	}
	rec := serveBody(t, s, http.MethodPost, alternativeEval, body("5"), principal(dutyRead), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate: %d %s", rec.Code, rec.Body.String())
	}
	var got alternativeEvaluation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Needed || got.Ordered.CoversQuantity || !got.Ordered.AvailableOnDate.Equal(dec("5")) {
		t.Fatalf("ordered product: %+v", got.Ordered)
	}
	if len(got.Offers) != 2 || got.Offers[0].AlternativeProductID != full || !got.Offers[0].CoversQuantity ||
		got.Offers[1].AlternativeProductID != partial || got.Offers[1].CoversQuantity || !got.Offers[1].AvailableOnDate.Equal(dec("10")) {
		t.Fatalf("a covering alternative leads, the partial one follows, later supply is no offer: %+v", got.Offers)
	}
	if got.Offers[0].Promise.PromisedDate != "2027-03-01" {
		t.Fatalf("offer promise %+v", got.Offers[0].Promise)
	}
	if len(got.Unchecked) != 1 || got.Unchecked[0].AlternativeProductID != unknown {
		t.Fatalf("an alternative without buckets is reported unchecked: %+v", got.Unchecked)
	}
	if s.filter.LegalEntityID != le || len(s.filter.OrderedProductIDs) != 1 || s.filter.OrderedProductIDs[0] != fg {
		t.Fatalf("rules read with %+v", s.filter)
	}

	s.filter = nil
	rec = serveBody(t, s, http.MethodPost, alternativeEval, body("30"), principal(dutyRead), nil)
	got = alternativeEvaluation{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %v", rec.Code, err)
	}
	if got.Needed || !got.Ordered.CoversQuantity || len(got.Offers) != 0 || s.filter != nil {
		t.Fatalf("when the ordered product covers the quantity nothing is offered and no rule is read: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"offers":[]`) || !strings.Contains(rec.Body.String(), `"unchecked":[]`) {
		t.Fatalf("empty lists, not null: %s", rec.Body.String())
	}

	for name, b := range map[string]string{
		"no legal entity": `{"productId":"` + fg + `","quantity":"1","requested":"2027-03-01","periods":` + bucket("2027-03-01", "1") + `}`,
		"bad product":     `{"legalEntityId":"` + le + `","productId":"FG-100","quantity":"1","requested":"2027-03-01","periods":` + bucket("2027-03-01", "1") + `}`,
		"zero quantity":   `{"legalEntityId":"` + le + `","productId":"` + fg + `","quantity":"0","requested":"2027-03-01","periods":` + bucket("2027-03-01", "1") + `}`,
		"bad date":        `{"legalEntityId":"` + le + `","productId":"` + fg + `","quantity":"1","requested":"soon","periods":` + bucket("2027-03-01", "1") + `}`,
		"no buckets":      `{"legalEntityId":"` + le + `","productId":"` + fg + `","quantity":"1","requested":"2027-03-01","periods":[]}`,
		"bad alternative": `{"legalEntityId":"` + le + `","productId":"` + fg + `","quantity":"1","requested":"2027-03-01","periods":` + bucket("2027-03-01", "1") + `,"alternatives":[{"productId":"x","periods":[]}]}`,
		"bad customer":    `{"legalEntityId":"` + le + `","customerId":"C1","productId":"` + fg + `","quantity":"1","requested":"2027-03-01","periods":` + bucket("2027-03-01", "1") + `}`,
		"repeated alternative": `{"legalEntityId":"` + le + `","productId":"` + fg + `","quantity":"1","requested":"2027-03-01","periods":` + bucket("2027-03-01", "1") +
			`,"alternatives":[{"productId":"` + full + `","periods":` + bucket("2027-03-01", "1") + `},{"productId":"` + full + `","periods":` + bucket("2027-03-01", "9") + `}]}`,
		"negative supply": `{"legalEntityId":"` + le + `","productId":"` + fg + `","quantity":"1","requested":"2027-03-01","periods":` + bucket("2027-03-01", "-1") + `}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, alternativeEval, b, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := serveBody(t, s, http.MethodPost, alternativeEval, body("5"), principal(dutyListing), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("evaluate needs sales.order.read: %d", rec.Code)
	}
	scoped := principal(dutyRead)
	scoped.LegalEntityID = ids.New()
	if rec := serveBody(t, s, http.MethodPost, alternativeEval, body("5"), scoped, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
