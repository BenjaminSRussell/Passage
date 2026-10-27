package store

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

func testProfile(group, site, wip string) domain.Profile {
	return domain.Profile{ItemGroup: group, SiteID: site, WIP: wip, Inventory: "1400", FinishedGoods: "1410", LaborAbsorption: "5510",
		OverheadAbsorption: "5520", VarLotSize: "5610", VarQuantity: "5620", VarPrice: "5630", VarSubstitution: "5640"}
}

// allProfile stores the All × All posting profile of a legal entity, WIP account 1450.
func allProfile(t *testing.T, s *Postgres, ctx context.Context, legalEntity string) {
	t.Helper()
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: legalEntity, Profile: testProfile(domain.AllKey, domain.AllKey, "1450"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
}

// startedOrder creates in with an All × All profile and takes it to Started.
func startedOrder(t *testing.T, s *Postgres, ctx context.Context, in NewOrder) *stepper {
	t.Helper()
	allProfile(t, s, ctx, in.LegalEntityID)
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

// journal creates a draft on the stepper's order.
func (st *stepper) journal(in NewJournal) (Journal, error) {
	st.n++
	in.OrderID, in.Actor = st.orderID, "supervisor-1"
	in.IdempotencyKey = st.orderID + "-journal-" + string(rune('a'+st.n))
	in.RequestHash = in.IdempotencyKey
	if in.Proposal == "" {
		in.Proposal = domain.ProposeRemaining
	}
	j, _, err := st.s.CreateJournal(st.ctx, in)
	return j, err
}

// post posts a journal under a fresh key and refreshes the order version.
func (st *stepper) post(journalID string, mut func(*PostRequest)) (Journal, error) {
	st.n++
	r := PostRequest{JournalID: journalID, OverConsumptionPct: decimal.NewFromInt(10), AutoStart: true, Actor: "supervisor-1",
		TraceID: "trace-2", IdempotencyKey: journalID + "-post-" + string(rune('a'+st.n))}
	if mut != nil {
		mut(&r)
	}
	j, _, err := st.s.PostJournal(st.ctx, r)
	if err == nil {
		st.refresh()
	}
	return j, err
}

func (st *stepper) mustJournal(in NewJournal) Journal {
	st.t.Helper()
	j, err := st.journal(in)
	if err != nil {
		st.t.Fatalf("create %s journal: %v", in.Type, err)
	}
	return j
}

func (st *stepper) mustPost(journalID string) Journal {
	st.t.Helper()
	j, err := st.post(journalID, nil)
	if err != nil {
		st.t.Fatalf("post %s: %v", journalID, err)
	}
	return j
}

func (st *stepper) refresh() Order {
	st.t.Helper()
	o, err := st.s.GetOrder(st.ctx, st.orderID)
	if err != nil {
		st.t.Fatal(err)
	}
	st.version = o.Version
	return o
}

func (st *stepper) costs() CostSummary {
	st.t.Helper()
	c, err := st.s.Costs(st.ctx, st.orderID)
	if err != nil {
		st.t.Fatal(err)
	}
	return c
}

func groupSum(rows []domain.CostRow, g domain.CostGroup) decimal.Decimal {
	sum := decimal.Zero
	for _, r := range rows {
		if r.Group == g {
			sum = sum.Add(r.Amount)
		}
	}
	return sum
}

// ledgerNet is Σ dr − Σ cr over every WIP-subledger row of the order (all accounts).
func ledgerNet(t *testing.T, pool *pgxpool.Pool, orderID string) decimal.Decimal {
	t.Helper()
	var v string
	if err := pool.QueryRow(context.Background(), `select coalesce(sum(amount_dr - amount_cr), 0)::text from production_wip_posting where order_id = $1`, orderID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return d(v)
}

func qty(s string) *decimal.Decimal { v := d(s); return &v }

// AC-21.6 with the BR-21.7 worked example: 305 kg at 10.20, 46 h, RAF 100,
// End posts four variances totalling −49.00 and leaves WIP at 0.00.
func TestJournals_WorkedSettlementExample(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(41))
	st := startedOrder(t, s, ctx, sampleOrder(t, "key-br217", ids.New()))

	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: qty("10.20")}, {LineNo: "20.1", Qty: d("102"), UnitCost: qty("10.20")}}})
	if pick.Posted || pick.JournalNumber != "PJ-000001" || len(pick.Lines) != 2 {
		t.Fatalf("draft picking list: %+v", pick)
	}
	posted := st.mustPost(pick.ID)
	if !posted.Posted || !posted.Lines[0].Amount.Equal(d("2070.6")) || !posted.Lines[1].Amount.Equal(d("1040.4")) || len(posted.Postings) != 4 {
		t.Fatalf("posted picking list: %+v", posted)
	}
	if !domain.Balanced(posted.Postings) || !domain.Net(posted.Postings, true).Equal(d("3111")) {
		t.Fatalf("picking postings: %+v", posted.Postings)
	}

	route := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("100")}}})
	st.mustPost(route.ID)
	o := st.refresh()
	if !o.Lines[0].QtyConsumed.Equal(d("203")) || !o.Operations[0].HoursReported.Equal(d("46")) || o.Operations[0].Status != domain.OperStarted {
		t.Fatalf("booked consumption and hours: %+v %+v", o.Lines, o.Operations)
	}

	raf := st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	if raf.Status != domain.ReportedAsFinished || !raf.QtyRAF.Equal(d("100")) {
		t.Fatalf("RAF: %s %s", raf.Status, raf.QtyRAF)
	}
	before := st.costs()
	if !before.WIPBalance.Equal(d("-49")) || !groupSum(before.Actual, domain.GroupMaterial).Equal(d("3111")) ||
		!groupSum(before.Actual, domain.GroupLabor).Equal(d("1380")) || !groupSum(before.Actual, domain.GroupOverhead).Equal(d("460")) {
		t.Fatalf("before End: wip %s actual %+v", before.WIPBalance, before.Actual)
	}

	if ended := st.must(domain.CmdEnd, nil); ended.Status != domain.Ended {
		t.Fatalf("end: %s", ended.Status)
	}
	after := st.costs()
	want := map[string]string{
		"production-quantity/material": "50", "production-price/material": "61",
		"production-quantity/labor": "-120", "production-quantity/overhead": "-40",
	}
	total := decimal.Zero
	for _, v := range after.Variances {
		key := string(v.Type) + "/" + string(v.Group)
		if w, ok := want[key]; !ok || !v.Amount.Equal(d(w)) {
			t.Errorf("variance %s = %s, want %s", key, v.Amount, w)
		}
		total = total.Add(v.Amount)
	}
	if len(after.Variances) != 4 || !total.Equal(d("-49")) || !after.WIPBalance.IsZero() {
		t.Fatalf("settlement: %d variances total %s, WIP %s", len(after.Variances), total, after.WIPBalance)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("the WIP subledger does not balance: %s", net)
	}

	var payload json.RawMessage
	if err := pool.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, st.orderID, prodevents.OrderSettled).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data prodevents.OrderSettledData `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || len(evt.Data.Variances) != 4 || evt.Data.WIPCleared != "-49" ||
		evt.Data.ActualCost.Material != "3111.00" || evt.Data.PlannedCost.Labor != "1500.00" || len(evt.Data.Postings) != 5 {
		t.Fatalf("order.settled: %s", payload)
	}
	posts := 0
	for _, typ := range outboxTypes(t, pool, st.orderID) {
		if typ == prodevents.JournalPosted {
			posts++
		}
	}
	if posts != 3 {
		t.Fatalf("want 3 journal.posted events (picking, route card, RAF), got %d", posts)
	}
}

// AC-21.4: start-flushed lines are issued at Start; finish-flushed lines are
// consumed with each RAF in proportion, and RAF of the full quantity consumes
// exactly qty per × quantity × (1 + scrap%).
func TestJournals_Backflush(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(42))
	in := sampleOrder(t, "key-backflush", ids.New())
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: ids.New(), QtyPer: d("3"), ScrapPct: d("5"), UnitCost: d("10"), Flushing: domain.FlushStart},
		{LineNo: "20", ItemID: ids.New(), QtyPer: d("0.3333"), ScrapPct: d("2"), UnitCost: d("4"), Flushing: domain.FlushFinish},
	}
	lines, err := domain.Explode(bom, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	in.Lines = lines
	st := startedOrder(t, s, ctx, in)

	o := st.refresh()
	if !o.Lines[0].QtyConsumed.Equal(d("315")) || !o.Lines[1].QtyConsumed.IsZero() {
		t.Fatalf("after Start: %s %s", o.Lines[0].QtyConsumed, o.Lines[1].QtyConsumed)
	}
	js, err := s.ListJournals(ctx, st.orderID)
	if err != nil || len(js) != 1 || js[0].Type != domain.JournalPickingList || !js[0].Posted {
		t.Fatalf("Start backflush journal: %+v %v", js, err)
	}

	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("40") })
	if o = st.refresh(); !o.Lines[1].QtyConsumed.Equal(d("13.59864")) {
		t.Fatalf("after RAF 40: %s", o.Lines[1].QtyConsumed)
	}
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })
	o = st.refresh()
	exact := d("0.3333").Mul(d("100")).Mul(d("1.02"))
	if !o.Lines[1].QtyConsumed.Equal(exact) || !o.Lines[1].QtyConsumed.Equal(o.Lines[1].QtyRequired) || !o.Lines[0].QtyConsumed.Equal(d("315")) {
		t.Fatalf("after RAF 100: consumed %s, want %s", o.Lines[1].QtyConsumed, exact)
	}

	// Nothing manual is left to pick.
	_, err = st.journal(NewJournal{Type: domain.JournalPickingList})
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeJournalEmpty)
}

// AC-21.7: after a partial RAF (60 of 100) the WIP balance is the posted
// consumption less 60 × the planned unit cost, a reset reverses the RAF, and
// End clears the WIP to zero.
func TestJournals_PartialOutputAndReset(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(43))
	st := startedOrder(t, s, ctx, sampleOrder(t, "key-partial", ids.New()))

	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList})
	if len(pick.Lines) != 2 || !pick.Lines[0].Qty.Equal(d("200")) || !pick.Lines[1].Qty.Equal(d("100")) {
		t.Fatalf("remaining proposal: %+v", pick.Lines)
	}
	st.mustPost(pick.ID)
	route := st.mustJournal(NewJournal{Type: domain.JournalRouteCard})
	if len(route.Lines) != 1 || !route.Lines[0].Qty.Equal(d("50")) {
		t.Fatalf("route-card proposal: %+v", route.Lines)
	}
	st.mustPost(route.ID)

	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })
	if c := st.costs(); !c.WIPBalance.Equal(d("2000")) {
		t.Fatalf("WIP after RAF 60: %s, want 5000 − 60 × 50", c.WIPBalance)
	}

	// FR-21.9: reset to Started reverses the RAF journal.
	back := st.must(domain.CmdResetStatus, nil)
	if back.Status != domain.Started || !back.QtyRAF.IsZero() {
		t.Fatalf("reset: %s %s", back.Status, back.QtyRAF)
	}
	if c := st.costs(); !c.WIPBalance.Equal(d("5000")) {
		t.Fatalf("WIP after the RAF reversal: %s", c.WIPBalance)
	}
	js, err := s.ListJournals(ctx, st.orderID)
	if err != nil || len(js) != 4 || js[2].ReversedBy == nil || js[3].ReversalOf == nil || *js[3].ReversalOf != js[2].ID ||
		!js[3].Lines[0].Qty.Equal(d("-60")) {
		t.Fatalf("journals after reset: %+v %v", js, err)
	}

	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })
	st.must(domain.CmdEnd, nil)
	c := st.costs()
	total := decimal.Zero
	for _, v := range c.Variances {
		total = total.Add(v.Amount)
	}
	if !c.WIPBalance.IsZero() || !total.Equal(d("2000")) {
		t.Fatalf("End after partial output: WIP %s, variances %s (%+v)", c.WIPBalance, total, c.Variances)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("subledger net %s", net)
	}
}

func TestJournals_Rules(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(44))
	in := sampleOrder(t, "key-rules", ids.New())
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease} {
		st.must(cmd, nil)
	}

	// Picking needs a Started order; a route card auto-starts a Released one (FR-21.6).
	_, err = st.journal(NewJournal{Type: domain.JournalPickingList})
	expectProblem(t, err, http.StatusConflict, CodeJournalNotAllowed)
	_, err = st.journal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}})
	expectProblem(t, err, http.StatusConflict, CodeJournalNotAllowed) // AutoStart off
	route := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, AutoStart: true, Hours: []HoursInput{{OperNumber: 10, Hours: d("2"), Finished: true}}})
	st.mustPost(route.ID)
	if got := st.refresh(); got.Status != domain.Started || got.Operations[0].Status != domain.OperFinished {
		t.Fatalf("auto-start: %s, operation %s", got.Status, got.Operations[0].Status)
	}
	log, _ := s.StatusLog(ctx, st.orderID)
	if last := log[len(log)-1]; last.Command != domain.CmdStart || last.From != domain.Released || last.To != domain.Started || last.Actor != "supervisor-1" {
		t.Fatalf("auto-start log: %+v", last)
	}
	_, err = st.journal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("1")}}})
	expectProblem(t, err, http.StatusConflict, CodeOperationFinished)

	// FR-21.5: 221 of 200 is beyond the 10% tolerance without the duty.
	over := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("221")}}})
	_, err = st.post(over.ID, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeOverConsumption)
	key := over.ID + "-post-with-duty"
	first, replayed, err := s.PostJournal(ctx, PostRequest{JournalID: over.ID, OverConsumptionDuty: true, OverConsumptionPct: d("10"), Actor: "supervisor-1", IdempotencyKey: key})
	if err != nil || replayed || !first.Posted {
		t.Fatalf("post with the duty: %v %v", replayed, err)
	}
	again, replayed, err := s.PostJournal(ctx, PostRequest{JournalID: over.ID, IdempotencyKey: key})
	if err != nil || !replayed || again.ID != over.ID {
		t.Fatalf("replay: %v %v", replayed, err)
	}
	_, err = st.post(over.ID, nil)
	expectProblem(t, err, http.StatusConflict, CodeJournalPosted)
	expectProblem(t, s.DeleteJournal(ctx, over.ID), http.StatusConflict, CodeJournalPosted)

	// BR-21.3: no posting before the order was created.
	early := st.mustJournal(NewJournal{Type: domain.JournalPickingList, PostingDate: "2000-01-03", Issues: []IssueInput{{LineNo: "20.1", Qty: d("1")}}})
	_, _, err = s.PostJournal(ctx, PostRequest{JournalID: early.ID, IdempotencyKey: key})
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)
	_, err = st.post(early.ID, nil)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodePostingBeforeOrder)

	// PROD-0012: End refuses open journals; deleting the draft clears the way.
	st.refresh()
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	_, err = st.step(domain.CmdEnd, nil)
	expectProblem(t, err, http.StatusConflict, CodeUnpostedJournals)
	if err := s.DeleteJournal(ctx, early.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetJournal(ctx, early.ID)
	expectProblem(t, err, http.StatusNotFound, CodeJournalNotFound)

	// A reset to Released reverses every posted journal.
	released := st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	if released.Status != domain.Released || !released.QtyRAF.IsZero() || !released.Lines[0].QtyConsumed.IsZero() ||
		!released.Operations[0].HoursReported.IsZero() || released.Operations[0].Status != domain.OperOpen {
		t.Fatalf("reset to Released: %+v", released)
	}
	if c := st.costs(); !c.WIPBalance.IsZero() || len(c.Actual) != 0 {
		t.Fatalf("WIP after a full reversal: %s %+v", c.WIPBalance, c.Actual)
	}
	if c := st.must(domain.CmdCancel, nil); c.Status != domain.Cancelled {
		t.Fatalf("cancel: %s", c.Status)
	}
}

// FR-21.8: an order ended with zero output sends 100% of its cost to variance
// (with the duty), and a missing posting profile refuses the posting.
func TestJournals_ZeroOutputAndMissingProfile(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(45))
	st := startedOrder(t, s, ctx, sampleOrder(t, "key-zero", ids.New()))
	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("50")}}})
	st.mustPost(pick.ID)
	st.must(domain.CmdEnd, func(tr *Transition) { tr.ZeroOutputDuty = true })
	c := st.costs()
	total := decimal.Zero
	for _, v := range c.Variances {
		total = total.Add(v.Amount)
	}
	if !total.Equal(d("500")) || !c.WIPBalance.IsZero() {
		t.Fatalf("zero-output settlement: variances %s WIP %s", total, c.WIPBalance)
	}

	other := sampleOrder(t, "key-noprofile", ids.New())
	o, _, err := s.CreateOrder(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	ns := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		ns.must(cmd, nil)
	}
	_, err = ns.step(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("1") })
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeNoPostingProfile)
}

// FR-21.13: profiles are created, replaced under If-Match, and the most
// specific one (item group × site) posts the journal.
func TestJournals_PostingProfiles(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(46))
	in := sampleOrder(t, "key-profile", ids.New())
	in.ItemGroup = "FG"
	le := in.LegalEntityID

	all, created, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, "1450"), Actor: "admin"})
	if err != nil || !created || all.Version != 1 || all.UpdatedBy != "admin" {
		t.Fatalf("create: %+v %v %v", all, created, err)
	}
	_, _, err = s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, "1451"), Actor: "admin"})
	expectProblem(t, err, http.StatusPreconditionRequired, "precondition_required")
	_, _, err = s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, "1451"), IfMatch: 7, Actor: "admin"})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	replaced, created, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, "1451"), IfMatch: 1, Actor: "admin"})
	if err != nil || created || replaced.Version != 2 || replaced.WIP != "1451" {
		t.Fatalf("replace: %+v %v %v", replaced, created, err)
	}
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile("FG", in.SiteID, "1460"), Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPostingProfiles(ctx, le)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}

	o, _, err := s.CreateOrder(ctx, in)
	if err != nil || o.ItemGroup != "FG" {
		t.Fatalf("create with item group: %v %+v", err, o)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart} {
		st.must(cmd, nil)
	}
	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("1")}}})
	posted := st.mustPost(pick.ID)
	if posted.Postings[0].Account != "1460" || !posted.Postings[0].WIP || posted.Postings[1].Account != "1400" {
		t.Fatalf("postings did not use the FG × site profile: %+v", posted.Postings)
	}
}
