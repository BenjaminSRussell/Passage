package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/passage/services/intercompany/migrations"
)

func pairing(seller, buyer string) domain.Partner {
	return domain.Partner{
		SellerEntity: seller, BuyerEntity: buyer, CustomerAccount: "IC-" + buyer, VendorAccount: "IC-" + seller,
		Active:    true,
		Actions:   domain.ActionPolicy{AutoCreate: true, AutoPostInvoice: true},
		Pricing:   domain.PriceCostPlus,
		MarkupPct: decimal.RequireFromString("10"),
		Tolerance: domain.DefaultTolerance(),
	}
}

func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(pool), pool, testkit.Ctx(testkit.TenantID(1))
}

func codeOf(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

// outbox returns the events of the tenant in ctx, oldest first.
func outbox(t *testing.T, pool *pgxpool.Pool, ctx context.Context) []map[string]any {
	t.Helper()
	p, _ := tenant.FromContext(ctx)
	rows, err := pool.Query(ctx, `select type, payload->'data' from outbox where tenant_id = $1 order by created_at, id`, p.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		data := map[string]any{"_type": typ}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data)
	}
	return out
}

func TestCreatePartner_RoundTripAndEvent(t *testing.T) {
	s, pool, ctx := setup(t)
	rec, replayed, err := s.CreatePartner(ctx, pairing("P", "S"), "")
	if err != nil || replayed {
		t.Fatalf("create: replayed=%v err=%v", replayed, err)
	}
	if !ids.Valid(rec.ID) || rec.Version != 1 || rec.CreatedAt.IsZero() {
		t.Fatalf("record not populated: %+v", rec)
	}
	got, err := s.GetPartner(ctx, rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SellerEntity != "P" || got.BuyerEntity != "S" || got.CustomerAccount != "IC-S" || got.VendorAccount != "IC-P" || got.Pricing != domain.PriceCostPlus {
		t.Errorf("round trip lost fields: %+v", got.Partner)
	}
	if got.MarkupPct.String() != "10" || got.Tolerance.PricePct.String() != "0.5" || got.Tolerance.DateDays != 2 {
		t.Errorf("decimals drifted: markup=%s price=%s days=%d", got.MarkupPct, got.Tolerance.PricePct, got.Tolerance.DateDays)
	}
	if !got.Actions.AutoCreate || !got.Actions.AutoPostInvoice || got.Actions.AutoConfirm || got.Actions.AutoPostPackingSlip {
		t.Errorf("action policy drifted: %+v", got.Actions)
	}
	evts := outbox(t, pool, ctx)
	if len(evts) != 1 || evts[0]["_type"] != "dev.passage.interco.partner.created.v1" || evts[0]["id"] != rec.ID || evts[0]["version"] != float64(1) {
		t.Fatalf("outbox = %v", evts)
	}
}

func TestCreatePartner_IdempotentReplayAndKeyReuse(t *testing.T) {
	s, pool, ctx := setup(t)
	first, replayed, err := s.CreatePartner(ctx, pairing("P", "S"), "key-1")
	if err != nil || replayed {
		t.Fatalf("create: replayed=%v err=%v", replayed, err)
	}
	again, replayed, err := s.CreatePartner(ctx, pairing("P", "S"), "key-1")
	if err != nil || !replayed || again.ID != first.ID || !again.MarkupPct.Equal(first.MarkupPct) {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.CreatePartner(ctx, pairing("P", "X"), "key-1"); codeOf(err) != domain.CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: got %v, want %s", err, domain.CodeIdempotencyReuse)
	}
	if n := len(outbox(t, pool, ctx)); n != 1 {
		t.Fatalf("a replay published again: %d events", n)
	}
	// The same key in another tenant is another request.
	other := testkit.Ctx(testkit.TenantID(2))
	if _, replayed, err := s.CreatePartner(other, pairing("P", "S"), "key-1"); err != nil || replayed {
		t.Fatalf("tenant 2 with the same key: replayed=%v err=%v", replayed, err)
	}
}

// TestCreatePartner_ConcurrentSameKey: two requests racing on one key create
// one pairing; the loser replays it.
func TestCreatePartner_ConcurrentSameKey(t *testing.T) {
	s, _, ctx := setup(t)
	var wg sync.WaitGroup
	results := make([]domain.PartnerRecord, 4)
	errs := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], _, errs[i] = s.CreatePartner(ctx, pairing("P", "S"), "race")
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if results[i].ID != results[0].ID {
			t.Fatalf("request %d created %s, request 0 created %s", i, results[i].ID, results[0].ID)
		}
	}
}

