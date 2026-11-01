package store

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// ageOrder moves an order's creation date back, as the passing of days would.
func ageOrder(t *testing.T, pool *pgxpool.Pool, orderID string, days int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `update production_order set created_at = now() - make_interval(days => $2) where id = $1`, orderID, days); err != nil {
		t.Fatal(err)
	}
}

func orderIDs(found []HousekeepingOrder) map[string]bool {
	out := map[string]bool{}
	for _, o := range found {
		out[o.OrderID] = true
	}
	return out
}

// PRD 4.21.16 cancel-stale-created-orders: age by site parameters, Created only, not planned orders.
func TestHousekeeping_StaleCreatedOrders(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(170))
	le := ids.New()
	allProfile(t, s, ctx, le)
	siteX, siteShort, siteNever := ids.New(), ids.New(), ids.New()
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.StaleOrderDays = 30 }, siteShort, 0)
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.StaleOrderDays = 0 }, siteNever, 0)

	make := func(key, site, origin string, age int, estimate, reset bool) string {
		in := sampleOrder(t, key, le)
		in.SiteID, in.OriginType = site, origin
		o, _, err := s.CreateOrder(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if estimate {
			st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
			st.must(domain.CmdEstimate, nil)
			if reset {
				st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Created })
			}
		}
		ageOrder(t, pool, o.ID, age)
		return o.ID
	}
	old := make("old", siteX, "Manual", 100, false, false)
	young := make("young", siteX, "Manual", 10, false, false)
	planned := make("planned", siteX, "PlannedOrder", 100, false, false)
	estimated := make("estimated", siteX, "Manual", 100, true, false)
	never := make("never", siteNever, "Manual", 500, false, false)
	short := make("short", siteShort, "Manual", 40, false, false)
	// estimated, then reset back to Created: it was estimated once, so it is not stale
	reset := make("reset", siteX, "Manual", 100, true, true)
	otherEntity := sampleOrder(t, "other-entity", ids.New())
	oe, _, err := s.CreateOrder(ctx, otherEntity)
	if err != nil {
		t.Fatal(err)
	}
	ageOrder(t, pool, oe.ID, 400)

	scan := HousekeepingScan{LegalEntityID: le, Limit: 500}
	found, total, err := s.StaleCreatedOrders(ctx, scan, domain.DefaultStaleOrderDays)
	if err != nil || total != 2 || len(found) != 2 {
		t.Fatalf("stale: %d %v %v", total, found, err)
	}
	got := orderIDs(found)
	if !got[old] || !got[short] || got[young] || got[planned] || got[estimated] || got[reset] || got[never] || got[oe.ID] {
		t.Fatalf("candidates %v", got)
	}
	if found[0].OrderID != old || found[0].AgeDays != 100 || found[0].Status != domain.Created || found[0].Version != 1 {
		t.Fatalf("oldest first: %+v", found)
	}
	// the limit trims the list, not the total
	if one, n, err := s.StaleCreatedOrders(ctx, HousekeepingScan{LegalEntityID: le, Limit: 1}, domain.DefaultStaleOrderDays); err != nil || n != 2 || len(one) != 1 {
		t.Fatalf("limit: %d %v %v", n, one, err)
	}
	// the reviewed orders narrow the scan; one that does not qualify stays out
	if some, n, err := s.StaleCreatedOrders(ctx, HousekeepingScan{LegalEntityID: le, OrderIDs: []string{short, young}, Limit: 500}, domain.DefaultStaleOrderDays); err != nil || n != 1 || some[0].OrderID != short {
		t.Fatalf("reviewed: %d %v %v", n, some, err)
	}
	// the legal entity is matched as a UUID, whatever its case
	if upper, n, err := s.StaleCreatedOrders(ctx, HousekeepingScan{LegalEntityID: strings.ToUpper(le), Limit: 500}, domain.DefaultStaleOrderDays); err != nil || n != 2 || len(upper) != 2 {
		t.Fatalf("upper-case entity: %d %v %v", n, upper, err)
	}
	// a service default of 0 cancels nothing where no row applies
	if none, n, err := s.StaleCreatedOrders(ctx, scan, 0); err != nil || n != 1 || none[0].OrderID != short {
		t.Fatalf("default 0: %d %v %v", n, none, err)
	}
	// another tenant sees nothing
	if other, n, err := s.StaleCreatedOrders(tenantCtx(testkit.TenantID(171)), scan, 90); err != nil || n != 0 || len(other) != 0 {
		t.Fatalf("tenant leak: %d %v %v", n, other, err)
	}

	// a key never used replays nothing
	const hash = "housekeeping:cancel-stale-created-orders"
	if prior, err := s.HousekeepingReplay(ctx, le, "stale-1", hash); err != nil || len(prior) != 0 {
		t.Fatalf("fresh key: %v %v", prior, err)
	}
	// an order changed since the scan is refused at the version the scan saw, not moved
	versions := map[string]int{old: found[0].Version, short: found[1].Version}
	if _, err := pool.Exec(context.Background(), `update production_order set version = version + 1 where id = $1`, short); err != nil {
		t.Fatal(err)
	}
	items := s.BulkTransition(ctx, Bulk{OrderIDs: []string{old, short}, Versions: versions, Command: domain.CmdCancel, Actor: "planner-1", TraceID: "t", IdempotencyKey: "stale-1", RequestHash: hash})
	if !items[0].OK || items[0].Status != domain.Cancelled || items[1].OK || items[1].Code != CodeVersionConflict {
		t.Fatalf("pinned cancel: %+v", items)
	}
	// a retry under the key replays the order it moved, and only that one
	prior, err := s.HousekeepingReplay(ctx, le, "stale-1", hash)
	if err != nil || len(prior) != 1 || prior[0].OrderID != old || prior[0].Status != domain.Cancelled {
		t.Fatalf("replay: %+v %v", prior, err)
	}
	again := s.BulkTransition(ctx, Bulk{OrderIDs: []string{old}, Versions: map[string]int{old: prior[0].Version}, Command: domain.CmdCancel, Actor: "planner-1", TraceID: "t", IdempotencyKey: "stale-1", RequestHash: hash})
	if !again[0].OK || !again[0].Replayed {
		t.Fatalf("replayed cancel: %+v", again)
	}
	// the key used for another job, or in another legal entity, is refused
	_, err = s.HousekeepingReplay(ctx, le, "stale-1", "housekeeping:auto-end-fully-reported")
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)
	_, err = s.HousekeepingReplay(ctx, ids.New(), "stale-1", hash)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)
	// a key that only shares a prefix is a different key
	if other, err := s.HousekeepingReplay(ctx, le, "stale", hash); err != nil || len(other) != 0 {
		t.Fatalf("prefix: %v %v", other, err)
	}
	items = s.BulkTransition(ctx, Bulk{OrderIDs: []string{short}, Command: domain.CmdCancel, Actor: "planner-1", TraceID: "t", IdempotencyKey: "stale-2", RequestHash: hash})
	if !items[0].OK {
		t.Fatalf("cancel at current version: %+v", items)
	}
	if after, n, err := s.StaleCreatedOrders(ctx, scan, domain.DefaultStaleOrderDays); err != nil || n != 0 || len(after) != 0 {
		t.Fatalf("after: %d %v %v", n, after, err)
	}
	if log, err := s.StatusLog(ctx, old); err != nil || log[len(log)-1].Command != domain.CmdCancel || log[len(log)-1].To != domain.Cancelled {
		t.Fatalf("log: %+v %v", log, err)
	}
}

