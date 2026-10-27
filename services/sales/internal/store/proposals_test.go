package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// TestPostgres_ProposalRules covers FR-27.21 storage: one rule per ordered
// product, customer (or all customers) and proposed product; the list filters
// by ordered products and legal entity; the stored rules feed the evaluation;
// removal keeps nothing behind.
func TestPostgres_ProposalRules(t *testing.T) {
	s, _, ctx := migrated(t)
	le, fg100, fg120, acc, up := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	rule := domain.ProposalRule{LegalEntityID: le, OrderedProductID: fg100, ProposedProductID: acc, Reason: domain.ProposalAccessory}
	saved, err := s.CreateProposalRule(ctx, rule, "product-admin")
	if err != nil || saved.ID == "" || saved.CustomerID != nil || saved.AcceptedCount != 0 {
		t.Fatalf("create: %v %+v", err, saved)
	}
	rule.Reason = domain.ProposalCrossSell
	if _, err := s.CreateProposalRule(ctx, rule, "product-admin"); problemCode(t, err) != CodeProposalDuplicate {
		t.Fatalf("a second all-customers rule for the same pair must be refused, whatever the reason: %v", err)
	}
	c9 := ids.New()
	mine := rule
	mine.CustomerID, mine.Reason = &c9, domain.ProposalUpSell
	own, err := s.CreateProposalRule(ctx, mine, "product-admin")
	if err != nil {
		t.Fatalf("a customer rule beside the all-customers rule: %v", err)
	}
	if _, err := s.CreateProposalRule(ctx, domain.ProposalRule{LegalEntityID: le, OrderedProductID: fg120, ProposedProductID: up,
		Reason: domain.ProposalUpSell}, "product-admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProposalRule(ctx, domain.ProposalRule{LegalEntityID: ids.New(), OrderedProductID: fg100, ProposedProductID: up,
		Reason: domain.ProposalUpSell}, "product-admin"); err != nil {
		t.Fatal(err)
	}

	rules, err := s.ListProposalRules(ctx, ProposalRuleFilter{LegalEntityID: le, OrderedProductIDs: []string{fg100}, Limit: 50})
	if err != nil || len(rules) != 2 || rules[0].ID != saved.ID || rules[1].ID != own.ID || rules[1].CustomerID == nil || *rules[1].CustomerID != c9 {
		t.Fatalf("list for FG-100: %v %+v", err, rules)
	}
	if all, err := s.ListProposalRules(ctx, ProposalRuleFilter{LegalEntityID: le, Limit: 50}); err != nil || len(all) != 3 {
		t.Fatalf("list of the legal entity: %v %+v", err, all)
	}
	got := domain.EvaluateProposals(rules, c9, []string{fg100}, nil)
	if len(got) != 1 || got[0].RuleID != own.ID || got[0].Reason != domain.ProposalUpSell {
		t.Fatalf("C9's own rule wins for the same product: %+v", got)
	}
	if got := domain.EvaluateProposals(rules, ids.New(), []string{fg100}, []string{acc}); len(got) != 0 {
		t.Fatalf("a product on the order is not proposed: %+v", got)
	}

	if err := s.DeleteProposalRule(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProposalRule(ctx, saved.ID); problemCode(t, err) != CodeProposalNotFound {
		t.Fatalf("second delete: %v", err)
	}
	if rules, err := s.ListProposalRules(ctx, ProposalRuleFilter{LegalEntityID: le, OrderedProductIDs: []string{fg100}, Limit: 50}); err != nil || len(rules) != 1 {
		t.Fatalf("after delete: %v %+v", err, rules)
	}
}

// TestPostgres_ProposalResponses covers the FR-27.21 acceptance rate: each
// accept or decline is stored once per Idempotency-Key (a retry, even a
// concurrent one, is not counted twice), the key with another body is
// refused, a rule of another legal entity is not found, and the counts survive
// in the history after the rule is removed.
func TestPostgres_ProposalResponses(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	rule, err := s.CreateProposalRule(ctx, domain.ProposalRule{LegalEntityID: le, OrderedProductID: ids.New(), ProposedProductID: ids.New(),
		Reason: domain.ProposalAccessory}, "product-admin")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2027, 3, 1, 9, 0, 0, 0, time.UTC)
	respond := func(key string, accepted bool) (ProposalResponse, bool, error) {
		return s.RecordProposalResponse(ctx, NewProposalResponse{LegalEntityID: le, RuleID: rule.ID, CustomerID: &customer, Accepted: accepted,
			IdempotencyKey: key, RequestHash: key + map[bool]string{true: "-yes", false: "-no"}[accepted], Actor: "sales-rep", At: at})
	}
	first, created, err := respond("k1", true)
	if err != nil || !created || !first.Accepted || first.ProposedProductID != rule.ProposedProductID || first.Reason != domain.ProposalAccessory ||
		first.CustomerID == nil || *first.CustomerID != customer || first.RespondedBy != "sales-rep" || !first.RespondedAt.Equal(at) {
		t.Fatalf("first response: created=%v err=%v %+v", created, err, first)
	}
	again, created, err := respond("k1", true)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("a replay returns the stored response: created=%v err=%v", created, err)
	}
	if _, _, err := respond("k1", false); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("the key with another body: %v", err)
	}
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, results[i] = respond("k2", false)
		}(i)
	}
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("concurrent replay %d: %v", i, err)
		}
	}
	if _, _, err := respond("k3", false); err != nil {
		t.Fatal(err)
	}
	rules, err := s.ListProposalRules(ctx, ProposalRuleFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(rules) != 1 || rules[0].AcceptedCount != 1 || rules[0].DeclinedCount != 2 {
		t.Fatalf("counts: %v %+v", err, rules)
	}

	other := ids.New()
	if _, _, err := s.RecordProposalResponse(ctx, NewProposalResponse{LegalEntityID: other, RuleID: rule.ID, Accepted: true,
		IdempotencyKey: "k4", RequestHash: "h4", Actor: "sales-rep", At: at}); problemCode(t, err) != CodeProposalNotFound {
		t.Fatalf("a rule outside the legal entity: %v", err)
	}
	otherTenant := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: map[string]struct{}{}})
	if _, _, err := s.RecordProposalResponse(otherTenant, NewProposalResponse{LegalEntityID: le, RuleID: rule.ID, Accepted: true,
		IdempotencyKey: "k5", RequestHash: "h5", Actor: "x", At: at}); problemCode(t, err) != CodeProposalNotFound {
		t.Fatalf("another tenant's rule: %v", err)
	}

	if err := s.DeleteProposalRule(ctx, rule.ID); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := pool.QueryRow(context.Background(), `select count(*) from product_proposal_response where tenant_id = $1 and rule_id = $2`,
		testkit.TenantID(1), rule.ID).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 3 {
		t.Fatalf("responses kept after the rule is removed: %d, want 3", kept)
	}
}
