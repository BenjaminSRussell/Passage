package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func TestBillingRule_PutVersionsAndSelfBilledWindow(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le, customer := ids.New(), ids.New()
	if r, found, err := s.GetBillingRule(ctx, le, customer); err != nil || found || r.Rule != RuleStandard {
		t.Fatalf("no rule: %+v %v %v", r, found, err)
	}
	from, to := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)
	got, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: customer, Rule: RuleSelfBilled, AgreementRef: "SBA-1", ValidFrom: &from, ValidTo: &to, UpdatedBy: "clerk"}, 0)
	if err != nil || got.Version != 1 {
		t.Fatalf("put: %+v %v", got, err)
	}
	if _, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: customer, Rule: RuleStandard, UpdatedBy: "clerk"}, 0); !errors.Is(err, ErrRuleVersion) {
		t.Fatalf("a second create is a version conflict: %v", err)
	}
	r, found, err := s.GetBillingRule(ctx, le, customer)
	if err != nil || !found || !r.SelfBilledOn(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)) || r.SelfBilledOn(time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		r.SelfBilledOn(time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC)) || !r.SelfBilledOn(to) {
		t.Fatalf("window: %+v %v %v", r, found, err)
	}
	if got, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: customer, Rule: RuleStandard, UpdatedBy: "clerk"}, 1); err != nil || got.Version != 2 || got.Rule != RuleStandard {
		t.Fatalf("update: %+v %v", got, err)
	}
	// SELF_BILLED without the agreement is refused by the table too.
	if _, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: ids.New(), Rule: RuleSelfBilled, UpdatedBy: "clerk"}, 0); err == nil {
		t.Fatal("SELF_BILLED without agreement stored")
	}
}
