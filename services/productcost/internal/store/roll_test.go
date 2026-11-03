package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
)

func purchased(id, price string) domain.Item {
	p := decimal.RequireFromString(price)
	return domain.Item{ID: id, Kind: domain.KindPurchased, Price: &p}
}

func rollInput(version Version, key string, items ...domain.Item) RollInput {
	cat := domain.Catalog{}
	for _, it := range items {
		cat[it.ID] = it
	}
	opts := domain.RollOptions{Mode: domain.ModeMultilevel, View: domain.ViewLegal}
	return RollInput{VersionID: version.ID, SiteID: siteA, Key: key, Fingerprint: key, Options: opts, Catalog: cat, Run: domain.MassRoll(cat, opts)}
}

func calculationStatuses(t *testing.T, admin *pgxpool.Pool, versionID string) map[string]int {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select status, count(*) from cost_calculation where costing_version_id = $1 group by status`, versionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			t.Fatal(err)
		}
		out[status] = n
	}
	return out
}

func TestRoll_ThenActivateThenPostWithNoHandInsertedCalculations(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	le := ids.New()
	if _, err := s.SetPostingAccounts(ctx, PostingAccounts{LegalEntityID: le, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	activate := func(v Version, key string, onHand map[inventory.Position]decimal.Decimal) (ActivationResult, error) {
		res, _, err := s.ActivateVersion(ctx, ActivationInput{VersionID: v.ID, Expected: 1, Key: key, Fingerprint: key, LedgerID: ids.Nil,
			PostingDate: "2026-01-02", Currency: "USD", OnHand: onHand, Ledger: ledger})
		return res, err
	}

	oldVer, _, err := s.CreateVersion(ctx, spec(le, "STD-OLD"), "k-old")
	if err != nil {
		t.Fatal(err)
	}
	if res, _, err := s.StoreRoll(ctx, rollInput(oldVer, "roll-old", purchased(itemA, "1.00"))); err != nil || res.StoredCount != 1 || res.ErrorCount != 0 {
		t.Fatalf("roll old: %+v %v", res, err)
	}
	if _, err := activate(oldVer, "act-old", map[inventory.Position]decimal.Decimal{}); err != nil {
		t.Fatalf("activating the first version, which has no stock to revalue: %v", err)
	}

	newVer, _, err := s.CreateVersion(ctx, spec(le, "STD-NEW"), "k-new")
	if err != nil {
		t.Fatal(err)
	}
	if res, _, err := s.StoreRoll(ctx, rollInput(newVer, "roll-new", purchased(itemA, "2.50"))); err != nil || res.StoredCount != 1 {
		t.Fatalf("roll new: %+v %v", res, err)
	}
	res, err := activate(newVer, "act-new", map[inventory.Position]decimal.Decimal{positionA: decimal.NewFromInt(10)})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	a := res.Activation
	if a.Total.String() != "15" || len(a.Lines) != 1 || a.Lines[0].OldStd.String() != "1" || a.Lines[0].NewStd.String() != "2.5" || a.JournalEntryID == "" {
		t.Fatalf("activation %+v, want 10 x (2.50 - 1.00) = 15 booked", a)
	}
	entries := ledger.Entries()
	if len(entries) != 1 || entries[0].ID != a.JournalEntryID || len(entries[0].Lines) != 2 {
		t.Fatalf("ledger entries %+v", entries)
	}
	if got := calculationStatuses(t, admin, newVer.ID); got["Pending"] != 1 {
		t.Fatalf("calculation statuses %v", got)
	}
}

func TestRoll_ARerollSupersedesPendingRowsAndAnErroredItemStoresNothing(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	v, _, err := s.CreateVersion(ctx, spec(ids.New(), "STD-REROLL"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(v, "roll-1", purchased(itemA, "1.00"), purchased(itemB, "3.00"))); err != nil {
		t.Fatal(err)
	}
	missingPrice := domain.Item{ID: itemA, Kind: domain.KindPurchased}
	res, _, err := s.StoreRoll(ctx, rollInput(v, "roll-2", missingPrice))
	if err != nil || res.ErrorCount != 1 || res.StoredCount != 0 || len(res.Errors) != 1 || res.Errors[0].ItemID != itemA {
		t.Fatalf("reroll with an item that cannot roll: %+v %v", res, err)
	}
	if got := calculationStatuses(t, admin, v.ID); got["Pending"] != 1 || got["Superseded"] != 1 || got["Error"] != 0 {
		t.Fatalf("statuses %v: item B keeps its Pending row; item A's row is Superseded, so no stale cost is revalued", got)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(v, "roll-3", purchased(itemA, "1.20"))); err != nil {
		t.Fatal(err)
	}
	if got := calculationStatuses(t, admin, v.ID); got["Pending"] != 2 {
		t.Fatalf("statuses %v", got)
	}
}

func TestRoll_IsIdempotentAndOnlyAnOpenVersionInItsSitesCanBeRolled(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	le := ids.New()
	v, _, err := s.CreateVersion(ctx, spec(le, "STD-GUARD"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	in := rollInput(v, "roll-1", purchased(itemA, "1.00"))
	if _, replayed, err := s.StoreRoll(ctx, in); err != nil || replayed {
		t.Fatalf("first: %v %v", replayed, err)
	}
	if res, replayed, err := s.StoreRoll(ctx, in); err != nil || !replayed || res.StoredCount != 1 {
		t.Fatalf("replay: %+v %v %v", res, replayed, err)
	}
	if got := calculationStatuses(t, admin, v.ID); got["Pending"] != 1 || len(got) != 1 {
		t.Fatalf("a replay wrote rows: %v", got)
	}
	reused := rollInput(v, "roll-1", purchased(itemA, "9.99"))
	reused.Fingerprint = "different"
	if _, _, err := s.StoreRoll(ctx, reused); codeOf(err) != CodeIdempotencyReuse {
		t.Fatalf("key reused for another request: %v", err)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(Version{ID: ids.New()}, "roll-x", purchased(itemA, "1.00"))); codeOf(err) != domain.CodeVersionNotFound {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := s.TransitionVersion(ctx, v.ID, 1, domain.ActionBlock); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(v, "roll-2", purchased(itemA, "1.00"))); codeOf(err) != domain.CodeVersionNotRollable {
		t.Fatalf("blocked version: %v", err)
	}

	scoped := spec(le, "STD-SCOPED")
	scoped.SiteIDs = []string{ids.New()}
	sv, _, err := s.CreateVersion(ctx, scoped, "k-scoped")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(sv, "roll-3", purchased(itemA, "1.00"))); codeOf(err) != domain.CodeVersionNotRollable {
		t.Fatalf("site outside the version's scope: %v", err)
	}
}