// PRD 4.21.16 auto-end-fully-reported: opt-in per site, whole quantity reported, no draft journal.
func TestHousekeeping_AutoEndFullyReported(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(172))
	le := ids.New()
	full := sampleOrder(t, "full", le)
	st := startedOrder(t, s, ctx, full)
	partial := sampleOrder(t, "partial", le)
	partial.SiteID = full.SiteID
	pst := startedAgain(t, s, ctx, partial)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	pst.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })

	// off by default
	scan := HousekeepingScan{LegalEntityID: le, Limit: 500}
	if found, n, err := s.FullyReportedOrders(ctx, scan); err != nil || n != 0 || len(found) != 0 {
		t.Fatalf("opt-in: %d %v %v", n, found, err)
	}
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.AutoEndEnabled = true }, domain.AllKey, 0)
	found, n, err := s.FullyReportedOrders(ctx, scan)
	if err != nil || n != 1 || found[0].OrderID != st.orderID || found[0].Status != domain.ReportedAsFinished || found[0].QtyRAF != "100" {
		t.Fatalf("fully reported only: %d %+v %v", n, found, err)
	}
	// a site row switches it off again for that site
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.AutoEndEnabled = false }, full.SiteID, 0)
	if _, n, err := s.FullyReportedOrders(ctx, scan); err != nil || n != 0 {
		t.Fatalf("site row off: %d %v", n, err)
	}
	// ... and a draft journal keeps an order out
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.AutoEndEnabled = true }, full.SiteID, 1)
	st.refresh()
	draft := st.mustJournal(NewJournal{Type: domain.JournalPickingList})
	if _, n, err := s.FullyReportedOrders(ctx, scan); err != nil || n != 0 {
		t.Fatalf("draft journal: %d %v", n, err)
	}
	// posting the draft makes the order a candidate, and the job's End settles it to zero WIP
	st.mustPost(draft.ID)
	found, n, err = s.FullyReportedOrders(ctx, scan)
	if err != nil || n != 1 {
		t.Fatalf("after post: %d %v", n, err)
	}
	items := s.BulkTransition(ctx, Bulk{OrderIDs: []string{found[0].OrderID}, Command: domain.CmdEnd, Actor: "planner-1", TraceID: "t", IdempotencyKey: "auto-end-1", RequestHash: "end"})
	if len(items) != 1 || !items[0].OK || items[0].Status != domain.Ended {
		t.Fatalf("end: %+v", items)
	}
	if again, n, err := s.FullyReportedOrders(ctx, scan); err != nil || n != 0 || len(again) != 0 {
		t.Fatalf("after end: %d %v %v", n, again, err)
	}
	if o := st.refresh(); o.Status != domain.Ended || !ledgerNet(t, pool, st.orderID).IsZero() {
		t.Fatalf("order %s net %s", o.Status, ledgerNet(t, pool, st.orderID))
	}
}

