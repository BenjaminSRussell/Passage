package store

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// TestPostgres_AlternativeRules covers FR-27.13 storage: one rule per ordered
// product, customer (or all customers) and alternative; validity days and
// priority round-trip; the list orders by priority; the stored rules feed the
// offer; removal is 404 the second time.
func TestPostgres_AlternativeRules(t *testing.T) {
	s, _, ctx := migrated(t)
	le, fg100, alt1, alt2 := ids.New(), ids.New(), ids.New(), ids.New()
	from, to := "2027-03-01", "2027-06-30"
	general, err := s.CreateAlternativeRule(ctx, domain.AlternativeRule{LegalEntityID: le, OrderedProductID: fg100,
		AlternativeProductID: alt2, Priority: 2}, "planner")
	if err != nil || general.ID == "" {
		t.Fatalf("create: %v %+v", err, general)
	}
	if _, err := s.CreateAlternativeRule(ctx, domain.AlternativeRule{LegalEntityID: le, OrderedProductID: fg100,
		AlternativeProductID: alt2, Priority: 5}, "planner"); problemCode(t, err) != CodeAlternativeDuplicate {
		t.Fatalf("a second all-customers rule for the same pair must be refused: %v", err)
	}
	c9 := ids.New()
	own, err := s.CreateAlternativeRule(ctx, domain.AlternativeRule{LegalEntityID: le, OrderedProductID: fg100,
		AlternativeProductID: alt1, CustomerID: &c9, Priority: 1, ValidFrom: &from, ValidTo: &to}, "planner")
	if err != nil {
		t.Fatalf("a customer rule: %v", err)
	}
	if _, err := s.CreateAlternativeRule(ctx, domain.AlternativeRule{LegalEntityID: ids.New(), OrderedProductID: fg100,
		AlternativeProductID: alt1, Priority: 1}, "planner"); err != nil {
		t.Fatal(err)
	}

	rules, err := s.ListAlternativeRules(ctx, AlternativeRuleFilter{LegalEntityID: le, OrderedProductIDs: []string{fg100}, Limit: 50})
	if err != nil || len(rules) != 2 || rules[0].ID != own.ID || rules[1].ID != general.ID {
		t.Fatalf("list by priority: %v %+v", err, rules)
	}
	if rules[0].ValidFrom == nil || *rules[0].ValidFrom != from || *rules[0].ValidTo != to || rules[0].CustomerID == nil || *rules[0].CustomerID != c9 {
		t.Fatalf("validity and customer must round-trip: %+v", rules[0])
	}
	if rules[1].ValidFrom != nil || rules[1].ValidTo != nil || rules[1].CustomerID != nil {
		t.Fatalf("an open rule has no validity or customer: %+v", rules[1])
	}
	if other, err := s.ListAlternativeRules(ctx, AlternativeRuleFilter{LegalEntityID: le, OrderedProductIDs: []string{ids.New()}, Limit: 50}); err != nil || len(other) != 0 {
		t.Fatalf("another product has no rules: %v %+v", err, other)
	}
	offered := domain.ApplicableAlternatives(rules, c9, fg100, day("2027-04-01"))
	if len(offered) != 2 || offered[0].AlternativeProductID != alt1 {
		t.Fatalf("C9's alternative first: %+v", offered)
	}
	if offered := domain.ApplicableAlternatives(rules, c9, fg100, day("2027-07-01")); len(offered) != 1 || offered[0].ID != general.ID {
		t.Fatalf("after the validity ends only the general rule remains: %+v", offered)
	}

	if err := s.DeleteAlternativeRule(ctx, own.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAlternativeRule(ctx, own.ID); problemCode(t, err) != CodeAlternativeNotFound {
		t.Fatalf("removing twice: %v", err)
	}
}

// TestPostgres_AlternativeRuleScope: a rule is invisible to a caller scoped to
// another legal entity or tenant, who can neither list nor remove it, and a
// caller cannot store a rule for a legal entity outside its scope.
func TestPostgres_AlternativeRuleScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le, fg, alt := ids.New(), ids.New(), ids.New()
	rule, err := s.CreateAlternativeRule(ctx, domain.AlternativeRule{LegalEntityID: le, OrderedProductID: fg, AlternativeProductID: alt, Priority: 1}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	strangerLE := ids.New()
	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: strangerLE, UserID: "x", Duties: testkit.ScopeDuties(strangerLE)}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if list, err := s.ListAlternativeRules(c, AlternativeRuleFilter{OrderedProductIDs: []string{fg}, Limit: 50}); err != nil || len(list) != 0 {
			t.Fatalf("%s: list %v %+v", name, err, list)
		}
		if err := s.DeleteAlternativeRule(c, rule.ID); problemCode(t, err) != CodeAlternativeNotFound {
			t.Fatalf("%s: delete %v", name, err)
		}
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: strangerLE, UserID: "x", Duties: testkit.ScopeDuties(strangerLE)})
	if _, err := s.CreateAlternativeRule(scoped, domain.AlternativeRule{LegalEntityID: le, OrderedProductID: fg, AlternativeProductID: ids.New(), Priority: 1}, "x"); err == nil {
		t.Fatal("a rule for a legal entity outside the caller's scope must be refused by row-level security")
	}
	own := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "x", Duties: testkit.ScopeDuties(le)})
	if list, err := s.ListAlternativeRules(own, AlternativeRuleFilter{OrderedProductIDs: []string{fg}, Limit: 50}); err != nil || len(list) != 1 || list[0].ID != rule.ID {
		t.Fatalf("own legal entity: %v %+v", err, list)
	}
	if err := s.DeleteAlternativeRule(own, rule.ID); err != nil {
		t.Fatalf("own legal entity removes the rule: %v", err)
	}
}
