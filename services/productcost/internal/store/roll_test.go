package store

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
)

func purchased(id, price string) domain.Item {
	p := decimal.RequireFromString(price)
	return domain.Item{ID: id, Kind: domain.KindPurchased, Price: &p}
}

func rollInput(version Version, key string, items ...domain.Item) RollInput {
	cat := domain.Catalog{}
	var roots []string
	sources := map[string]RollSource{}
	for _, it := range items {
		cat[it.ID] = it
		roots = append(roots, it.ID)
		sources[it.ID] = RollSource{BOMVersionID: ids.New(), RouteVersionID: ids.New()}
	}
	opts := domain.RollOptions{Mode: domain.ModeMultilevel, View: domain.ViewLegal}
	return RollInput{VersionID: version.ID, SiteID: siteA, Key: key, Fingerprint: key, Options: opts, Roots: roots, Sources: sources, Run: domain.MassRoll(cat, opts)}
}

func rollSpec(le, code string) domain.VersionSpec {
	v := spec(le, code)
	v.SiteIDs = []string{siteA}
	return v
}

func asUser(n int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: user, Duties: testkit.ScopeDuties("")})
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
	roller := asUser(1, "cost.roller")
	le := ids.New()
	if _, err := s.SetPostingAccounts(ctx, PostingAccounts{LegalEntityID: le, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	activate := func(v Version, key string, onHand map[inventory.Position]decimal.Decimal) (ActivationResult, error) {
		res, _, err := s.ActivateVersion(ctx, ActivationInput{VersionID: v.ID, Expected: 2, Key: key, Fingerprint: key, LedgerID: ids.Nil,
			PostingDate: "2026-01-02", Currency: "USD", OnHand: onHand, Ledger: ledger})
		return res, err
	}

	oldVer, _, err := s.CreateVersion(ctx, rollSpec(le, "STD-OLD"), "k-old")
	if err != nil {
		t.Fatal(err)
	}
	if res, _, err := s.StoreRoll(roller, rollInput(oldVer, "roll-old", purchased(itemA, "1.00"))); err != nil || res.StoredCount != 1 || res.ErrorCount != 0 {
		t.Fatalf("roll old: %+v %v", res, err)
	}
	if _, err := activate(oldVer, "act-old", map[inventory.Position]decimal.Decimal{}); err != nil {
		t.Fatalf("activating the first version, which has no stock to revalue: %v", err)
	}

	newVer, _, err := s.CreateVersion(ctx, rollSpec(le, "STD-NEW"), "k-new")
	if err != nil {
		t.Fatal(err)
	}
	if res, _, err := s.StoreRoll(roller, rollInput(newVer, "roll-new", purchased(itemA, "2.50"))); err != nil || res.StoredCount != 1 {
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
	if got := calculationStatuses(t, admin, newVer.ID); got["Active"] != 1 || len(got) != 1 {
		t.Fatalf("calculation statuses %v, want the rolled row Active after activation", got)
	}
}

func TestRoll_ARerollSupersedesPendingRowsAndAnErroredItemStoresNothing(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	v, _, err := s.CreateVersion(ctx, rollSpec(ids.New(), "STD-REROLL"), "k-v")
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
	v, _, err := s.CreateVersion(ctx, rollSpec(le, "STD-GUARD"), "k-v")
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
	if _, err := s.TransitionVersion(ctx, v.ID, 2, domain.ActionBlock); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(v, "roll-2", purchased(itemA, "1.00"))); codeOf(err) != domain.CodeVersionNotRollable {
		t.Fatalf("blocked version: %v", err)
	}
	if res, found, err := s.RollReplay(ctx, "roll-1", in.Fingerprint); err != nil || !found || res.StoredCount != 1 {
		t.Fatalf("replay lookup once the version left Open: %+v %v %v", res, found, err)
	}
	if _, _, err := s.RollReplay(ctx, "roll-1", "different"); codeOf(err) != CodeIdempotencyReuse {
		t.Fatalf("replay lookup with another request: %v", err)
	}
	if _, found, err := s.RollReplay(ctx, "roll-unused", "x"); err != nil || found {
		t.Fatalf("unused key: %v %v", found, err)
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

func TestRoll_BumpsTheVersionCounterSoAStaleActivationIsRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := as(1, "")
	roller := asUser(1, "cost.roller")
	v, _, err := s.CreateVersion(ctx, rollSpec(ids.New(), "STD-COUNTER"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := s.StoreRoll(roller, rollInput(v, "roll-1", purchased(itemA, "1.00")))
	if err != nil || res.Version != 2 {
		t.Fatalf("roll: %+v %v", res, err)
	}
	in := ActivationInput{VersionID: v.ID, Expected: 1, Key: "act", Fingerprint: "act", LedgerID: ids.Nil, PostingDate: "2026-01-02", Currency: "USD", Ledger: ledgerclient.NewFake()}
	if _, _, err := s.ActivateVersion(ctx, in); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("an activation that read the version before the roll: %v", err)
	}
}

func TestRoll_TheUserWhoRolledCannotActivateItsPendingPrices(t *testing.T) {
	s, _, _ := setup(t)
	roller := asUser(1, "cost.roller")
	if _, err := s.SetPostingAccounts(roller, PostingAccounts{LegalEntityID: ids.New(), InventoryAccount: "1", CostChangeAccount: "2"}); err != nil {
		t.Fatal(err)
	}
	v, _, err := s.CreateVersion(roller, rollSpec(ids.New(), "STD-FOUR-EYES"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(roller, rollInput(v, "roll-1", purchased(itemA, "1.00"))); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	in := ActivationInput{VersionID: v.ID, Expected: 2, Key: "act", Fingerprint: "act", LedgerID: ids.Nil, PostingDate: "2026-01-02", Currency: "USD", Ledger: ledger}
	if _, _, err := s.ActivateVersion(roller, in); codeOf(err) != domain.CodeRollerActivates {
		t.Fatalf("roller activating: %v", err)
	}
	if res, _, err := s.ActivateVersion(asUser(1, "cost.controller"), in); err != nil || res.Version.Status != domain.VersionActive {
		t.Fatalf("a second user activating: %+v %v", res, err)
	}
}

func TestRoll_OnePendingCalculationPerItemSiteAndView(t *testing.T) {
	s, admin, _ := setup(t)
	le := ids.New()
	v, _, err := s.CreateVersion(as(1, ""), spec(le, "STD-DUP"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, admin, le, v.ID, "Pending", "1.00")
	_, err = admin.Exec(context.Background(), `insert into cost_calculation
		(tenant_id, id, legal_entity_id, costing_version_id, item_id, site_id, calc_quantity, explosion_mode, unit_cost, status)
		values ($1, $2, $3, $4, $5, $6, 1, 'Multilevel', 1, 'Pending')`, testkit.TenantID(1), ids.New(), le, v.ID, itemA, siteA)
	if pgCode(err) != uniqueViolation {
		t.Fatalf("a second Pending row for the same item, site and view: %v", err)
	}
}

func TestRoll_AVersionWithoutASiteScopeCannotBeRolled(t *testing.T) {
	s, _, _ := setup(t)
	ctx := as(1, "")
	v, _, err := s.CreateVersion(ctx, spec(ids.New(), "STD-NO-SITES"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(ctx, rollInput(v, "roll-1", purchased(itemA, "1.00"))); codeOf(err) != domain.CodeVersionNotRollable {
		t.Fatalf("a version with no site scope: %v", err)
	}
}

func TestRoll_FourEyesHoldsWhileAnAmbiguousActivationAttemptIsPending(t *testing.T) {
	s, admin, _ := setup(t)
	a, b := asUser(1, "cost.roller"), asUser(1, "cost.controller")
	le := ids.New()
	if _, err := s.SetPostingAccounts(a, PostingAccounts{LegalEntityID: le, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}); err != nil {
		t.Fatal(err)
	}
	oldVer, _, err := s.CreateVersion(a, rollSpec(le, "STD-OLD"), "k-old")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(a, rollInput(oldVer, "roll-old", purchased(itemA, "1.00"))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ActivateVersion(b, ActivationInput{VersionID: oldVer.ID, Expected: 2, Key: "act-old", Fingerprint: "act-old", LedgerID: ids.Nil,
		PostingDate: "2026-01-02", Currency: "USD", OnHand: map[inventory.Position]decimal.Decimal{}, Ledger: ledgerclient.NewFake()}); err != nil {
		t.Fatal(err)
	}
	v, _, err := s.CreateVersion(a, rollSpec(le, "STD-NEW"), "k-new")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(a, rollInput(v, "roll-1", purchased(itemA, "2.50"))); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	activate := func(expected int) ActivationInput {
		return ActivationInput{VersionID: v.ID, Expected: expected, Key: "act", Fingerprint: "act", LedgerID: ids.Nil, PostingDate: "2026-01-02", Currency: "USD",
			OnHand: map[inventory.Position]decimal.Decimal{positionA: decimal.NewFromInt(10)}, Ledger: ledger}
	}
	if _, _, err := s.ActivateVersion(b, activate(2)); err == nil {
		t.Fatal("the ambiguous post should have failed")
	}
	if n, ambiguous := pendingAttempts(t, admin); n != 1 || !ambiguous {
		t.Fatalf("attempts %d ambiguous %v", n, ambiguous)
	}
	if _, _, err := s.StoreRoll(a, rollInput(v, "roll-2", purchased(itemA, "99.00"))); codeOf(err) != domain.CodeActivationPending {
		t.Fatalf("a roll while an activation attempt is pending: %v", err)
	}
	if _, _, err := s.ActivateVersion(a, activate(2)); codeOf(err) != domain.CodeRollerActivates {
		t.Fatalf("the roller resuming the attempt: %v", err)
	}
	if got := len(ledger.Entries()); got != 0 {
		t.Fatalf("ledger entries %d before the controller retries", got)
	}
	ledger.FailPosts(nil)
	res, _, err := s.ActivateVersion(b, activate(2))
	if err != nil || res.Activation.Total.String() != "15" {
		t.Fatalf("the controller retrying: %+v %v", res, err)
	}
	if got := calculationStatuses(t, admin, v.ID); got["Active"] != 1 || len(got) != 1 {
		t.Fatalf("calculation statuses %v", got)
	}
}

func TestActivation_AResumedAttemptActivatesOnlyTheCalculationsItPlanned(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	ctx := as(1, "")
	if _, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act")); err == nil {
		t.Fatal("the ambiguous post should have failed")
	}
	if _, err := f.admin.Exec(context.Background(), `update cost_calculation set status = 'Superseded' where costing_version_id = $1`, f.newVer.ID); err != nil {
		t.Fatal(err)
	}
	ledger.FailPosts(nil)
	if _, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act")); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("a planned calculation no longer Pending: %v", err)
	}
	if got := len(ledger.Entries()); got != 0 {
		t.Fatalf("ledger entries %d: nothing is posted for a stale plan", got)
	}
}

func TestActivation_APrincipalWithoutAUserCannotActivateRolledCalculations(t *testing.T) {
	s, _, _ := setup(t)
	v, _, err := s.CreateVersion(as(1, ""), rollSpec(ids.New(), "STD-NO-USER"), "k-v")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.StoreRoll(asUser(1, "cost.roller"), rollInput(v, "roll-1", purchased(itemA, "1.00"))); err != nil {
		t.Fatal(err)
	}
	in := ActivationInput{VersionID: v.ID, Expected: 2, Key: "act", Fingerprint: "act", LedgerID: ids.Nil, PostingDate: "2026-01-02", Currency: "USD", Ledger: ledgerclient.NewFake()}
	if _, _, err := s.ActivateVersion(asUser(1, ""), in); codeOf(err) != domain.CodeRollerActivates {
		t.Fatalf("an activation with no user: %v", err)
	}
}