// The site's proposal defaults decide a journal that names none (PRD 4.21.13).
func TestParameters_ProposalDefaultsApplyToJournals(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(173))
	le := ids.New()
	in := sampleOrder(t, "proposal", le)
	st := startedOrder(t, s, ctx, in)
	create := func(typ domain.JournalType, key string) (Journal, error) {
		j, _, err := s.CreateJournal(ctx, NewJournal{OrderID: st.orderID, Type: typ, AutoStart: true, Actor: "supervisor-1", IdempotencyKey: key, RequestHash: key})
		return j, err
	}
	// no stored row: remaining for both
	if j, err := create(domain.JournalPickingList, "p1"); err != nil || len(j.Lines) == 0 {
		t.Fatalf("remaining picking list: %+v %v", j, err)
	}
	if j, err := create(domain.JournalRouteCard, "r1"); err != nil || len(j.Lines) == 0 {
		t.Fatalf("remaining route card: %+v %v", j, err)
	}
	// proportional with nothing reported proposes nothing; none proposes nothing
	saved := saveParams(t, s, ctx, le, func(p *domain.Parameters) {
		p.PickingListProposal, p.RouteCardProposal = domain.ProposeNone, domain.ProposeProportional
	}, in.SiteID, 0)
	if saved.PickingListProposal != domain.ProposeNone || saved.RouteCardProposal != domain.ProposeProportional || saved.StaleOrderDays != 0 || saved.AutoEndEnabled {
		t.Fatalf("stored: %+v", saved)
	}
	_, err := create(domain.JournalPickingList, "p2")
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeJournalEmpty)
	_, err = create(domain.JournalRouteCard, "r2")
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeJournalEmpty)
	// a request that names a proposal wins over the site default
	j, _, err := s.CreateJournal(ctx, NewJournal{OrderID: st.orderID, Type: domain.JournalPickingList, Proposal: domain.ProposeRemaining, Actor: "supervisor-1", IdempotencyKey: "p3", RequestHash: "p3"})
	if err != nil || len(j.Lines) == 0 {
		t.Fatalf("explicit proposal: %+v %v", j, err)
	}
}