func TestCreatePartner_OneActivePairingPerDirection(t *testing.T) {
	s, _, ctx := setup(t)
	if _, _, err := s.CreatePartner(ctx, pairing("P", "S"), ""); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, _, err := s.CreatePartner(ctx, pairing("P", "S"), "second"); codeOf(err) != domain.CodePairingExists {
		t.Fatalf("second active pairing: got %v, want %s", err, domain.CodePairingExists)
	}
	inactive := pairing("P", "S")
	inactive.Active = false
	if _, _, err := s.CreatePartner(ctx, inactive, ""); err != nil {
		t.Fatalf("an inactive pairing for the same direction is allowed: %v", err)
	}
	if _, _, err := s.CreatePartner(ctx, pairing("S", "P"), ""); err != nil {
		t.Fatalf("the reverse direction is another pairing: %v", err)
	}
	// The other tenant has its own P to S.
	if _, _, err := s.CreatePartner(testkit.Ctx(testkit.TenantID(2)), pairing("P", "S"), ""); err != nil {
		t.Fatalf("tenant 2: %v", err)
	}
}

func TestCreatePartner_RejectsInvalidPairing(t *testing.T) {
	s, pool, ctx := setup(t)
	bad := pairing("P", "P")
	if _, _, err := s.CreatePartner(ctx, bad, ""); codeOf(err) != domain.CodeInvalid {
		t.Fatalf("same entity both sides: got %v", err)
	}
	noVendor := pairing("P", "S")
	noVendor.VendorAccount = ""
	if _, _, err := s.CreatePartner(ctx, noVendor, ""); codeOf(err) != domain.CodeInvalid {
		t.Fatalf("missing vendor account: got %v", err)
	}
	if n := len(outbox(t, pool, ctx)); n != 0 {
		t.Fatalf("a rejected pairing published %d events", n)
	}
}

func TestGetPartner_Missing(t *testing.T) {
	s, _, ctx := setup(t)
	if _, err := s.GetPartner(ctx, ids.New()); codeOf(err) != domain.CodePartnerNotFound {
		t.Fatalf("missing: got %v, want %s", err, domain.CodePartnerNotFound)
	}
}

func TestUpdatePartner_VersionedPolicyChange(t *testing.T) {
	s, pool, ctx := setup(t)
	rec, _, err := s.CreatePartner(ctx, pairing("P", "S"), "")
	if err != nil {
		t.Fatal(err)
	}
	table, days := domain.PriceTable, 5
	updated, err := s.UpdatePartner(ctx, rec.ID, 1, domain.PartnerPatch{Pricing: &table, ToleranceDateDays: &days})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 || updated.Pricing != domain.PriceTable || updated.Tolerance.DateDays != 5 || updated.UpdatedAt.Before(rec.CreatedAt) {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := s.UpdatePartner(ctx, rec.ID, 1, domain.PartnerPatch{ToleranceDateDays: &days}); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("stale version: got %v, want %s", err, domain.CodeVersionConflict)
	}
	same, err := s.UpdatePartner(ctx, rec.ID, 2, domain.PartnerPatch{ToleranceDateDays: &days})
	if err != nil || same.Version != 2 {
		t.Fatalf("no-op patch: version=%d err=%v", same.Version, err)
	}
	tooWide := 31
	if _, err := s.UpdatePartner(ctx, rec.ID, 2, domain.PartnerPatch{ToleranceDateDays: &tooWide}); codeOf(err) != domain.CodeInvalid {
		t.Fatalf("31 days: got %v", err)
	}
	if _, err := s.UpdatePartner(ctx, ids.New(), 1, domain.PartnerPatch{ToleranceDateDays: &days}); codeOf(err) != domain.CodePartnerNotFound {
		t.Fatalf("missing: got %v", err)
	}
	evts := outbox(t, pool, ctx)
	if len(evts) != 2 || evts[1]["_type"] != "dev.passage.interco.partner.updated.v1" || evts[1]["version"] != float64(2) {
		t.Fatalf("outbox = %v", evts)
	}
	changed, _ := evts[1]["changedFields"].([]any)
	if len(changed) != 2 || changed[0] != "pricing" || changed[1] != "tolerance" {
		t.Fatalf("changedFields = %v", evts[1]["changedFields"])
	}
}

