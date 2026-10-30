package store

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

func changeEvents(t *testing.T, pool *pgxpool.Pool, orderID, typ string) []prodevents.OrderChangeData {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`, orderID, typ)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []prodevents.OrderChangeData
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data prodevents.OrderChangeData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

// ocmOrderFor creates a sampleOrder of the product at the site and returns its stepper.
func ocmOrderFor(t *testing.T, s *Postgres, ctx context.Context, key, entity, product, site string) *stepper {
	t.Helper()
	in := sampleOrder(t, key, entity)
	in.ProductID, in.SiteID = product, site
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
}

func ecoNotice(eco, product string, mut func(*ChangeDetect)) ChangeDetect {
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: ids.New(), QtyPer: d("3"), UnitCost: d("11")},
		{LineNo: "30", ItemID: ids.New(), QtyPer: d("1"), UnitCost: d("2")},
	}
	in := ChangeDetect{EcoID: eco, ProductID: product, BOM: &bom, Actor: "engineer-1", TraceID: "trace-eco", RequestHash: "hash-" + eco}
	if mut != nil {
		mut(&in)
	}
	return in
}

func apply(s *Postgres, ctx context.Context, c OrderChange, key string, duty bool) (ChangeApplyResult, error) {
	return s.ApplyOrderChange(ctx, ChangeApply{ChangeID: c.ID, IfMatch: c.Version, IdempotencyKey: key, ReleaseDuty: duty, Actor: "planner-1", TraceID: "trace-apply"})
}

// FR-21.16 and BR-21.12: an ECO is detected on the open orders of the product only, the
// profile decides, the change cost is new minus old planned plus the surcharge, applying
// re-explodes and re-estimates through the order-edit rules, and every step is idempotent.
func TestOCM_DetectPreviewApply(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(61))
	entity, product, site := ids.New(), ids.New(), ids.New()
	allProfile(t, s, ctx, entity)
	if _, _, err := s.SaveOCMProfile(ctx, OCMProfileSave{LegalEntityID: entity, Actor: "admin",
		Profile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, Mode: domain.OCMManual, SurchargeFixed: d("25"), SurchargePct: d("10")}}); err != nil {
		t.Fatal(err)
	}
	// The same product at a site whose profile is none is skipped.
	ignored := ids.New()
	if _, _, err := s.SaveOCMProfile(ctx, OCMProfileSave{LegalEntityID: entity, Actor: "admin",
		Profile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: ignored, Mode: domain.OCMNone}}); err != nil {
		t.Fatal(err)
	}

	a := ocmOrderFor(t, s, ctx, "ocm-a", entity, product, site) // Created
	e := ocmOrderFor(t, s, ctx, "ocm-e", entity, product, site) // Released
	e.must(domain.CmdEstimate, nil)
	e.must(domain.CmdSchedule, nil)
	e.must(domain.CmdRelease, nil)
	b := ocmOrderFor(t, s, ctx, "ocm-b", entity, product, site) // Started later
	b.must(domain.CmdEstimate, nil)
	b.must(domain.CmdSchedule, nil)
	b.must(domain.CmdRelease, nil)
	started := ocmOrderFor(t, s, ctx, "ocm-started", entity, product, site)
	started.must(domain.CmdEstimate, nil)
	started.must(domain.CmdSchedule, nil)
	started.must(domain.CmdRelease, nil)
	started.must(domain.CmdStart, nil)
	other := ocmOrderFor(t, s, ctx, "ocm-other", entity, ids.New(), site)
	skipped := ocmOrderFor(t, s, ctx, "ocm-skipped", entity, product, ignored)

	res, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-1", product, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 3 || res.SkippedNone != 1 || res.Existing != 0 {
		t.Fatalf("detect: %d created, %d skipped by profile, %d existing", len(res.Created), res.SkippedNone, res.Existing)
	}
	byOrder := map[string]OrderChange{}
	for _, c := range res.Created {
		byOrder[c.OrderID] = c
	}
	for _, id := range []string{started.orderID, other.orderID, skipped.orderID} {
		if _, found := byOrder[id]; found {
			t.Fatalf("order %s must not be recorded", id)
		}
	}
	ca := byOrder[a.orderID]
	if ca.Status != "pending" || ca.ProfileMode != "manual" || ca.NeedsReview || ca.EcoID != "ECO-1" || !ca.ChangesBOM || ca.ChangesRoute ||
		ca.Delta != (domain.ChangeDelta{LinesAdded: 1, LinesRemoved: 1, LinesChanged: 1}) || ca.DeltaCount != 3 {
		t.Fatalf("record %+v", ca)
	}
	// 100 units: old material 2 × 100 × 10 + 100 × 10 = 3000; new 3 × 100 × 11 + 100 × 2 = 3500; delta 500;
	// surcharge 25 + 10% of 500 = 75.
	if !ca.ChangeCost.Equal(d("575")) || ca.Currency != "USD" {
		t.Fatalf("change cost %s %s, want 575", ca.ChangeCost, ca.Currency)
	}
	if ev := changeEvents(t, pool, a.orderID, prodevents.OrderChangeDetected); len(ev) != 1 || ev[0].ChangeID != ca.ID || ev[0].DeltaCount != 3 || ev[0].ChangeCost != "575" {
		t.Fatalf("detected events %+v", ev)
	}

	// Repeating the notice records nothing; another snapshot under the same ECO is refused.
	again, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-1", product, nil))
	if err != nil || len(again.Created) != 0 || again.Existing != 3 {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if ev := changeEvents(t, pool, a.orderID, prodevents.OrderChangeDetected); len(ev) != 1 {
		t.Fatalf("repeat emitted: %d events", len(ev))
	}
	_, err = s.DetectOrderChanges(ctx, ecoNotice("ECO-1", product, func(in *ChangeDetect) { in.RequestHash = "other" }))
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	// Preview is read-only and matches the stored cost.
	pv, err := s.PreviewOrderChange(ctx, ca.ID)
	if err != nil || pv.Blocked != nil || pv.Delta.Total() != 3 || !pv.Cost.Total.Equal(d("575")) || !pv.Cost.Delta.Equal(d("500")) ||
		!pv.Cost.Surcharge.Equal(d("75")) || pv.Edits.Count() != 3 {
		t.Fatalf("preview %+v %v", pv, err)
	}

	// Apply: stale If-Match, then the real thing.
	stale := ca
	stale.Version = 99
	_, err = apply(s, ctx, stale, "apply-a", false)
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	ap, err := apply(s, ctx, ca, "apply-a", false)
	if err != nil || ap.Replayed || ap.Change.Status != "applied" || ap.Change.BeforeHash == nil || ap.Change.AfterHash == nil ||
		*ap.Change.BeforeHash == *ap.Change.AfterHash || ap.Change.ResolvedBy == nil || *ap.Change.ResolvedBy != "planner-1" || ap.Change.Version != ca.Version+1 {
		t.Fatalf("apply %+v %v", ap, err)
	}
	if ap.Order.OrderBOMModified || ap.Order.Version != a.version+1 {
		t.Fatalf("an applied change must not flag the order as hand-edited: %+v", ap.Order.OrderBOMModified)
	}
	a.version = ap.Order.Version
	got := map[string]domain.OrderLine{}
	for _, l := range ap.Order.Lines {
		got[l.LineNo] = l
	}
	if len(got) != 2 || !got["10"].QtyRequired.Equal(d("300")) || !got["10"].UnitCost.Equal(d("11")) || !got["30"].QtyRequired.Equal(d("100")) {
		t.Fatalf("lines after apply: %+v", ap.Order.Lines)
	}
	if ev := changeEvents(t, pool, a.orderID, prodevents.OrderChangeApplied); len(ev) != 1 || ev[0].Status != "applied" || ev[0].OrderVersion != ap.Order.Version {
		t.Fatalf("applied events %+v", ev)
	}
	if ev := bomEditedEvents(t, pool, a.orderID); len(ev) != 1 {
		t.Fatalf("order.bomedited events: %d", len(ev))
	}
	// Replay with the same key answers the stored result; another key finds it resolved; the key is bound to its change.
	rp, err := apply(s, ctx, ca, "apply-a", false)
	if err != nil || !rp.Replayed || rp.Change.ID != ca.ID {
		t.Fatalf("replay %+v %v", rp, err)
	}
	_, err = apply(s, ctx, ap.Change, "apply-a2", false)
	expectProblem(t, err, http.StatusConflict, domain.CodeChangeResolved)
	_, err = apply(s, ctx, byOrder[e.orderID], "apply-a", true)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	// A Released order needs the duty of any BOM edit after release.
	ce := byOrder[e.orderID]
	_, err = apply(s, ctx, ce, "apply-e", false)
	expectProblem(t, err, http.StatusForbidden, CodeEditAfterRelease)
	if ap, err = apply(s, ctx, ce, "apply-e", true); err != nil || ap.Change.Status != "applied" {
		t.Fatalf("apply on Released with the duty: %+v %v", ap, err)
	}

	// An order that started since detection takes no change; the preview says why.
	b.must(domain.CmdStart, nil)
	cb := byOrder[b.orderID]
	pv, err = s.PreviewOrderChange(ctx, cb.ID)
	if err != nil || pv.Blocked == nil || pv.Blocked.Code != domain.CodeChangeNotEligible {
		t.Fatalf("preview of a started order: %+v %v", pv.Blocked, err)
	}
	_, err = apply(s, ctx, cb, "apply-b", true)
	expectProblem(t, err, http.StatusConflict, domain.CodeChangeNotEligible)

	// A hand-edited order is flagged for review; dismissing twice is a no-op, then apply is refused.
	if _, err := a.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "30", QtyPer: qty("2")}}}, false); err != nil {
		t.Fatal(err)
	}
	res2, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-2", product, func(in *ChangeDetect) {
		bom := []domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("4"), UnitCost: d("12")}}
		in.BOM = &bom
	}))
	if err != nil {
		t.Fatal(err)
	}
	var review OrderChange
	for _, c := range res2.Created {
		if c.OrderID == a.orderID {
			review = c
		}
	}
	if review.ID == "" || !review.NeedsReview {
		t.Fatalf("hand-edited order record: %+v", review)
	}
	dm, err := s.DismissOrderChange(ctx, ChangeDismiss{ChangeID: review.ID, IfMatch: review.Version, Note: "keep our version", Actor: "planner-1"})
	if err != nil || dm.Status != "dismissed" || dm.Note != "keep our version" || dm.ResolvedAt == nil {
		t.Fatalf("dismiss %+v %v", dm, err)
	}
	if again, err := s.DismissOrderChange(ctx, ChangeDismiss{ChangeID: review.ID, IfMatch: 1, Actor: "planner-1"}); err != nil || again.Version != dm.Version {
		t.Fatalf("dismiss twice: %+v %v", again, err)
	}
	if ev := changeEvents(t, pool, a.orderID, prodevents.OrderChangeDismissed); len(ev) != 1 {
		t.Fatalf("dismissed events: %d", len(ev))
	}
	_, err = apply(s, ctx, dm, "apply-dismissed", true)
	expectProblem(t, err, http.StatusConflict, domain.CodeChangeResolved)
	_, err = s.DismissOrderChange(ctx, ChangeDismiss{ChangeID: ca.ID, IfMatch: 1, Actor: "planner-1"})
	expectProblem(t, err, http.StatusConflict, domain.CodeChangeResolved)

	// The worklist filters and pages; another tenant sees nothing.
	page, err := s.ListOrderChanges(ctx, ChangeFilter{Status: "pending", Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	next, err := s.ListOrderChanges(ctx, ChangeFilter{Status: "pending", Limit: 10, Cursor: page.NextCursor})
	if err != nil || len(next.Items) == 0 || next.Items[0].ID == page.Items[0].ID {
		t.Fatalf("page 2: %+v %v", next, err)
	}
	for _, c := range append(page.Items, next.Items...) {
		if c.Status != "pending" {
			t.Fatalf("status filter leaked %s", c.Status)
		}
	}
	yes := true
	flagged, err := s.ListOrderChanges(ctx, ChangeFilter{NeedsReview: &yes, Limit: 10})
	if err != nil || len(flagged.Items) != 1 || flagged.Items[0].ID != review.ID {
		t.Fatalf("needsReview filter: %+v %v", flagged, err)
	}
	if foreign, err := s.ListOrderChanges(tenantCtx(testkit.TenantID(62)), ChangeFilter{Limit: 10}); err != nil || len(foreign.Items) != 0 {
		t.Fatalf("tenant leak: %+v %v", foreign, err)
	}
	_, err = s.GetOrderChange(tenantCtx(testkit.TenantID(62)), ca.ID)
	expectProblem(t, err, http.StatusNotFound, CodeChangeNotFound)
	if _, err := s.ListOrderChanges(ctx, ChangeFilter{Cursor: "garbage", Limit: 5}); err == nil {
		t.Fatal("a foreign cursor must be refused")
	}
}

// A route-only ECO leaves the BOM alone, an order that already matches records nothing,
// and a snapshot that cannot explode is a 422.
func TestOCM_RouteOnlyAndNoDelta(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(63))
	entity, product, site := ids.New(), ids.New(), ids.New()
	ocmOrderFor(t, s, ctx, "ocm-route", entity, product, site)

	ops := []domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.4"), LaborRate: d("30"), OverheadRate: d("10")}}
	res, err := s.DetectOrderChanges(ctx, ChangeDetect{EcoID: "ECO-R", ProductID: product, Operations: &ops, Actor: "e", RequestHash: "r"})
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("route ECO: %+v %v", res, err)
	}
	c := res.Created[0]
	if c.ChangesBOM || !c.ChangesRoute || c.Delta.OperationsChanged != 1 || c.Delta.LinesChanged != 0 {
		t.Fatalf("route record %+v", c)
	}
	// The resource changed too (a new id), and 100 × 0.5 → 100 × 0.4 hours: labor 1500 → 1200, overhead 500 → 400;
	// the default profile has no surcharge.
	if !c.ChangeCost.Equal(d("-400")) {
		t.Fatalf("change cost %s, want -400", c.ChangeCost)
	}
	ap, err := apply(s, ctx, c, "apply-r", false)
	if err != nil || len(ap.Order.Lines) != 2 || !ap.Order.Operations[0].RunHPer.Equal(d("0.4")) || !ap.Order.Operations[0].ProcessQty.Equal(d("100")) {
		t.Fatalf("apply: %+v %v", ap.Order, err)
	}
	res, err = s.DetectOrderChanges(ctx, ChangeDetect{EcoID: "ECO-R2", ProductID: product, Operations: &ops, Actor: "e", RequestHash: "r2"})
	if err != nil || len(res.Created) != 0 || res.SkippedNoDelta != 1 {
		t.Fatalf("no delta: %+v %v", res, err)
	}
	bad := []domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("1"), ScrapPct: d("100")}}
	_, err = s.DetectOrderChanges(ctx, ChangeDetect{EcoID: "ECO-bad", ProductID: product, BOM: &bad, Actor: "e", RequestHash: "bad"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeInvalidInput)
}

// A line with consumption is never removed by a change: the preview says so and apply is refused.
func TestOCM_ConsumedLineBlocksTheChange(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(64))
	entity, product, site := ids.New(), ids.New(), ids.New()
	a := ocmOrderFor(t, s, ctx, "ocm-consumed", entity, product, site)
	res, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-C", product, nil))
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("detect: %+v %v", res, err)
	}
	// Consumption needs a Started order and its journal, which a change no longer takes; stamp the line directly.
	if _, err := pool.Exec(context.Background(), `update production_bom_line set qty_consumed = 1 where order_id = $1 and line_number = '20.1'`, a.orderID); err != nil {
		t.Fatal(err)
	}
	pv, err := s.PreviewOrderChange(ctx, res.Created[0].ID)
	if err != nil || pv.Blocked == nil || pv.Blocked.Code != domain.CodeEditTargetInUse {
		t.Fatalf("preview: %+v %v", pv.Blocked, err)
	}
	_, err = apply(s, ctx, res.Created[0], "apply-c", false)
	expectProblem(t, err, http.StatusConflict, domain.CodeEditTargetInUse)
	o, err := s.GetOrder(ctx, a.orderID)
	if err != nil || len(o.Lines) != 2 || o.Version != a.version {
		t.Fatalf("a refused change must leave the order alone: %+v %v", o.Version, err)
	}
}

func TestOCMProfiles_CreateReplaceAndResolve(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(65))
	entity := ids.New()
	save := func(group string, mode domain.OCMMode, ifMatch int) (OCMProfile, bool, error) {
		return s.SaveOCMProfile(ctx, OCMProfileSave{LegalEntityID: entity, IfMatch: ifMatch, Actor: "admin",
			Profile: domain.OCMProfile{ItemGroup: group, SiteID: domain.AllKey, Mode: mode, SurchargePct: d("5")}})
	}
	p, created, err := save("FG", domain.OCMAutomatic, 0)
	if err != nil || !created || p.Version != 1 {
		t.Fatalf("create: %+v %v", p, err)
	}
	_, _, err = save("FG", domain.OCMManual, 0)
	expectProblem(t, err, http.StatusPreconditionRequired, "precondition_required")
	_, _, err = save("FG", domain.OCMManual, 7)
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	_, _, err = save("RM", domain.OCMManual, 2)
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	if p, created, err = save("FG", domain.OCMNone, 1); err != nil || created || p.Version != 2 || p.Mode != domain.OCMNone || !p.SurchargePct.Equal(d("5")) {
		t.Fatalf("replace: %+v %v", p, err)
	}
	list, err := s.ListOCMProfiles(ctx, entity)
	if err != nil || len(list) != 1 || list[0].ItemGroup != "FG" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if other, err := s.ListOCMProfiles(tenantCtx(testkit.TenantID(66)), entity); err != nil || len(other) != 0 {
		t.Fatalf("tenant leak: %+v %v", other, err)
	}
}

// Under the NOSUPERUSER NOBYPASSRLS application role the OCM tables are granted (select, insert,
// update) and scoped by tenant and legal entity.
func TestOCM_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantA, tenantB := testkit.TenantID(67), testkit.TenantID(68)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	product, site := ids.New(), ids.New()
	inB := ocmOrderFor(t, s, ctxA, "ocm-rls-b", leB, product, site)
	ocmOrderFor(t, s, ctxA, "ocm-rls-a", leA, product, site)

	res, err := s.DetectOrderChanges(ctxA, ecoNotice("ECO-RLS", product, nil))
	if err != nil || len(res.Created) != 2 {
		t.Fatalf("detect as the application role: %+v %v", res, err)
	}
	var changeB OrderChange
	for _, c := range res.Created {
		if c.OrderID == inB.orderID {
			changeB = c
		}
	}
	scoped := scopedCtx(tenantA, leA)
	page, err := s.ListOrderChanges(scoped, ChangeFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].LegalEntityID != leA {
		t.Fatalf("legal-entity scope: %+v %v", page.Items, err)
	}
	_, err = s.GetOrderChange(scoped, changeB.ID)
	expectProblem(t, err, http.StatusNotFound, CodeChangeNotFound)
	if foreign, err := s.ListOrderChanges(tenantCtx(tenantB), ChangeFilter{Limit: 10}); err != nil || len(foreign.Items) != 0 {
		t.Fatalf("another tenant sees %d changes (%v)", len(foreign.Items), err)
	}
	if _, _, err := s.SaveOCMProfile(scoped, OCMProfileSave{LegalEntityID: leB, Actor: "x",
		Profile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, Mode: domain.OCMManual}}); err == nil {
		t.Fatal("an OCM profile was saved outside the caller's legal-entity scope")
	}
	// The application role updates profiles and change records.
	if _, _, err := s.SaveOCMProfile(ctxA, OCMProfileSave{LegalEntityID: leA, Actor: "x", Profile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, Mode: domain.OCMManual}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveOCMProfile(ctxA, OCMProfileSave{LegalEntityID: leA, Actor: "x", IfMatch: 1, Profile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, Mode: domain.OCMNone}}); err != nil {
		t.Fatal(err)
	}
	ap, err := apply(s, ctxA, changeB, "rls-apply", false)
	if err != nil || ap.Change.Status != "applied" {
		t.Fatalf("apply as the application role: %+v %v", ap, err)
	}
}

func pendingChange(t *testing.T, s *Postgres, ctx context.Context, res ChangeDetectResult, orderID string) OrderChange {
	t.Helper()
	for _, c := range res.Created {
		if c.OrderID == orderID {
			return c
		}
	}
	t.Fatalf("no change recorded for order %s in %+v", orderID, res)
	return OrderChange{}
}

// FR-21.16 automatic profile: the pending change applies by itself at the order's next status
// transition, before the command; an order edited by hand (before or after detection) and a
// cancel leave the change pending for the planner.
func TestOCM_AutomaticProfileAppliesAtTheNextTransition(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(69))
	entity, product, site := ids.New(), ids.New(), ids.New()
	allProfile(t, s, ctx, entity)
	if _, _, err := s.SaveOCMProfile(ctx, OCMProfileSave{LegalEntityID: entity, Actor: "admin",
		Profile: domain.OCMProfile{ItemGroup: domain.AllKey, SiteID: domain.AllKey, Mode: domain.OCMAutomatic}}); err != nil {
		t.Fatal(err)
	}
	auto := ocmOrderFor(t, s, ctx, "auto-a", entity, product, site)
	editedBefore := ocmOrderFor(t, s, ctx, "auto-b", entity, product, site)
	if _, err := editedBefore.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "10", QtyPer: qty("2.5")}}}, false); err != nil {
		t.Fatal(err)
	}
	editedAfter := ocmOrderFor(t, s, ctx, "auto-c", entity, product, site)
	cancelled := ocmOrderFor(t, s, ctx, "auto-d", entity, product, site)

	res, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-AUTO", product, nil))
	if err != nil || len(res.Created) != 4 {
		t.Fatalf("detect: %+v %v", res, err)
	}
	ca := pendingChange(t, s, ctx, res, auto.orderID)
	if ca.ProfileMode != "automatic" || ca.NeedsReview || pendingChange(t, s, ctx, res, editedBefore.orderID).NeedsReview != true {
		t.Fatalf("records: %+v", res.Created)
	}
	// A hand edit after detection flags the pending change for review (and bumps its version).
	cc := pendingChange(t, s, ctx, res, editedAfter.orderID)
	if _, err := editedAfter.edit(domain.OrderEdits{Lines: []domain.LineEdit{{Op: domain.EditChange, LineNo: "10", QtyPer: qty("2.5")}}}, false); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetOrderChange(ctx, cc.ID); err != nil || !got.NeedsReview || got.Version != cc.Version+1 {
		t.Fatalf("hand edit after detection: %+v %v", got, err)
	}

	before := auto.version
	o := auto.must(domain.CmdEstimate, nil)
	got, err := s.GetOrderChange(ctx, ca.ID)
	if err != nil || got.Status != "applied" || got.ResolvedBy == nil || *got.ResolvedBy != "planner-1" || got.Note != "applied automatically at :estimate" ||
		got.BeforeHash == nil || got.AfterHash == nil {
		t.Fatalf("automatic change: %+v %v", got, err)
	}
	lines := map[string]domain.OrderLine{}
	for _, l := range o.Lines {
		lines[l.LineNo] = l
	}
	if o.Status != domain.Estimated || o.Version != before+2 || o.OrderBOMModified || len(lines) != 2 || !lines["10"].QtyRequired.Equal(d("300")) || lines["30"].LineNo == "" {
		t.Fatalf("order after the automatic change: status %s version %d lines %+v", o.Status, o.Version, o.Lines)
	}
	// The estimate the command wrote prices the new BOM: 3 × 100 × 11 + 100 × 2 material.
	material := decimal.Zero
	for _, c := range o.Costs {
		if c.Group == domain.GroupMaterial {
			material = material.Add(c.Amount)
		}
	}
	if !material.Equal(d("3500")) {
		t.Fatalf("estimate after the automatic change: material %s, want 3500", material)
	}
	if ev := changeEvents(t, pool, auto.orderID, prodevents.OrderChangeApplied); len(ev) != 1 || ev[0].ChangeID != ca.ID {
		t.Fatalf("applied events %+v", ev)
	}

	for name, st := range map[string]*stepper{"edited before detection": editedBefore, "edited after detection": editedAfter} {
		st.must(domain.CmdEstimate, nil)
		if ev := changeEvents(t, pool, st.orderID, prodevents.OrderChangeApplied); len(ev) != 0 {
			t.Fatalf("%s: a hand-edited order was changed automatically", name)
		}
	}
	cd := pendingChange(t, s, ctx, res, cancelled.orderID)
	cancelled.must(domain.CmdCancel, nil)
	if got, err := s.GetOrderChange(ctx, cd.ID); err != nil || got.Status != "pending" {
		t.Fatalf("cancel applied the change: %+v %v", got, err)
	}
}

// A later ECO replacing the same part dismisses the older pending change of the order; one
// that replaces only part of it blocks the older change instead, so applying it cannot undo
// the newer version.
func TestOCM_LaterECOSupersedesOlderChanges(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(70))
	entity, product, site := ids.New(), ids.New(), ids.New()
	o := ocmOrderFor(t, s, ctx, "sup-a", entity, product, site)

	first, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-S1", product, nil))
	if err != nil || len(first.Created) != 1 {
		t.Fatalf("detect S1: %+v %v", first, err)
	}
	s1 := first.Created[0]
	second, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-S2", product, func(in *ChangeDetect) {
		bom := []domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("5"), UnitCost: d("1")}}
		in.BOM = &bom
	}))
	if err != nil || len(second.Created) != 1 {
		t.Fatalf("detect S2: %+v %v", second, err)
	}
	got, err := s.GetOrderChange(ctx, s1.ID)
	if err != nil || got.Status != "dismissed" || got.Note != "superseded by ECO-S2" || got.ResolvedBy == nil || *got.ResolvedBy != "engineer-1" {
		t.Fatalf("older change: %+v %v", got, err)
	}
	if ev := changeEvents(t, pool, o.orderID, prodevents.OrderChangeDismissed); len(ev) != 1 || ev[0].ChangeID != s1.ID {
		t.Fatalf("dismissed events %+v", ev)
	}

	// A route-only ECO does not cover the BOM change: both stay pending and both apply.
	ops := []domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.4"), LaborRate: d("30"), OverheadRate: d("10")}}
	third, err := s.DetectOrderChanges(ctx, ChangeDetect{EcoID: "ECO-S3", ProductID: product, Operations: &ops, Actor: "e", RequestHash: "s3"})
	if err != nil || len(third.Created) != 1 {
		t.Fatalf("detect S3: %+v %v", third, err)
	}
	if got, err := s.GetOrderChange(ctx, second.Created[0].ID); err != nil || got.Status != "pending" {
		t.Fatalf("a route ECO dismissed a BOM change: %+v %v", got, err)
	}
	if _, err := apply(s, ctx, second.Created[0], "sup-apply-2", false); err != nil {
		t.Fatal(err)
	}
	if _, err := apply(s, ctx, third.Created[0], "sup-apply-3", false); err != nil {
		t.Fatal(err)
	}

	// A BOM-and-route change followed by a BOM-only ECO: the older one is blocked, not dismissed.
	p := ocmOrderFor(t, s, ctx, "sup-b", entity, product, site)
	both, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-P1", product, func(in *ChangeDetect) { in.Operations = &ops; in.SiteID = site }))
	if err != nil {
		t.Fatal(err)
	}
	p1 := pendingChange(t, s, ctx, both, p.orderID)
	if _, err := s.DetectOrderChanges(ctx, ecoNotice("ECO-P2", product, func(in *ChangeDetect) {
		bom := []domain.BOMComponent{{LineNo: "10", ItemID: ids.New(), QtyPer: d("6"), UnitCost: d("1")}}
		in.BOM = &bom
	})); err != nil {
		t.Fatal(err)
	}
	pv, err := s.PreviewOrderChange(ctx, p1.ID)
	if err != nil || pv.Change.Status != "pending" || pv.Blocked == nil || pv.Blocked.Code != domain.CodeChangeSuperseded {
		t.Fatalf("preview of the older change: %+v %v", pv.Blocked, err)
	}
	_, err = apply(s, ctx, p1, "sup-apply-p1", false)
	expectProblem(t, err, http.StatusConflict, domain.CodeChangeSuperseded)
}
