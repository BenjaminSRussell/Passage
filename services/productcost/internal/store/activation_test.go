package store

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/inventory"
)

const (
	itemA = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6b01"
	siteA = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6b02"
	itemB = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6b03"
)

var positionA = inventory.Position{ItemID: itemA, SiteID: siteA}

type activationFixture struct {
	s      *Postgres
	admin  *pgxpool.Pool
	le     string
	oldVer Version
	newVer Version
	pa     PostingAccounts
	rls    bool
}

func seedCalculation(t *testing.T, admin *pgxpool.Pool, le, versionID, status, unitCost string) {
	t.Helper()
	seedCalculationFor(t, admin, le, versionID, itemA, status, unitCost)
}

func seedCalculationFor(t *testing.T, admin *pgxpool.Pool, le, versionID, itemID, status, unitCost string) {
	t.Helper()
	_, err := admin.Exec(context.Background(), `insert into cost_calculation
		(tenant_id, id, legal_entity_id, costing_version_id, item_id, site_id, calc_quantity, explosion_mode, bom_version_id, route_version_id, unit_cost, status)
		values ($1, $2, $3, $4, $5, $6, 1, 'Multilevel', $7, $7, $8, $9)`,
		testkit.TenantID(1), ids.New(), le, versionID, itemID, siteA, ids.New(), unitCost, status)
	if err != nil {
		t.Fatal(err)
	}
}

func newActivationFixture(t *testing.T, withAccounts bool) activationFixture {
	t.Helper()
	s, admin, rls := setup(t)
	ctx := as(1, "")
	f := activationFixture{s: s, admin: admin, le: ids.New(), rls: rls}
	f.pa = PostingAccounts{LegalEntityID: f.le, InventoryAccount: "1400-INV", CostChangeAccount: "5900-PPV"}
	if withAccounts {
		if _, err := s.SetPostingAccounts(ctx, f.pa); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if f.oldVer, _, err = s.CreateVersion(ctx, spec(f.le, "STD-OLD"), "k-old"); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(context.Background(), `update costing_version set status = 'Active', activated_at = now() - interval '1 day' where id = $1`, f.oldVer.ID); err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, admin, f.le, f.oldVer.ID, "Active", "1.00")
	if f.newVer, _, err = s.CreateVersion(ctx, spec(f.le, "STD-NEW"), "k-new"); err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, admin, f.le, f.newVer.ID, "Pending", "2.50")
	return f
}

func (f activationFixture) activate(ledger ledgerclient.Ledger, key string) ActivationInput {
	return ActivationInput{
		VersionID: f.newVer.ID, Expected: 1, Key: key, Fingerprint: f.newVer.ID, LedgerID: ids.Nil, PostingDate: "2026-01-02", Currency: "USD",
		OnHand: map[inventory.Position]decimal.Decimal{positionA: decimal.NewFromInt(10)}, Ledger: ledger,
	}
}

func reversal(versionID, activationID string, expected int, key string, ledger ledgerclient.Ledger) ActivationInput {
	return ActivationInput{VersionID: versionID, Expected: expected, Key: key, Fingerprint: key, ReversesID: activationID, PostingDate: "2026-01-03", Ledger: ledger}
}