// TestUpdatePartner_ActivationKeepsOneActive: a pairing can only be activated
// once the active one for its direction is deactivated.
func TestUpdatePartner_ActivationKeepsOneActive(t *testing.T) {
	s, _, ctx := setup(t)
	active, _, err := s.CreatePartner(ctx, pairing("P", "S"), "")
	if err != nil {
		t.Fatal(err)
	}
	draft := pairing("P", "S")
	draft.Active = false
	spare, _, err := s.CreatePartner(ctx, draft, "")
	if err != nil {
		t.Fatal(err)
	}
	on, off := true, false
	if _, err := s.UpdatePartner(ctx, spare.ID, 1, domain.PartnerPatch{Active: &on}); codeOf(err) != domain.CodePairingExists {
		t.Fatalf("second active: got %v, want %s", err, domain.CodePairingExists)
	}
	if _, err := s.UpdatePartner(ctx, active.ID, 1, domain.PartnerPatch{Active: &off}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got, err := s.UpdatePartner(ctx, spare.ID, 1, domain.PartnerPatch{Active: &on}); err != nil || !got.Active || got.Version != 2 {
		t.Fatalf("activate the spare: %+v, %v", got, err)
	}
}

func TestListPartners_FilterAndPages(t *testing.T) {
	s, _, ctx := setup(t)
	for _, pair := range [][2]string{{"P", "S1"}, {"S1", "P"}, {"P", "S2"}, {"S2", "S1"}} {
		if _, _, err := s.CreatePartner(ctx, pairing(pair[0], pair[1]), ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListPartners(ctx, store.PartnerFilter{}, "", 3)
	if err != nil || len(first.Items) != 3 || first.Next == "" {
		t.Fatalf("page 1: %d items next=%q err=%v", len(first.Items), first.Next, err)
	}
	second, err := s.ListPartners(ctx, store.PartnerFilter{}, first.Next, 3)
	if err != nil || len(second.Items) != 1 || second.Next != "" || second.Items[0].SellerEntity != "S2" {
		t.Fatalf("page 2: %+v err=%v", second, err)
	}
	withP, err := s.ListPartners(ctx, store.PartnerFilter{Entity: "P"}, "", 50)
	if err != nil || len(withP.Items) != 3 {
		t.Fatalf("entity P on either side: %d, %v", len(withP.Items), err)
	}
	off := false
	inactive, err := s.ListPartners(ctx, store.PartnerFilter{Active: &off}, "", 50)
	if err != nil || len(inactive.Items) != 0 {
		t.Fatalf("inactive: %d, %v", len(inactive.Items), err)
	}
	other, err := s.ListPartners(testkit.Ctx(testkit.TenantID(2)), store.PartnerFilter{}, "", 50)
	if err != nil || len(other.Items) != 0 {
		t.Fatalf("tenant 2 sees %d pairings, err=%v", len(other.Items), err)
	}
}

// TestStore_RowLevelSecurity runs as the non-bypass application role: every
// table enables and forces RLS, and one tenant never reads or changes
// another's pairings.
func TestStore_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []string{
		"grant usage on schema " + schema + " to passage_app",
		"grant select, insert, update, delete on all tables in schema " + schema + " to passage_app",
	} {
		if _, err := admin.Exec(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"intercompany_partner", "outbox", "idempotency_keys"} {
		var enabled, forced bool
		if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = $2::regnamespace`, table, schema).Scan(&enabled, &forced); err != nil {
			t.Fatal(table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v", table, enabled, forced)
		}
	}
	s := store.New(app)
	one, two := testkit.Ctx(testkit.TenantID(1)), testkit.Ctx(testkit.TenantID(2))
	rec, _, err := s.CreatePartner(one, pairing("P", "S"), "rls")
	if err != nil {
		t.Fatalf("create as the app role: %v", err)
	}
	if _, err := s.GetPartner(two, rec.ID); codeOf(err) != domain.CodePartnerNotFound {
		t.Fatalf("tenant 2 reads tenant 1's pairing: %v", err)
	}
	off := false
	if _, err := s.UpdatePartner(two, rec.ID, 1, domain.PartnerPatch{Active: &off}); codeOf(err) != domain.CodePartnerNotFound {
		t.Fatalf("tenant 2 changes tenant 1's pairing: %v", err)
	}
	if page, err := s.ListPartners(two, store.PartnerFilter{}, "", 50); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists %d pairings, err=%v", len(page.Items), err)
	}
	if _, replayed, err := s.CreatePartner(two, pairing("P", "S"), "rls"); err != nil || replayed {
		t.Fatalf("tenant 2 with tenant 1's key: replayed=%v err=%v", replayed, err)
	}
	if got, err := s.UpdatePartner(one, rec.ID, 1, domain.PartnerPatch{Active: &off}); err != nil || got.Active {
		t.Fatalf("tenant 1 deactivates its own pairing: %+v, %v", got, err)
	}
}
