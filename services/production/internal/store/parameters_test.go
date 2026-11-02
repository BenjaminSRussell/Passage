package store

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// plantParams are the service defaults the API would pass, with every switch off the table.
func plantParams(site string) domain.Parameters {
	return domain.Parameters{SiteID: site, RAFTolerancePct: d("0"), OverConsumptionPct: d("10"), AutoStartOnFeedback: true,
		SplitAllocation: domain.AllocProportional, TriggerPointsEnabled: true}
}

// startedAgain takes in to Started when its legal entity already has a posting profile.
func startedAgain(t *testing.T, s *Postgres, ctx context.Context, in NewOrder) *stepper {
	t.Helper()
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	return st
}

func saveParams(t *testing.T, s *Postgres, ctx context.Context, le string, mut func(*domain.Parameters), site string, ifMatch int) StoredParameters {
	t.Helper()
	prm := plantParams(site)
	if mut != nil {
		mut(&prm)
	}
	out, _, err := s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: ifMatch, Actor: "admin"})
	if err != nil {
		t.Fatalf("save parameters: %v", err)
	}
	return out
}

func TestParameters_CreateReplaceListAndEvent(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(70))
	le := ids.New()
	save := func(ifMatch int, site string) (StoredParameters, bool, error) {
		prm := plantParams(site)
		prm.RAFTolerancePct = d("2.5")
		return s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: ifMatch, Actor: "admin"})
	}
	p, created, err := save(0, domain.AllKey)
	if err != nil || !created || p.Version != 1 || !p.RAFTolerancePct.Equal(d("2.5")) || p.UpdatedBy != "admin" {
		t.Fatalf("create: %+v %v", p, err)
	}
	_, _, err = save(0, domain.AllKey)
	expectProblem(t, err, http.StatusPreconditionRequired, "precondition_required")
	_, _, err = save(9, domain.AllKey)
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	_, _, err = save(1, ids.New())
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	if p, created, err = save(1, domain.AllKey); err != nil || created || p.Version != 2 {
		t.Fatalf("replace: %+v %v", p, err)
	}
	list, err := s.ListParameters(ctx, le)
	if err != nil || len(list) != 1 || list[0].SiteID != domain.AllKey || list[0].SplitAllocation != domain.AllocProportional {
		t.Fatalf("list: %+v %v", list, err)
	}
	if other, err := s.ListParameters(tenantCtx(testkit.TenantID(71)), le); err != nil || len(other) != 0 {
		t.Fatalf("tenant leak: %+v %v", other, err)
	}
	// The create and the replace each enqueued parameters.saved in their transaction; the refused saves none.
	if got := outboxTypes(t, pool, le); len(got) != 2 || got[0] != prodevents.ParametersSaved || got[1] != prodevents.ParametersSaved {
		t.Fatalf("events: %v", got)
	}
}

// Two creates of the same site race: the one that loses the insert is a stale write (412),
// not a primary-key failure, and it enqueues nothing.
func TestParameters_ConcurrentCreateIsAConflict(t *testing.T) {
	pool, s := migrated(t)
	tenantID := testkit.TenantID(78)
	ctx := tenantCtx(tenantID)
	le := ids.New()
	bg := context.Background()
	tx, err := pool.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err := tx.Exec(bg, `insert into production_parameters (tenant_id, legal_entity_id, site_key, updated_by) values ($1, $2, $3, 'other')`,
		tenantID, le, domain.AllKey); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: plantParams(domain.AllKey), Actor: "admin"})
		done <- err
	}()
	// Wait until the save blocks on the uncommitted row, then let the other create win.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(bg, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and query like '%insert into production_parameters%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the save never waited on the concurrent insert")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, <-done, http.StatusPreconditionFailed, CodeVersionConflict)
	if got := outboxTypes(t, pool, le); len(got) != 0 {
		t.Fatalf("the refused save enqueued %v", got)
	}
}

