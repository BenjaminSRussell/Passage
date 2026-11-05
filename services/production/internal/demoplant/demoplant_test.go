package demoplant

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/ledgertest"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func TestStableID_IsAStableUUID(t *testing.T) {
	a, b := StableID("demo-plant", "le", "site"), StableID("demo-plant", "le", "site")
	if a != b || !ids.Valid(a) || a == StableID("demo-plant", "le", "warehouse") || StableID("ab", "c") == StableID("a", "bc") {
		t.Fatalf("%s %s", a, b)
	}
}

func seeded(t *testing.T, tenantID string) (*store.Postgres, context.Context, string) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: le, UserID: actor, Duties: map[string]struct{}{}})
	return store.New(pool), ctx, le
}

// subledgerWIP is the WIP the demo set leaves: 4951.00 on the order in progress (3111 material, 1380 labor, 460 overhead),
// 1951.00 on the partially reported one, nothing on the estimated, released and ended ones.
var subledgerWIP = decimal.RequireFromString("6902")

func checkOrders(t *testing.T, res Result, existing bool) {
	t.Helper()
	want := map[string]domain.Status{"bracket-estimated": domain.Estimated, "bracket-released": domain.Released,
		"housing-in-progress": domain.Started, "housing-partial-raf": domain.ReportedAsFinished, "gearbox-ended": domain.Ended}
	if len(res.Orders) != len(want) {
		t.Fatalf("orders: %+v", res.Orders)
	}
	for _, o := range res.Orders {
		if o.Status != want[o.Name] || o.Existing != existing || o.Number == "" {
			t.Fatalf("%s: %+v (want %s, existing %v)", o.Name, o, want[o.Name], existing)
		}
	}
}

// A second run under the same prefix creates nothing and books nothing new.
func TestSeed_ReplaySafe(t *testing.T) {
	s, ctx, le := seeded(t, testkit.TenantID(9120))
	fake := ledgerclient.NewFake()
	cfg := Config{LegalEntityID: le, Ledger: fake, LedgerID: ids.New()}
	first, err := Seed(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	checkOrders(t, first, false)
	if !first.ProfileCreated || len(first.GL.Failed) != 0 || len(first.GL.Booked) == 0 || !first.Reconciliation.Reconciled {
		t.Fatalf("first run: %+v %+v", first.GL, first.Reconciliation)
	}
	entries := len(fake.Entries())
	again, err := Seed(ctx, s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	checkOrders(t, again, true)
	if again.ProfileCreated || len(again.GL.Booked) != 0 || len(fake.Entries()) != entries || !again.Reconciliation.Reconciled {
		t.Fatalf("second run: %+v %+v", again.GL, again.Reconciliation)
	}
	other, err := Seed(ctx, s, Config{LegalEntityID: le, Prefix: "demo-plant-2"})
	if err != nil || other.Orders[0].ID == first.Orders[0].ID || other.Orders[0].Existing {
		t.Fatalf("another prefix seeds a fresh set: %+v %v", other.Orders, err)
	}
}

// W21 AC4 (#478): the demo plant booked into the real ledger service reconciles, WIP account by account.
func TestSeed_RealLedger_BooksAndReconciles(t *testing.T) {
	led := ledgertest.Start(t)
	tenantID := testkit.TenantID(9121)
	s, ctx, le := seeded(t, tenantID)
	ledgerID := led.CreateLedger(t, tenantID, le, "DEMO", "USD", Accounts())
	res, err := Seed(ctx, s, Config{LegalEntityID: le, Ledger: led.Client, LedgerID: ledgerID})
	if err != nil {
		t.Fatal(err)
	}
	checkOrders(t, res, false)
	if len(res.GL.Failed) != 0 || len(res.GL.Booked) == 0 || res.GL.Remaining != 0 {
		t.Fatalf("booking: %+v", res.GL)
	}
	r := res.Reconciliation
	if !r.Reconciled || len(r.Accounts) != 1 || r.Accounts[0].Account != Profile.WIP || !r.Accounts[0].GL.Equal(subledgerWIP) || !r.Accounts[0].Unbooked.IsZero() {
		t.Fatalf("reconciliation: %+v", r)
	}
	for acct, want := range map[string]string{Profile.VarQuantity: "1890", Profile.VarPrice: "61"} {
		b, err := led.Client.Balance(ctx, ledgerclient.BalanceQuery{LegalEntityID: le, LedgerID: ledgerID, Account: acct})
		if err != nil || !b.Net().Equal(decimal.RequireFromString(want)) {
			t.Fatalf("GL %s = %s, want %s (%v)", acct, b.Net(), want, err)
		}
	}
}
