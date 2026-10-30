package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// proposalStore records the product proposal calls.
type proposalStore struct {
	fakeStore
	rule      *domain.ProposalRule
	ruleActor string
	filter    *store.ProposalRuleFilter
	rules     []domain.ProposalRule
	deleted   string
	response  *store.NewProposalResponse
	replayed  bool
}

func (s *proposalStore) CreateProposalRule(_ context.Context, in domain.ProposalRule, actor string) (domain.ProposalRule, error) {
	s.rule, s.ruleActor = &in, actor
	in.ID = ids.New()
	return in, nil
}

func (s *proposalStore) ListProposalRules(_ context.Context, f store.ProposalRuleFilter) ([]domain.ProposalRule, error) {
	s.filter = &f
	if s.rules == nil {
		return []domain.ProposalRule{}, nil
	}
	return s.rules, nil
}

func (s *proposalStore) DeleteProposalRule(_ context.Context, id string) error {
	s.deleted = id
	return nil
}

func (s *proposalStore) RecordProposalResponse(_ context.Context, in store.NewProposalResponse) (store.ProposalResponse, bool, error) {
	s.response = &in
	return store.ProposalResponse{ID: ids.New(), RuleID: in.RuleID, Accepted: in.Accepted, RespondedBy: in.Actor}, !s.replayed, nil
}

