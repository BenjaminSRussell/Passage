package api

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

type fakeXref struct {
	asked  []string
	system string
	le     string
	found  map[string]string
	err    error
}

func (f *fakeXref) Configured() bool { return true }
func (f *fakeXref) Resolve(_ context.Context, _, le, system, entity string, keys []string) (map[string]string, error) {
	f.asked, f.system, f.le = append([]string(nil), keys...), system, le
	if entity != "customer" {
		return nil, errors.New("entity " + entity)
	}
	return f.found, f.err
}

// A legacy customer with no Passage id on the record or in the map is resolved through the party migration's cross-reference
// (H-S8-6): company-qualified first, then the bare party; AP lines, UUID parties and mapped parties are not asked about.
func TestResolveCustomers_FillsTheMapFromTheCrossReference(t *testing.T) {
	const c1, c2, mapped = "0195f0a0-0000-7000-8000-0000000000c1", "0195f0a0-0000-7000-8000-0000000000c2", "0195f0a0-0000-7000-8000-0000000000c3"
	x := &fakeXref{found: map[string]string{"USMF|CUST-1": c1, "CUST-2": c2}}
	s := (&Server{}).WithXref(x)
	cfg := &domain.TargetConfig{LegalEntityID: "le-1", Customers: map[string]string{"CUST-9": mapped}}
	records := []domain.Record{
		{LegacyID: "1", Party: "CUST-1"}, {LegacyID: "2", Party: "CUST-2"}, {LegacyID: "3", Party: "CUST-9"},
		{LegacyID: "4", Party: c2}, {LegacyID: "5", Party: "VEND-1", Ledger: domain.LedgerAP}, {LegacyID: "6", Party: "CUST-3"}, {LegacyID: "7", Party: "CUST-1"},
	}
	if err := s.resolveCustomers(context.Background(), "t1", cfg, records, map[string]domain.TargetExtra{}, domain.SourceAX2012, "USMF"); err != nil {
		t.Fatal(err)
	}
	if x.system != "ax2012" || x.le != "le-1" || !slices.Equal(x.asked, []string{"USMF|CUST-1", "CUST-1", "USMF|CUST-2", "CUST-2", "USMF|CUST-3", "CUST-3"}) {
		t.Fatalf("asked %s %s %v", x.system, x.le, x.asked)
	}
	if cfg.Customers["CUST-1"] != c1 || cfg.Customers["CUST-2"] != c2 || cfg.Customers["CUST-9"] != mapped || cfg.Customers["CUST-3"] != "" {
		t.Fatalf("customers %v", cfg.Customers)
	}
	x.err = errors.New("down")
	cfg.Customers = map[string]string{}
	if err := s.resolveCustomers(context.Background(), "t1", cfg, records, map[string]domain.TargetExtra{}, domain.SourceAX2012, "USMF"); err == nil {
		t.Fatal("an unreachable cross-reference let the run start")
	}
}