func activationRows(t *testing.T, admin *pgxpool.Pool, versionID string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from standard_cost_activation where costing_version_id = $1`, versionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostingAccounts_AreStoredPerLegalEntityIsolatedAndAnnounced(t *testing.T) {
	s, admin, rls := setup(t)
	t1 := as(1, "")
	leA, leB := ids.New(), ids.New()
	if _, err := s.GetPostingAccounts(t1, leA); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("none configured: %v", err)
	}
	if _, err := s.SetPostingAccounts(t1, PostingAccounts{LegalEntityID: leA, InventoryAccount: "1400", CostChangeAccount: "5900"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetPostingAccounts(t1, PostingAccounts{LegalEntityID: leA, InventoryAccount: "1410", CostChangeAccount: "5910"})
	if err != nil || got.UpdatedBy != "cost.accountant" {
		t.Fatalf("update: %+v %v", got, err)
	}
	if pa, err := s.GetPostingAccounts(t1, leA); err != nil || pa.InventoryAccount != "1410" || pa.CostChangeAccount != "5910" {
		t.Fatalf("read back %+v %v", pa, err)
	}
	if types := outboxTypes(t, admin, leA); len(types) != 2 || types[0] != costevents.PostingAccountsChanged || types[1] != costevents.PostingAccountsChanged {
		t.Fatalf("each change is announced, got %v", types)
	}
	if _, err := s.GetPostingAccounts(t1, leB); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("another legal entity inherits accounts: %v", err)
	}
	if _, err := s.GetPostingAccounts(as(2, ""), leA); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("another tenant reads the accounts: %v", err)
	}
	if !rls {
		t.Skipf("%s is needed to run as the application role", testkit.EnvAppURL)
	}
	if _, err := s.GetPostingAccounts(as(1, leB), leA); codeOf(err) != domain.CodePostingAccounts {
		t.Fatalf("legal-entity scope B reads A: %v", err)
	}
	if _, err := s.SetPostingAccounts(as(1, leB), PostingAccounts{LegalEntityID: leA, InventoryAccount: "X", CostChangeAccount: "Y"}); codeOf(err) != domain.CodeLegalEntityScope || statusOf(err) != http.StatusForbidden {
		t.Fatalf("legal-entity scope B writes A: %v", err)
	}
}

func TestActivation_PostsTheStoredStandardMathOnceAndReplaysByIdempotencyKey(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	first, replayed, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	if err != nil || replayed {
		t.Fatalf("activate: replayed=%v err=%v", replayed, err)
	}
	act := first.Activation
	if first.Version.Status != domain.VersionActive || first.Version.Version != 2 || act.JournalEntryID == "" || act.Total.String() != "15" {
		t.Fatalf("result %+v", first)
	}
	if len(act.Lines) != 1 || act.Lines[0].OldStd.String() != "1" || act.Lines[0].NewStd.String() != "2.5" || act.Lines[0].Qty.String() != "10" {
		t.Fatalf("lines %+v, want 10 x (2.50 - 1.00) from the stored standards", act.Lines)
	}
	again, replayed, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	if err != nil || !replayed || again.Activation.ID != act.ID {
		t.Fatalf("replay %+v replayed=%v err=%v", again, replayed, err)
	}
	entries := ledger.Entries()
	if len(entries) != 1 || entries[0].ID != act.JournalEntryID || entries[0].DocumentID != act.ID {
		t.Fatalf("ledger entries %+v", entries)
	}
	booked := map[string]string{}
	for _, l := range entries[0].Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		booked[l.Account] = side + l.Amount.String()
	}
	if len(booked) != 2 || booked["1400-INV"] != "D15" || booked["5900-PPV"] != "C15" {
		t.Fatalf("journal %v, want Dr 1400-INV 15 / Cr 5900-PPV 15", booked)
	}
	stored, err := f.s.GetActivation(ctx, act.ID)
	if err != nil || stored.InventoryAccount != "1400-INV" || stored.CostChangeAccount != "5900-PPV" || len(stored.JournalLines) != 2 {
		t.Fatalf("stored activation %+v err=%v", stored, err)
	}
	if n := activationRows(t, f.admin, f.newVer.ID); n != 1 {
		t.Fatalf("activation rows %d", n)
	}
	if got := outboxTypes(t, f.admin, act.ID); len(got) != 1 || got[0] != costevents.StandardCostActivated {
		t.Fatalf("outbox %v", got)
	}
	if _, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-other")); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("second activation under a new key with a stale counter: %v", err)
	}
	if len(ledger.Entries()) != 1 {
		t.Fatal("a refused activation posted")
	}
}

func TestActivation_PreviewReadsTheStoredStandardsAndWritesNothing(t *testing.T) {
	f := newActivationFixture(t, false)
	preview, err := f.s.PreviewActivation(as(1, ""), f.activate(nil, ""))
	if err != nil || preview.Total.String() != "15" || len(preview.Journal) != 2 {
		t.Fatalf("preview %+v err=%v", preview, err)
	}
	if n := activationRows(t, f.admin, f.newVer.ID); n != 0 {
		t.Fatalf("a preview stored %d activations", n)
	}
}

func TestActivation_FailsClosedWithoutStoredStandardsOrAccounts(t *testing.T) {
	t.Run("no new standard", func(t *testing.T) {
		f := newActivationFixture(t, true)
		if _, err := f.admin.Exec(context.Background(), `delete from cost_calculation where costing_version_id = $1`, f.newVer.ID); err != nil {
			t.Fatal(err)
		}
		ledger := ledgerclient.NewFake()
		if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-1")); codeOf(err) != domain.CodeNoStoredStandard {
			t.Fatalf("%v", err)
		}
		if len(ledger.Entries()) != 0 {
			t.Fatal("posted without a stored standard")
		}
	})
	t.Run("no earlier standard", func(t *testing.T) {
		f := newActivationFixture(t, true)
		if _, err := f.admin.Exec(context.Background(), `update costing_version set status = 'Closed' where id = $1`, f.oldVer.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledgerclient.NewFake(), "k-1")); codeOf(err) != domain.CodeNoStoredStandard {
			t.Fatalf("%v", err)
		}
	})
	t.Run("no posting accounts", func(t *testing.T) {
		f := newActivationFixture(t, false)
		ledger := ledgerclient.NewFake()
		if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-1")); codeOf(err) != domain.CodePostingAccounts {
			t.Fatalf("%v", err)
		}
		got, err := f.s.GetVersion(as(1, ""), f.newVer.ID)
		if err != nil || got.Status != domain.VersionOpen || len(ledger.Entries()) != 0 || activationRows(t, f.admin, f.newVer.ID) != 0 {
			t.Fatalf("state after a refusal: %+v %v entries=%d", got, err, len(ledger.Entries()))
		}
	})
}

func TestActivation_RefusedJournalLeavesTheVersionUntouchedAndRetryable(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	refusal := &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed}
	ledger.FailPosts(refusal)
	_, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	var refused *LedgerRefusal
	if !errors.As(err, &refused) || !errors.Is(err, refusal) {
		t.Fatalf("refused journal: %v", err)
	}
	got, err := f.s.GetVersion(ctx, f.newVer.ID)
	if err != nil || got.Status != domain.VersionOpen || got.Version != 1 || got.ActivatedAt != nil {
		t.Fatalf("version after a refused journal: %+v %v", got, err)
	}
	if n := activationRows(t, f.admin, f.newVer.ID); n != 0 {
		t.Fatalf("activation rows %d", n)
	}
	ledger.FailPosts(nil)
	res, replayed, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	if err != nil || replayed || res.Version.Status != domain.VersionActive || len(ledger.Entries()) != 1 {
		t.Fatalf("retry with the same key: %+v replayed=%v err=%v entries=%d", res, replayed, err, len(ledger.Entries()))
	}
}

type postThenBreakCommit struct {
	ledgerclient.Ledger
	admin *pgxpool.Pool
	t     *testing.T
}

func (p postThenBreakCommit) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	id, err := p.Ledger.Post(ctx, e)
	if _, execErr := p.admin.Exec(context.Background(), `alter table standard_cost_activation add constraint break_commit check (false) not valid`); execErr != nil {
		p.t.Fatal(execErr)
	}
	return id, err
}

func pendingAttempts(t *testing.T, admin *pgxpool.Pool) (n int, ambiguous bool) {
	t.Helper()
	err := admin.QueryRow(context.Background(), `select count(*), coalesce(bool_or(attempted_ambiguous), false) from standard_cost_activation_attempt`).Scan(&n, &ambiguous)
	if err != nil {
		t.Fatal(err)
	}
	return n, ambiguous
}

func TestActivation_RetryAfterTheLedgerAcceptedButTheCommitFailedReplaysTheSameEntryWhateverTheDefaultsBecame(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(postThenBreakCommit{Ledger: ledger, admin: f.admin, t: t}, "k-act")); err == nil {
		t.Fatal("the commit should have failed")
	}
	got, err := f.s.GetVersion(as(1, ""), f.newVer.ID)
	if err != nil || got.Status != domain.VersionOpen || activationRows(t, f.admin, f.newVer.ID) != 0 {
		t.Fatalf("a failed commit left state behind: %+v %v", got, err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 1 || len(ledger.Entries()) != 1 {
		t.Fatalf("pending attempts %d, ledger entries %d; want the attempt kept and the entry accepted", n, len(ledger.Entries()))
	}
	if _, err := f.admin.Exec(context.Background(), `alter table standard_cost_activation drop constraint break_commit`); err != nil {
		t.Fatal(err)
	}
	retry := f.activate(ledger, "k-act")
	retry.PostingDate, retry.Currency, retry.LedgerID = "2026-01-03", "EUR", ids.New()
	retry.OnHand = map[inventory.Position]decimal.Decimal{positionA: decimal.NewFromInt(99)}
	res, replayed, err := f.s.ActivateVersion(as(1, ""), retry)
	if err != nil || replayed || len(ledger.Entries()) != 1 || res.Activation.JournalEntryID != ledger.Entries()[0].ID {
		t.Fatalf("retry: %+v replayed=%v err=%v entries=%d", res, replayed, err, len(ledger.Entries()))
	}
	if a := res.Activation; a.PostingDate != "2026-01-02" || a.Currency != "USD" || a.Total.String() != "15" {
		t.Fatalf("the retry must use the pending attempt verbatim, got %+v", a)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 0 || res.Version.Status != domain.VersionActive {
		t.Fatalf("pending attempts %d after the retry", n)
	}
}

func TestActivation_ADefiniteRefusalClearsTheAttemptAndACorrectedRetryPostsAfresh(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err == nil {
		t.Fatal("the refusal should fail the call")
	}
	if n, _ := pendingAttempts(t, f.admin); n != 0 {
		t.Fatalf("a definite refusal left %d attempts", n)
	}
	ledger.FailPosts(nil)
	retry := f.activate(ledger, "k-act")
	retry.PostingDate = "2026-02-01"
	res, _, err := f.s.ActivateVersion(as(1, ""), retry)
	if err != nil || res.Activation.PostingDate != "2026-02-01" || len(ledger.Entries()) != 1 {
		t.Fatalf("corrected retry: %+v %v entries=%d", res.Activation, err, len(ledger.Entries()))
	}
}

func TestActivation_AnAmbiguousFailureKeepsTheAttemptAndALaterRefusalDoesNotClearIt(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusBadGateway} {
		ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: status})
		if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err == nil {
			t.Fatalf("status %d should fail the call", status)
		}
		if n, amb := pendingAttempts(t, f.admin); n != 1 || !amb {
			t.Fatalf("status %d: attempts %d ambiguous %v, want the attempt kept and flagged", status, n, amb)
		}
	}
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err == nil {
		t.Fatal("the refusal should fail the call")
	}
	if n, _ := pendingAttempts(t, f.admin); n != 1 {
		t.Fatalf("a refusal after an ambiguous attempt cleared it: %d", n)
	}
	ledger.FailPosts(nil)
	if res, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err != nil || res.Version.Status != domain.VersionActive || len(ledger.Entries()) != 1 {
		t.Fatalf("recovery: %+v %v", res, err)
	}
}

func TestActivation_ReversalCommitFailureAlsoReplaysTheSameLedgerEntry(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	act, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act"))
	if err != nil {
		t.Fatal(err)
	}
	broken := reversal(f.newVer.ID, act.Activation.ID, act.Version.Version, "k-rev", reverseThenBreak{Ledger: ledger, admin: f.admin, t: t})
	if _, _, err := f.s.ActivateVersion(as(1, ""), broken); err == nil {
		t.Fatal("the commit should have failed")
	}
	if _, err := f.admin.Exec(context.Background(), `alter table standard_cost_activation drop constraint break_commit`); err != nil {
		t.Fatal(err)
	}
	retry := reversal(f.newVer.ID, act.Activation.ID, act.Version.Version, "k-rev", ledger)
	retry.PostingDate = "2026-02-02"
	res, _, err := f.s.ActivateVersion(as(1, ""), retry)
	if err != nil || res.Activation.PostingDate != "2026-01-03" || len(ledger.Entries()) != 2 {
		t.Fatalf("reversal retry: %+v %v entries=%d", res.Activation, err, len(ledger.Entries()))
	}
}

type reverseThenBreak struct {
	ledgerclient.Ledger
	admin *pgxpool.Pool
	t     *testing.T
}

func (p reverseThenBreak) Reverse(ctx context.Context, r ledgerclient.Reversal) (string, error) {
	id, err := p.Ledger.Reverse(ctx, r)
	if _, execErr := p.admin.Exec(context.Background(), `alter table standard_cost_activation add constraint break_commit check (false) not valid`); execErr != nil {
		p.t.Fatal(execErr)
	}
	return id, err
}

func TestActivation_ARevertedStandardIsNotTheOldStandardOfTheNextActivation(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	v2, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-v2"))
	if err != nil || v2.Activation.Total.String() != "15" {
		t.Fatalf("v2: %+v %v", v2.Activation, err)
	}
	if _, _, err := f.s.ActivateVersion(ctx, reversal(f.newVer.ID, v2.Activation.ID, v2.Version.Version, "k-rev", ledger)); err != nil {
		t.Fatal(err)
	}
	v3, _, err := f.s.CreateVersion(ctx, spec(f.le, "STD-V3"), "k-v3")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, f.admin, f.le, v3.ID, "Pending", "4.00")
	in := f.activate(ledger, "k-act-v3")
	in.VersionID, in.Fingerprint = v3.ID, v3.ID
	res, _, err := f.s.ActivateVersion(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if l := res.Activation.Lines[0]; res.Activation.Total.String() != "30" || l.OldStd.String() != "1" || l.NewStd.String() != "4" {
		t.Fatalf("v3 posted %s with line %+v, want 10 x (4.00 - 1.00): the stock is back at 1.00 after the reversal", res.Activation.Total, l)
	}
	v4, _, err := f.s.CreateVersion(ctx, spec(f.le, "STD-V4"), "k-v4")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, f.admin, f.le, v4.ID, "Pending", "5.00")
	in = f.activate(ledger, "k-act-v4")
	in.VersionID, in.Fingerprint = v4.ID, v4.ID
	next, _, err := f.s.ActivateVersion(ctx, in)
	if err != nil || next.Activation.Lines[0].OldStd.String() != "4" || next.Activation.Total.String() != "10" {
		t.Fatalf("v4 must start from the standard v3 booked: %+v %v", next.Activation, err)
	}
}

func TestActivation_APlannedVersionOnlyMovesTheStatusAndAStandardOneWithPositionsIsLeftToTheRevaluation(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	if _, handled, err := f.s.ActivateStatusOnly(ctx, f.newVer.ID, 1); err != nil || handled {
		t.Fatalf("a Standard version with positions: handled=%v err=%v", handled, err)
	}
	if got, _ := f.s.GetVersion(ctx, f.newVer.ID); got.Status != domain.VersionOpen || got.Version != 1 {
		t.Fatalf("an unhandled version moved: %+v", got)
	}
	if _, err := f.admin.Exec(context.Background(), `update costing_version set type = 'Planned' where id = $1`, f.newVer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PreviewActivation(ctx, f.activate(nil, "")); err != nil {
		t.Fatalf("a preview of a planned version stays available: %v", err)
	}
	if _, _, err := f.s.ActivateStatusOnly(ctx, f.newVer.ID, 7); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("stale version counter: %v", err)
	}
	v, handled, err := f.s.ActivateStatusOnly(ctx, f.newVer.ID, 1)
	if err != nil || !handled || v.Status != domain.VersionActive || v.Version != 2 || v.ActivatedAt == nil {
		t.Fatalf("planned activation: %+v handled=%v err=%v", v, handled, err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 0 || activationRows(t, f.admin, f.newVer.ID) != 0 {
		t.Fatalf("a status-only activation left attempts %d or activation rows", n)
	}
	if got := outboxTypes(t, f.admin, f.newVer.ID); len(got) != 2 || got[1] != costevents.VersionTransitioned {
		t.Fatalf("outbox %v", got)
	}
}

func TestActivation_ASimulationVersionStaysRefusedOnTheStatusOnlyPath(t *testing.T) {
	f := newActivationFixture(t, true)
	if _, err := f.admin.Exec(context.Background(), `update costing_version set type = 'Simulation' where id = $1`, f.newVer.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ActivateStatusOnly(as(1, ""), f.newVer.ID, 1); codeOf(err) != domain.CodeSimulationActivation {
		t.Fatalf("simulation: %v", err)
	}
}

func TestActivation_AFirstTimePositionWithoutStockIsBookedAtItsOwnStandard(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	seedCalculationFor(t, f.admin, f.le, f.newVer.ID, itemB, "Pending", "3.00")
	ledger := ledgerclient.NewFake()
	res, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-first"))
	if err != nil || res.Activation.ItemCount != 2 || res.Activation.Total.String() != "15" {
		t.Fatalf("absent on-hand for a position without a basis: %+v %v", res.Activation, err)
	}
	for _, l := range res.Activation.Lines {
		if l.ItemID == itemB && (!l.Qty.IsZero() || !l.OldStd.Equal(l.NewStd) || !l.Amount.IsZero() || l.NewStd.String() != "3") {
			t.Errorf("first-time line %+v, want old = new = 3 and amount 0", l)
		}
	}
	if _, err := f.admin.Exec(context.Background(), `delete from cost_calculation where costing_version_id = $1`, f.oldVer.ID); err != nil {
		t.Fatal(err)
	}
	next, _, err := f.s.CreateVersion(ctx, spec(f.le, "STD-ZERO"), "k-next")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculationFor(t, f.admin, f.le, next.ID, "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6b09", "Pending", "9.00")
	z := f.activate(ledgerclient.NewFake(), "k-zero-qty")
	z.VersionID, z.Fingerprint = next.ID, next.ID
	z.OnHand = map[inventory.Position]decimal.Decimal{{ItemID: "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6b09", SiteID: siteA}: decimal.Zero}
	if got, _, err := f.s.ActivateVersion(ctx, z); err != nil || !got.Activation.Total.IsZero() || got.Activation.JournalEntryID != "" {
		t.Fatalf("zero on-hand: %+v %v", got.Activation, err)
	}
}

func TestActivation_StockWithoutABasisIsStillRefused(t *testing.T) {
	f := newActivationFixture(t, true)
	if _, err := f.admin.Exec(context.Background(), `delete from cost_calculation where costing_version_id = $1`, f.oldVer.ID); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-stock")); codeOf(err) != domain.CodeNoStoredStandard || len(ledger.Entries()) != 0 {
		t.Fatalf("10 on hand and no earlier standard: %v", err)
	}
}

func TestActivation_ReversingAnActivationBehindALaterOneIsRefusedUntilTheLaterIsReversed(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	v2, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-v2"))
	if err != nil {
		t.Fatal(err)
	}
	v3ver, _, err := f.s.CreateVersion(ctx, spec(f.le, "STD-V3"), "k-v3")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, f.admin, f.le, v3ver.ID, "Pending", "4.00")
	in := f.activate(ledger, "k-act-v3")
	in.VersionID, in.Fingerprint = v3ver.ID, v3ver.ID
	v3, _, err := f.s.ActivateVersion(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ActivateVersion(ctx, reversal(f.newVer.ID, v2.Activation.ID, v2.Version.Version, "k-rev-v2", ledger))
	if codeOf(err) != domain.CodeReversalTarget || statusOf(err) == http.StatusConflict {
		t.Fatalf("out-of-order reversal: %v", err)
	}
	if len(ledger.Entries()) != 2 {
		t.Fatalf("the refused reversal posted: %d entries", len(ledger.Entries()))
	}
	if _, _, err := f.s.ActivateVersion(ctx, reversal(v3ver.ID, v3.Activation.ID, v3.Version.Version, "k-rev-v3", ledger)); err != nil {
		t.Fatalf("reversing the latest: %v", err)
	}
	if _, _, err := f.s.ActivateVersion(ctx, reversal(f.newVer.ID, v2.Activation.ID, v2.Version.Version, "k-rev-v2", ledger)); err != nil {
		t.Fatalf("reversing the earlier once the later is reversed: %v", err)
	}
}

func TestActivation_AnotherVersionsPendingAttemptInTheSameLegalEntityIs409(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusBadGateway})
	if _, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-v2")); err == nil {
		t.Fatal("the post should have failed")
	}
	v3ver, _, err := f.s.CreateVersion(ctx, spec(f.le, "STD-V3"), "k-v3")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, f.admin, f.le, v3ver.ID, "Pending", "4.00")
	in := f.activate(ledger, "k-act-v3")
	in.VersionID, in.Fingerprint = v3ver.ID, v3ver.ID
	if _, _, err := f.s.ActivateVersion(ctx, in); codeOf(err) != domain.CodeActivationPending || statusOf(err) != http.StatusConflict {
		t.Fatalf("activation beside a pending attempt: %v", err)
	}
	if n, _ := pendingAttempts(t, f.admin); n != 1 {
		t.Fatalf("attempts %d, the refused activation must not add one", n)
	}
	ledger.FailPosts(nil)
	if _, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-v2")); err != nil {
		t.Fatalf("the pending version recovers: %v", err)
	}
	res, _, err := f.s.ActivateVersion(ctx, in)
	if err != nil || res.Activation.Lines[0].OldStd.String() != "2.5" {
		t.Fatalf("after recovery v3 starts from v2's booked standard: %+v %v", res.Activation, err)
	}
}

func TestActivation_APositionInventoryDoesNotReportIsRefused(t *testing.T) {
	f := newActivationFixture(t, true)
	ledger := ledgerclient.NewFake()
	missing := f.activate(ledger, "k-missing")
	missing.OnHand = map[inventory.Position]decimal.Decimal{}
	if _, _, err := f.s.ActivateVersion(as(1, ""), missing); codeOf(err) != domain.CodeNoOnHand {
		t.Fatalf("position missing from the inventory answer: %v", err)
	}
	if len(ledger.Entries()) != 0 {
		t.Fatal("posted for a refused activation")
	}
}

func TestActivation_PositionsComeFromTheStoredCalculations(t *testing.T) {
	f := newActivationFixture(t, true)
	got, err := f.s.ActivationPositions(as(1, ""), f.newVer.ID)
	if err != nil || len(got) != 1 || got[0] != positionA {
		t.Fatalf("%v %v", got, err)
	}
	if empty, err := f.s.ActivationPositions(as(1, ""), ids.New()); err != nil || len(empty) != 0 {
		t.Fatalf("%v %v", empty, err)
	}
}

func TestActivation_ReversalReversesTheOriginalEntryWhateverTheAccountsAreNow(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	act, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetPostingAccounts(ctx, PostingAccounts{LegalEntityID: f.le, InventoryAccount: "1500-NEW", CostChangeAccount: "5950-NEW"}); err != nil {
		t.Fatal(err)
	}
	rev, replayed, err := f.s.ActivateVersion(ctx, reversal(f.newVer.ID, act.Activation.ID, act.Version.Version, "k-rev", ledger))
	if err != nil || replayed {
		t.Fatalf("reversal: replayed=%v err=%v", replayed, err)
	}
	r := rev.Activation
	if r.ReversesActivationID == nil || *r.ReversesActivationID != act.Activation.ID || r.Total.String() != "-15" || r.JournalEntryID == "" ||
		r.InventoryAccount != "1400-INV" || r.CostChangeAccount != "5900-PPV" || r.LedgerID != act.Activation.LedgerID || r.Currency != "USD" {
		t.Fatalf("reversal row %+v", r)
	}
	if rev.Version.Status != domain.VersionActive || rev.Version.Version != 3 {
		t.Fatalf("version after the reversal %+v", rev.Version)
	}
	entries := ledger.Entries()
	if len(entries) != 2 || entries[1].ReversalOf != act.Activation.JournalEntryID || entries[1].ID != r.JournalEntryID {
		t.Fatalf("ledger entries %+v", entries)
	}
	for i, l := range entries[1].Lines {
		orig := entries[0].Lines[i]
		if l.Account != orig.Account || l.Credit == orig.Credit || !l.Amount.Equal(orig.Amount) {
			t.Errorf("reversed line %+v vs original %+v: want the same account and amount on the other side", l, orig)
		}
		if l.Account == "1500-NEW" || l.Account == "5950-NEW" {
			t.Errorf("the reversal used today's account %s", l.Account)
		}
	}
	if len(r.JournalLines) != 2 || r.JournalLines[0].Credit == act.Activation.JournalLines[0].Credit {
		t.Fatalf("stored reversal lines %+v", r.JournalLines)
	}
	if got := outboxTypes(t, f.admin, r.ID); len(got) != 1 || got[0] != costevents.StandardCostActivated {
		t.Fatalf("outbox %v", got)
	}
}

func TestActivation_ASecondReversalIs409AndPostsNothing(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	act, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	if err != nil {
		t.Fatal(err)
	}
	rev, _, err := f.s.ActivateVersion(ctx, reversal(f.newVer.ID, act.Activation.ID, act.Version.Version, "k-rev-1", ledger))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.s.ActivateVersion(ctx, reversal(f.newVer.ID, act.Activation.ID, rev.Version.Version, "k-rev-2", ledger))
	if codeOf(err) != domain.CodeReversalTarget || statusOf(err) != http.StatusConflict {
		t.Fatalf("duplicate reversal: %v", err)
	}
	if len(ledger.Entries()) != 2 {
		t.Fatalf("the duplicate reversal left %d ledger entries, want 2", len(ledger.Entries()))
	}
	if n := activationRows(t, f.admin, f.newVer.ID); n != 2 {
		t.Fatalf("activation rows %d", n)
	}
	if _, _, err := f.s.ActivateVersion(ctx, reversal(f.newVer.ID, rev.Activation.ID, rev.Version.Version, "k-rev-3", ledger)); codeOf(err) != domain.CodeReversalTarget {
		t.Fatalf("reversing a reversal: %v", err)
	}
	if len(ledger.Entries()) != 2 {
		t.Fatal("reversing a reversal posted")
	}
}

func TestActivation_ReversalTargetMustBeAnActivationOfThisVersion(t *testing.T) {
	f := newActivationFixture(t, true)
	ctx := as(1, "")
	ledger := ledgerclient.NewFake()
	act, _, err := f.s.ActivateVersion(ctx, f.activate(ledger, "k-act"))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := f.s.CreateVersion(ctx, spec(f.le, "STD-OTHER"), "k-other")
	if err != nil {
		t.Fatal(err)
	}
	seedCalculation(t, f.admin, f.le, other.ID, "Pending", "4.00")
	in := f.activate(ledger, "k-act-other")
	in.VersionID, in.Fingerprint = other.ID, other.ID
	otherAct, _, err := f.s.ActivateVersion(ctx, in)
	if err != nil || otherAct.Activation.Total.String() != "15" {
		t.Fatalf("second version: %+v %v", otherAct.Activation, err)
	}
	_, _, err = f.s.ActivateVersion(ctx, reversal(other.ID, act.Activation.ID, otherAct.Version.Version, "k-wrong", ledger))
	if codeOf(err) != domain.CodeReversalTarget || statusOf(err) == http.StatusConflict {
		t.Fatalf("an activation of another version: %v", err)
	}
	_, _, err = f.s.ActivateVersion(ctx, reversal(other.ID, ids.New(), otherAct.Version.Version, "k-missing", ledger))
	if codeOf(err) != domain.CodeReversalTarget || statusOf(err) != http.StatusNotFound {
		t.Fatalf("a missing activation: %v", err)
	}
	if len(ledger.Entries()) != 2 {
		t.Fatalf("refused reversals posted: %d entries", len(ledger.Entries()))
	}
}

func TestActivation_IsRefusedWhileARollHoldsTheVersionLock(t *testing.T) {
	f := newActivationFixture(t, true)
	roll, err := f.admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = roll.Rollback(context.Background()) }()
	if _, err := roll.Exec(context.Background(), `select pg_advisory_xact_lock(hashtextextended($1, 0))`, domain.RollLockKey(testkit.TenantID(1), f.newVer.ID)); err != nil {
		t.Fatal(err)
	}
	ledger := ledgerclient.NewFake()
	_, _, err = f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act"))
	if codeOf(err) != domain.CodeVersionRolling || statusOf(err) != http.StatusConflict {
		t.Fatalf("activation during a roll: %v", err)
	}
	if len(ledger.Entries()) != 0 {
		t.Fatal("posted during a roll")
	}
	if err := roll.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.ActivateVersion(as(1, ""), f.activate(ledger, "k-act")); err != nil {
		t.Fatalf("after the roll ended: %v", err)
	}
}

func TestActivation_LegalEntityScopeAppliesToTheVersion(t *testing.T) {
	f := newActivationFixture(t, true)
	if !f.rls {
		t.Skipf("%s is needed to run as the application role", testkit.EnvAppURL)
	}
	ledger := ledgerclient.NewFake()
	if _, _, err := f.s.ActivateVersion(as(1, ids.New()), f.activate(ledger, "k-sneak")); codeOf(err) != domain.CodeVersionNotFound {
		t.Fatalf("another scope activates a version: %v", err)
	}
	if len(ledger.Entries()) != 0 {
		t.Fatal("a journal was posted for a version outside the caller's scope")
	}
}