func TestBuildProposalRule_Table(t *testing.T) {
	a, b := ids.New(), ids.New()
	le := testLE
	bad := "C9"
	cases := []struct {
		name string
		req  proposalRuleRequest
		keys []string
	}{
		{name: "valid for every customer", req: proposalRuleRequest{LegalEntityID: le, OrderedProductID: a, ProposedProductID: b, Reason: domain.ProposalAccessory}},
		{name: "products and legal entity must be UUIDs", req: proposalRuleRequest{LegalEntityID: "le", OrderedProductID: "FG-100", ProposedProductID: "ACC-1", Reason: domain.ProposalUpSell},
			keys: []string{"legalEntityId", "orderedProductId", "proposedProductId"}},
		{name: "customer must be a UUID", req: proposalRuleRequest{LegalEntityID: le, OrderedProductID: a, ProposedProductID: b, CustomerID: &bad, Reason: domain.ProposalCrossSell},
			keys: []string{"customerId"}},
		{name: "self proposal and unknown reason", req: proposalRuleRequest{LegalEntityID: le, OrderedProductID: a, ProposedProductID: a, Reason: "BUNDLE"},
			keys: []string{"proposedProductId", "reason"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := buildProposalRule(tc.req)
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

func TestProposalRuleHandlers(t *testing.T) {
	fg, acc := ids.New(), ids.New()
	le := testLE
	s := &proposalStore{}
	body := `{"legalEntityId":"` + le + `","orderedProductId":"` + fg + `","proposedProductId":"` + acc + `","reason":"ACCESSORY"}`
	if rec := serveBody(t, s, http.MethodPost, proposalRules, body, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs sales.listing.maintain: %d", rec.Code)
	}
	scoped := principal(dutyListing)
	scoped.LegalEntityID = ids.New()
	if rec := serveBody(t, s, http.MethodPost, proposalRules, body, scoped, nil); rec.Code != http.StatusForbidden || s.rule != nil {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, proposalRules, body, principal(dutyListing), nil)
	if rec.Code != http.StatusCreated || s.ruleActor != "u1" || s.rule.Reason != domain.ProposalAccessory || s.rule.CustomerID != nil {
		t.Fatalf("create: status %d %+v", rec.Code, s.rule)
	}
	if rec := serveBody(t, s, http.MethodPost, proposalRules, `{"legalEntityId":"`+le+`","orderedProductId":"`+fg+`","proposedProductId":"`+fg+`","reason":"ACCESSORY"}`,
		principal(dutyListing), nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "proposedProductId") {
		t.Fatalf("self proposal: %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(t, s, http.MethodGet, proposalRules+"?legalEntityId="+le+"&orderedProductId="+fg+"&orderedProductId="+acc+"&limit=20", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.filter.LegalEntityID != le || len(s.filter.OrderedProductIDs) != 2 || s.filter.Limit != 20 {
		t.Fatalf("list: %d %+v", rec.Code, s.filter)
	}
	if rec := serve(t, s, http.MethodGet, proposalRules+"?orderedProductId=FG-100", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad product filter: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, proposalRules+"?limit=0", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}

	if rec := serve(t, s, http.MethodDelete, proposalRules+"/x", principal(dutyListing), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	id := ids.New()
	if rec := serve(t, s, http.MethodDelete, proposalRules+"/"+id, principal(dutyRead), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("delete needs sales.listing.maintain: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodDelete, proposalRules+"/"+id, principal(dutyListing), nil); rec.Code != http.StatusNoContent || s.deleted != id {
		t.Fatalf("delete: %d", rec.Code)
	}
}

func TestEvaluateProposalsHandler(t *testing.T) {
	fg, acc, up, customer := ids.New(), ids.New(), ids.New(), ids.New()
	le := testLE
	s := &proposalStore{rules: []domain.ProposalRule{
		{ID: "r1", LegalEntityID: le, OrderedProductID: fg, ProposedProductID: acc, Reason: domain.ProposalAccessory},
		{ID: "r2", LegalEntityID: le, OrderedProductID: fg, ProposedProductID: up, Reason: domain.ProposalUpSell},
	}}
	body := `{"legalEntityId":"` + le + `","customerId":"` + customer + `","productIds":["` + fg + `"],"currentProductIds":["` + up + `"]}`
	rec := serveBody(t, s, http.MethodPost, proposalEval, body, principal(dutyRead), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("evaluate: %d %s", rec.Code, rec.Body.String())
	}
	var got proposalEvaluation
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 1 || got.Items[0].ProposedProductID != acc || got.Items[0].RuleID != "r1" {
		t.Fatalf("the product already on the order is not proposed: %s", rec.Body.String())
	}
	if s.filter.LegalEntityID != le || len(s.filter.OrderedProductIDs) != 1 || s.filter.OrderedProductIDs[0] != fg {
		t.Fatalf("rules read with %+v", s.filter)
	}
	s.rules = []domain.ProposalRule{}
	rec = serveBody(t, s, http.MethodPost, proposalEval, `{"legalEntityId":"`+le+`","productIds":["`+fg+`"]}`, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"items\":[]}\n" {
		t.Fatalf("no rule is an empty list, not null: %s", rec.Body.String())
	}
	many := make([]string, maxProposalProducts+1)
	for i := range many {
		many[i] = `"` + ids.New() + `"`
	}
	for name, b := range map[string]string{
		"no products":      `{"legalEntityId":"` + le + `","productIds":[]}`,
		"too many":         `{"legalEntityId":"` + le + `","productIds":[` + strings.Join(many, ",") + `]}`,
		"non-UUID product": `{"legalEntityId":"` + le + `","productIds":["FG-100"]}`,
		"no legal entity":  `{"productIds":["` + fg + `"]}`,
		"bad customer":     `{"legalEntityId":"` + le + `","customerId":"C1","productIds":["` + fg + `"]}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, proposalEval, b, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if rec := serveBody(t, s, http.MethodPost, proposalEval, body, principal(dutyListing), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("evaluate needs sales.order.read: %d", rec.Code)
	}
}

func TestRespondToProposalHandler(t *testing.T) {
	rule, order := ids.New(), ids.New()
	le := testLE
	s := &proposalStore{}
	body := `{"legalEntityId":"` + le + `","ruleId":"` + rule + `","orderId":"` + order + `","accepted":true}`
	key := map[string]string{idempotencyKey: "k-1"}
	if rec := serveBody(t, s, http.MethodPost, proposalRespond, body, principal(dutyRead), key); rec.Code != http.StatusForbidden {
		t.Fatalf("respond needs sales.order.write or sales.channel.intake: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, proposalRespond, body, principal(dutyWrite), nil); rec.Code != http.StatusBadRequest || s.response != nil {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	rec := serveBody(t, s, http.MethodPost, proposalRespond, body, principal(dutyWrite), key)
	if rec.Code != http.StatusCreated || s.response.IdempotencyKey != "k-1" || !s.response.Accepted || s.response.Actor != "u1" ||
		s.response.OrderID == nil || *s.response.OrderID != order || s.response.RequestHash == "" || s.response.At.IsZero() {
		t.Fatalf("respond: %d %+v", rec.Code, s.response)
	}
	first := s.response.RequestHash
	s.replayed = true
	if rec := serveBody(t, s, http.MethodPost, proposalRespond, body, principal(dutyIntake), key); rec.Code != http.StatusOK || s.response.RequestHash != first {
		t.Fatalf("a replay answers 200 with the same request hash: %d", rec.Code)
	}
	for name, b := range map[string]string{
		"accepted missing": `{"legalEntityId":"` + le + `","ruleId":"` + rule + `"}`,
		"bad rule":         `{"legalEntityId":"` + le + `","ruleId":"r1","accepted":false}`,
		"bad order":        `{"legalEntityId":"` + le + `","ruleId":"` + rule + `","orderId":"SO-1","accepted":false}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, proposalRespond, b, principal(dutyWrite), key); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	scoped := principal(dutyWrite)
	scoped.LegalEntityID = ids.New()
	if rec := serveBody(t, s, http.MethodPost, proposalRespond, body, scoped, key); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity is out of scope: %d", rec.Code)
	}
}