// The RAF tolerance and the auto-start switch of a site override the service settings the
// request carries; another site keeps them.
func TestParameters_ToleranceAndAutoStart(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(72))
	le := ids.New()
	in := sampleOrder(t, "prm-raf", le)
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.RAFTolerancePct = d("10") }, in.SiteID, 0)
	st := startedOrder(t, s, ctx, in)
	// 105 of 100 passes under the site's 10%, although the request carries 0.
	if o := st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("105"); tr.RAFTolerancePct = d("0") }); !o.QtyRAF.Equal(d("105")) {
		t.Fatalf("RAF: %+v", o)
	}
	// Without stored parameters the request's tolerance applies and 105 is refused.
	other := startedAgain(t, s, ctx, sampleOrder(t, "prm-raf-2", le))
	_, err := other.step(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("105") })
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeRAFExceeds)

	// Auto-start off for one site: a route card on a Released order is refused there.
	site := ids.New()
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.AutoStartOnFeedback = false }, site, 0)
	released := func(key, siteID string) *stepper {
		o := sampleOrder(t, key, le)
		o.SiteID = siteID
		created, _, err := s.CreateOrder(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		r := &stepper{t: t, s: s, ctx: ctx, orderID: created.ID, version: created.Version}
		for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease} {
			r.must(cmd, nil)
		}
		return r
	}
	off := released("prm-auto-off", site)
	_, err = off.journal(NewJournal{Type: domain.JournalRouteCard, AutoStart: true, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}})
	expectProblem(t, err, http.StatusConflict, CodeJournalNotAllowed)
	on := released("prm-auto-on", ids.New())
	if _, err := on.journal(NewJournal{Type: domain.JournalRouteCard, AutoStart: true, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}}); err != nil {
		t.Fatalf("a site without parameters keeps the request's auto-start: %v", err)
	}
}

func TestParameters_EstimateOnCreateAndSplitAllocation(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(73))
	le := ids.New()
	in := sampleOrder(t, "prm-est", le)
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.EstimateOnCreate = true; p.SplitAllocation = domain.AllocKeepOnOriginal }, domain.AllKey, 0)
	allProfile(t, s, ctx, le)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil || o.Status != domain.Estimated {
		t.Fatalf("estimate on create from the All row: %+v %v", o.Status, err)
	}

	st := startedAgain(t, s, ctx, sampleOrder(t, "prm-split", le))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("100"), UnitCost: qty("10")}}}).ID)
	// No allocation in the request: the parameters' keep-on-original applies, nothing moves.
	res, err := s.SplitOrder(ctx, Split{OrderID: st.orderID, IfMatch: st.version, SplitQty: d("40"), IdempotencyKey: "prm-split-1", RequestHash: "h"})
	if err != nil || !res.New.Lines[0].QtyConsumed.IsZero() || !res.Original.Lines[0].QtyConsumed.Equal(d("100")) {
		t.Fatalf("split under keep-on-original: %+v %v", res, err)
	}
	// An explicit allocation wins over the parameters.
	o2, _ := s.GetOrder(ctx, st.orderID)
	res, err = s.SplitOrder(ctx, Split{OrderID: st.orderID, IfMatch: o2.Version, SplitQty: d("10"), Allocation: domain.AllocProportional, IdempotencyKey: "prm-split-2", RequestHash: "h2"})
	if err != nil || res.New.Lines[0].QtyConsumed.IsZero() {
		t.Fatalf("explicit proportional allocation: %+v %v", res.New.Lines, err)
	}
}

func TestParameters_TriggerPointsEnabled(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(74))
	le := ids.New()
	in := routedOrder(t, "prm-trig", le, 10, 20)
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.TriggerPointsEnabled = false }, in.SiteID, 0)
	st := startedOrder(t, s, ctx, in)
	st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 10, Condition: "qty_error > 0", Function: domain.FnNotify})
	draft := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 20, Hours: d("1"), QtyGood: d("97"), QtyError: d("3")}}})
	j := st.mustPost(draft.ID)
	if len(j.TriggerFirings) != 0 {
		t.Fatalf("a site with trigger points off fired %+v", j.TriggerFirings)
	}
}