// A replace from a client that omits the slice 17 settings keeps them; a create takes the defaults.
func TestParameters_OmittedHousekeepingSettingsAreKept(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(174))
	le := ids.New()
	saved := saveParams(t, s, ctx, le, func(p *domain.Parameters) {
		p.StaleOrderDays, p.AutoEndEnabled, p.PickingListProposal, p.RouteCardProposal = 0, true, domain.ProposeNone, domain.ProposeProportional
	}, domain.AllKey, 0)
	keepAll := KeepParameters{PickingListProposal: true, RouteCardProposal: true, StaleOrderDays: true, AutoEndEnabled: true}
	prm := plantParams(domain.AllKey)
	prm.StaleOrderDays, prm.PickingListProposal, prm.RouteCardProposal = domain.DefaultStaleOrderDays, domain.ProposeRemaining, domain.ProposeRemaining
	out, _, err := s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: saved.Version, Actor: "admin", Keep: keepAll})
	if err != nil || out.StaleOrderDays != 0 || !out.AutoEndEnabled || out.PickingListProposal != domain.ProposeNone || out.RouteCardProposal != domain.ProposeProportional {
		t.Fatalf("kept: %+v %v", out, err)
	}
	// a setting the request names is written
	prm.AutoEndEnabled = false
	out, _, err = s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: out.Version, Actor: "admin",
		Keep: KeepParameters{PickingListProposal: true, RouteCardProposal: true, StaleOrderDays: true}})
	if err != nil || out.AutoEndEnabled || out.StaleOrderDays != 0 {
		t.Fatalf("named: %+v %v", out, err)
	}
	// a create has nothing to keep: the values given (the defaults) are stored
	site := ids.New()
	out, _, err = s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: plantParamsWithDefaults(site), Actor: "admin", Keep: keepAll})
	if err != nil || out.StaleOrderDays != domain.DefaultStaleOrderDays || out.AutoEndEnabled || out.PickingListProposal != domain.ProposeRemaining {
		t.Fatalf("create: %+v %v", out, err)
	}
}

func plantParamsWithDefaults(site string) domain.Parameters {
	p := plantParams(site)
	p.StaleOrderDays = domain.DefaultStaleOrderDays
	return p
}

// Under the application role the scans see only the caller's legal entities, even when asked for another.
func TestHousekeeping_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantA := testkit.TenantID(175)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	var mine string
	for _, le := range []string{leA, leB} {
		o, _, err := s.CreateOrder(ctxA, sampleOrder(t, "rls-"+le, le))
		if err != nil {
			t.Fatal(err)
		}
		ageOrder(t, admin, o.ID, 200)
		if le == leA {
			mine = o.ID
		}
	}
	scoped := scopedCtx(tenantA, leA)
	if found, n, err := s.StaleCreatedOrders(scoped, HousekeepingScan{LegalEntityID: leA, Limit: 500}, 90); err != nil || n != 1 || found[0].OrderID != mine {
		t.Fatalf("own entity: %d %v %v", n, found, err)
	}
	if found, n, err := s.StaleCreatedOrders(scoped, HousekeepingScan{LegalEntityID: leB, Limit: 500}, 90); err != nil || n != 0 || len(found) != 0 {
		t.Fatalf("another entity leaks: %d %v %v", n, found, err)
	}
	items := s.BulkTransition(scoped, Bulk{OrderIDs: []string{mine}, Command: domain.CmdCancel, Actor: "planner-1", TraceID: "t", IdempotencyKey: "rls-1", RequestHash: "h"})
	if !items[0].OK {
		t.Fatalf("cancel: %+v", items)
	}
	if prior, err := s.HousekeepingReplay(scoped, leA, "rls-1", "h"); err != nil || len(prior) != 1 || prior[0].OrderID != mine {
		t.Fatalf("replay under RLS: %v %v", prior, err)
	}
}
