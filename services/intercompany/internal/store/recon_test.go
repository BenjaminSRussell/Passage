package store_test

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

func reconFixture(t *testing.T) (domain.ReconRun, []domain.ReconItem) {
	t.Helper()
	items := []domain.ReconItem{
		{Entity: "A", Counterparty: "B", Ref: "P-1", AmountA: decimal.RequireFromString("100"), AmountB: decimal.RequireFromString("100"), Status: domain.ReconMatched, Resolution: domain.ResolutionNone},
		{Entity: "A", Counterparty: "B", Ref: "P-2", AmountA: decimal.RequireFromString("50"), Status: domain.ReconMismatch, Difference: decimal.RequireFromString("50"), Resolution: domain.ResolutionOpen},
	}
	return domain.ReconRun{Period: "2026-11", Currency: "USD", FXTolerancePct: decimal.RequireFromString("0.5"), Matched: 1, Mismatch: 1}, items
}

func TestReconRun_IsImmutableExceptResolvingAnOpenItem(t *testing.T) {
	s, pool, ctx := setup(t)
	run, items := reconFixture(t)
	created, _, err := s.CreateReconRun(ctx, run, items, "k1", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Open != 1 || created.Mismatch != 1 {
		t.Fatalf("run = %+v", created)
	}
	p, _ := tenant.FromContext(ctx)
	for name, stmt := range map[string]string{
		"change an amount":       `update ic_reconciliation_item set amount_b = 100 where tenant_id = $1 and ref = 'P-2'`,
		"re-open a matched item": `update ic_reconciliation_item set resolution = 'open' where tenant_id = $1 and ref = 'P-1'`,
		"delete an item":         `delete from ic_reconciliation_item where tenant_id = $1`,
		"delete the run":         `delete from ic_reconciliation_run where tenant_id = $1`,
		"change the run":         `update ic_reconciliation_run set mismatch = 0 where tenant_id = $1`,
	} {
		if _, err := pool.Exec(ctx, stmt, p.TenantID); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Errorf("%s must be refused by the database, got %v", name, err)
		}
	}
	_, got, err := s.GetReconRun(ctx, created.ID, "Mismatch")
	if err != nil || len(got) != 1 {
		t.Fatalf("items = %v %v", got, err)
	}
	if _, _, err := s.ResolveItem(ctx, got[0].ID, "credit note raised"); err != nil {
		t.Fatalf("resolving an open item is the one allowed change: %v", err)
	}
	events := outbox(t, pool, ctx)
	if len(events) != 1 || events[0]["_type"] != "dev.passage.interco.reconciliation.flagged.v1" {
		t.Fatalf("events = %v", events)
	}
}