// As-built required (FR-21.19): :end is refused for output without an as-built record, and
// goes through once a record is attached.
func TestParameters_AsBuiltRequiredAtEnd(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(75))
	le, motor := ids.New(), ids.New()
	bom := []domain.BOMComponent{{LineNo: "10", ItemID: motor, QtyPer: d("1"), UnitCost: d("100")}}
	in := orderOf(t, "prm-ab", le, ids.New(), "1", bom, "1")
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.AsBuiltRequired = true }, in.SiteID, 0)
	st := startedOrder(t, s, ctx, in)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("1") })
	_, err := st.step(domain.CmdEnd, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeAsBuiltRequired)
	if _, _, err := s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: st.orderID, Actor: "operator-1", IdempotencyKey: "prm-ab-1", RequestHash: "prm-ab-1",
		AsBuilt: domain.AsBuilt{FinishedSerial: "S-1", ComponentItemID: motor, Tracking: domain.TrackSerial, ComponentSerial: "M-1", Qty: d("1")}}); err != nil {
		t.Fatal(err)
	}
	st.refresh()
	if o := st.must(domain.CmdEnd, nil); o.Status != domain.Ended {
		t.Fatalf("end: %+v", o.Status)
	}
}

// Under the NOSUPERUSER NOBYPASSRLS application role the table is granted (select, insert,
// update) and scoped by tenant and legal entity.
func TestParameters_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantA, tenantB := testkit.TenantID(76), testkit.TenantID(77)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	saveParams(t, s, ctxA, leA, nil, domain.AllKey, 0)
	saveParams(t, s, ctxA, leA, func(p *domain.Parameters) { p.AsBuiltRequired = true }, domain.AllKey, 1)
	saveParams(t, s, ctxA, leB, nil, domain.AllKey, 0)

	scoped := scopedCtx(tenantA, leA)
	list, err := s.ListParameters(scoped, "")
	if err != nil || len(list) != 1 || list[0].LegalEntityID != leA || !list[0].AsBuiltRequired {
		t.Fatalf("legal-entity scope: %+v %v", list, err)
	}
	if _, _, err := s.SaveParameters(scoped, ParametersSave{LegalEntityID: leB, Parameters: plantParams(ids.New()), Actor: "x"}); err == nil {
		t.Fatal("parameters were saved outside the caller's legal-entity scope")
	}
	if foreign, err := s.ListParameters(tenantCtx(tenantB), ""); err != nil || len(foreign) != 0 {
		t.Fatalf("another tenant sees %d rows (%v)", len(foreign), err)
	}
}

// A replace that leaves reworkCapitalize out keeps the stored value; one that sets it changes it.
func TestParameters_ReworkCapitalizeKeptOnReplace(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(71))
	le := ids.New()
	if p := saveParams(t, s, ctx, le, nil, domain.AllKey, 0); p.ReworkCapitalize {
		t.Fatalf("created with the default: %+v", p)
	}
	saveParams(t, s, ctx, le, func(p *domain.Parameters) { p.ReworkCapitalize = true }, domain.AllKey, 1)
	prm := plantParams(domain.AllKey)
	out, _, err := s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: 2, Actor: "admin", Keep: KeepParameters{ReworkCapitalize: true}})
	if err != nil || !out.ReworkCapitalize {
		t.Fatalf("replace keeping reworkCapitalize: %+v %v", out, err)
	}
	if out, _, err = s.SaveParameters(ctx, ParametersSave{LegalEntityID: le, Parameters: prm, IfMatch: 3, Actor: "admin"}); err != nil || out.ReworkCapitalize {
		t.Fatalf("replace setting it off: %+v %v", out, err)
	}
}
