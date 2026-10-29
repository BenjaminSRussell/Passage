package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

// filterStore runs as passage_app when PASSAGE_TEST_APP_DATABASE_URL is set, so
// the tenant-2 assertions below are enforced by row-level security rather than
// only by the queries' tenant predicate.
func filterStore(t *testing.T) *Postgres {
	t.Helper()
	if os.Getenv(testkit.EnvAppURL) == "" {
		return New(migrated(t))
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(app)
}

// The scorecard filters rules by asset, rule type and owner (§9.46.14). Each
// filter narrows on its own and they combine with AND.
func TestListDQRules_FiltersByAssetTypeAndOwner(t *testing.T) {
	st := filterStore(t)
	ctx := ctxFor(1, "")
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	const otherOwner = "0193d6a0-0000-7000-8000-000000000002"

	taxID := vendorTaxRule()
	iban := vendorTaxRule()
	iban.RuleID, iban.Type, iban.Expression, iban.OwnerUserID = "vendor_iban_unique", domain.RuleUniqueness, "iban", otherOwner
	fresh := vendorTaxRule()
	fresh.RuleID, fresh.AssetFQN, fresh.Type, fresh.Expression = "journal_fresh", "mart.fin_journal_line", domain.RuleFreshness, "PT2H"
	for _, r := range []domain.DQRule{taxID, iban, fresh} {
		if _, _, err := st.PutDQRule(ctx, r, 0, false, now); err != nil {
			t.Fatalf("put %s: %v", r.RuleID, err)
		}
	}

	cases := []struct {
		name string
		f    RuleFilter
		want []string
	}{
		{"no filter", RuleFilter{}, []string{"vendor_iban_unique", "vendor_tax_id", "journal_fresh"}},
		{"asset", RuleFilter{Asset: "core.vendor"}, []string{"vendor_iban_unique", "vendor_tax_id"}},
		{"rule type", RuleFilter{RuleType: domain.RuleFreshness}, []string{"journal_fresh"}},
		{"owner", RuleFilter{OwnerUserID: otherOwner}, []string{"vendor_iban_unique"}},
		{"asset and owner", RuleFilter{Asset: "core.vendor", OwnerUserID: ruleOwner}, []string{"vendor_tax_id"}},
		{"no match", RuleFilter{Asset: "mart.fin_journal_line", RuleType: domain.RuleUniqueness}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ListDQRules(ctx, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rules %+v, want %v", len(got), got, tc.want)
			}
			for i, r := range got {
				if r.RuleID != tc.want[i] {
					t.Fatalf("rule %d = %s, want %s (order asset, rule id)", i, r.RuleID, tc.want[i])
				}
			}
		})
	}
	if got, err := st.ListDQRules(ctxFor(2, ""), RuleFilter{OwnerUserID: ruleOwner}); err != nil || len(got) != 0 {
		t.Fatalf("tenant 2 filtered by owner sees %+v, %v", got, err)
	}
}

// The archive partition inquiry filters by domain, period and status (§9.46.14).
func TestListArchivePartitions_FiltersByPeriod(t *testing.T) {
	st := filterStore(t)
	ctx := ctxFor(1, "")
	for _, p := range []struct{ domain, table, period string }{
		{"ledger", "journal_line", "2024-01"},
		{"ledger", "journal_line", "2024-02"},
		{"inventory", "inventory_trans", "2024-01"},
	} {
		if _, err := st.RegisterArchivePartition(ctx, p.domain, p.table, p.period, false, false); err != nil {
			t.Fatal(err)
		}
	}
	jan, err := st.ListArchivePartitions(ctx, PartitionFilter{Period: "2024-01"})
	if err != nil || len(jan) != 2 || jan[0].Period != "2024-01" || jan[1].Period != "2024-01" {
		t.Fatalf("period 2024-01 = %+v, %v", jan, err)
	}
	ledgerJan, err := st.ListArchivePartitions(ctx, PartitionFilter{Domain: "ledger", Period: "2024-01", Status: domain.ArchivePending})
	if err != nil || len(ledgerJan) != 1 || ledgerJan[0].Table != "journal_line" {
		t.Fatalf("ledger 2024-01 PENDING = %+v, %v", ledgerJan, err)
	}
	if none, err := st.ListArchivePartitions(ctx, PartitionFilter{Period: "2024-01", Status: domain.ArchiveDropped}); err != nil || len(none) != 0 {
		t.Fatalf("2024-01 DROPPED = %+v, %v", none, err)
	}
	if other, err := st.ListArchivePartitions(ctxFor(2, ""), PartitionFilter{Period: "2024-01"}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 sees %+v, %v", other, err)
	}
}
