package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// Accounts of testProfile (All × All, WIP 1450).
const (
	acctWIP      = "1450"
	acctInv      = "1400"
	acctFG       = "1410"
	acctVarQty   = "5620"
	acctVarPrice = "5630"
	acctVarSub   = "5640"
	acctLabor    = "5510"
	acctOverhead = "5520"
)

// accountNets is Σ dr − Σ cr per ledger account over the order's WIP subledger
// rows (zero nets left out), and the number of rows.
func accountNets(ctx context.Context, pool *pgxpool.Pool, orderID string) (map[string]decimal.Decimal, int, error) {
	rows, err := pool.Query(ctx, `select ledger_account, sum(amount_dr - amount_cr)::text, count(*) from production_wip_posting
		where order_id = $1 group by ledger_account`, orderID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, n := map[string]decimal.Decimal{}, 0
	for rows.Next() {
		var acct, net string
		var c int
		if err := rows.Scan(&acct, &net, &c); err != nil {
			return nil, 0, err
		}
		n += c
		if v := d(net); !v.IsZero() {
			out[acct] = v
		}
	}
	return out, n, rows.Err()
}

// eventAccountNets is the same per-account net over the ledger lines the order's
// journal.posted and order.settled events carry: what the general ledger books.
func eventAccountNets(ctx context.Context, pool *pgxpool.Pool, orderID string) (map[string]decimal.Decimal, error) {
	rows, err := pool.Query(ctx, `select payload from outbox where aggregate_id = $1 and type = any($2)`, orderID,
		[]string{prodevents.JournalPosted, prodevents.OrderSettled})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]decimal.Decimal{}
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var evt struct {
			Data struct {
				Postings []prodevents.Posting `json:"postings"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			return nil, err
		}
		balance := decimal.Zero
		for _, p := range evt.Data.Postings {
			v := d(p.Debit).Sub(d(p.Credit))
			out[p.Account] = out[p.Account].Add(v)
			balance = balance.Add(v)
		}
		if !balance.IsZero() {
			return nil, fmt.Errorf("an event's ledger lines do not balance: %s", raw)
		}
	}
	for k, v := range out {
		if v.IsZero() {
			delete(out, k)
		}
	}
	return out, rows.Err()
}

func sameNets(want, got map[string]decimal.Decimal) bool {
	for k, v := range want {
		if !v.IsZero() && !got[k].Equal(v) {
			return false
		}
	}
	for k, v := range got {
		if !v.IsZero() && !want[k].Equal(v) {
			return false
		}
	}
	return true
}

// lifecycleOrder is the AC1 order. Every amount it posts is derived by hand:
//
//	quantity 100; line 10: 3 per unit + 5% scrap at 10.00, start-flushed → 315 × 10 = 3150.00 at :start
//	line 20: 0.5 per unit at 4.00, finish-flushed → a RAF of q consumes 0.5q × 4 = 2q
//	operation 10: 0.2 h per unit at 30.00 labor and 10.00 overhead → 20 h, 600.00 + 200.00
//	planned 3150 + 200 + 600 + 200 = 4150.00, so a RAF of q receives 41.50q
//
// With Q reported in all, End clears WIP 3150 − 39.5Q into: quantity variance
// (line 10) (315 − 3.15Q) × 10 = 3150 − 31.5Q; line 20 has no variance (0.5Q
// consumed against 0.5Q earned at standard); substitution −Q/100 × (600 + 200) = −8Q,
// because no hours are reported.
func lifecycleOrder(key, legalEntity string) (NewOrder, error) {
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: ids.New(), QtyPer: d("3"), ScrapPct: d("5"), UnitCost: d("10"), Flushing: domain.FlushStart},
		{LineNo: "20", ItemID: ids.New(), QtyPer: d("0.5"), UnitCost: d("4"), Flushing: domain.FlushFinish},
	}
	lines, err := domain.Explode(bom, d("100"))
	if err != nil {
		return NewOrder{}, err
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.2"), LaborRate: d("30"), OverheadRate: d("10")}}, d("100"))
	if err != nil {
		return NewOrder{}, err
	}
	date := "2026-11-30"
	return NewOrder{LegalEntityID: legalEntity, ProductID: ids.New(), SiteID: ids.New(), WarehouseID: ids.New(), Currency: "USD",
		Quantity: d("100"), DeliveryDate: &date, OriginType: "Manual", Lines: lines, Operations: ops, IdempotencyKey: key, RequestHash: "hash-" + key}, nil
}

// lcLegal is BR-21.1 written out again for the model (status × command → status).
var lcLegal = map[domain.Status]map[domain.Command]domain.Status{
	domain.Created:            {domain.CmdEstimate: domain.Estimated, domain.CmdCancel: domain.Cancelled},
	domain.Estimated:          {domain.CmdEstimate: domain.Estimated, domain.CmdSchedule: domain.Scheduled, domain.CmdCancel: domain.Cancelled, domain.CmdResetStatus: domain.Created},
	domain.Scheduled:          {domain.CmdEstimate: domain.Scheduled, domain.CmdRelease: domain.Released, domain.CmdCancel: domain.Cancelled, domain.CmdResetStatus: domain.Estimated},
	domain.Released:           {domain.CmdEstimate: domain.Released, domain.CmdStart: domain.Started, domain.CmdCancel: domain.Cancelled, domain.CmdResetStatus: domain.Scheduled},
	domain.Started:            {domain.CmdReportFinished: domain.ReportedAsFinished, domain.CmdEnd: domain.Ended, domain.CmdResetStatus: domain.Released},
	domain.ReportedAsFinished: {domain.CmdReportFinished: domain.ReportedAsFinished, domain.CmdEnd: domain.Ended, domain.CmdResetStatus: domain.Started},
	domain.Ended:              {},
	domain.Cancelled:          {},
}

var lcOrder = []domain.Status{domain.Created, domain.Estimated, domain.Scheduled, domain.Released, domain.Started, domain.ReportedAsFinished}

func lcRank(s domain.Status) int {
	for i, x := range lcOrder {
		if x == s {
			return i
		}
	}
	return -1
}

var (
	lcStatuses = []domain.Status{domain.Created, domain.Estimated, domain.Scheduled, domain.Released, domain.Started, domain.ReportedAsFinished, domain.Ended, domain.Cancelled}
	lcCommands = []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease, domain.CmdStart, domain.CmdReportFinished, domain.CmdEnd, domain.CmdCancel, domain.CmdResetStatus}
)

// lcModel tracks status, the quantity reported and the expected net per account.
type lcModel struct {
	status domain.Status
	q      decimal.Decimal
	nets   map[string]decimal.Decimal
}

func (m *lcModel) add(acct string, v decimal.Decimal) { m.nets[acct] = m.nets[acct].Add(v) }

type fataler interface {
	Fatalf(format string, args ...any)
}

// lcRunner drives one lifecycleOrder through the store and checks every
// command against the model.
type lcRunner struct {
	f       fataler
	s       *Postgres
	pool    *pgxpool.Pool
	ctx     context.Context
	orderID string
	version int
	n       int
	m       *lcModel
	counts  map[string]int
}

func newLCRunner(f fataler, s *Postgres, pool *pgxpool.Pool, ctx context.Context, key string, counts map[string]int) *lcRunner {
	le := ids.New()
	if _, _, err := s.SavePostingProfile(ctx, ProfileSave{LegalEntityID: le, Profile: testProfile(domain.AllKey, domain.AllKey, acctWIP), Actor: "admin"}); err != nil {
		f.Fatalf("profile: %v", err)
	}
	in, err := lifecycleOrder(key, le)
	if err != nil {
		f.Fatalf("order: %v", err)
	}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		f.Fatalf("create: %v", err)
	}
	return &lcRunner{f: f, s: s, pool: pool, ctx: ctx, orderID: o.ID, version: o.Version,
		m: &lcModel{status: domain.Created, q: decimal.Zero, nets: map[string]decimal.Decimal{}}, counts: counts}
}

// apply sends cmd (good for a report, duty for End, resetTo for a reset) and
// checks the outcome: a refusal with the model's code and nothing changed, or
// the model's status and exactly the model's change on every account.
func (r *lcRunner) apply(cmd domain.Command, good decimal.Decimal, duty bool, resetTo domain.Status) {
	m, f := r.m, r.f
	r.n++
	tr := Transition{OrderID: r.orderID, IfMatch: r.version, Command: cmd, Good: good, ZeroOutputDuty: duty, ResetTo: resetTo,
		Actor: "planner-1", TraceID: "trace-lc", IdempotencyKey: fmt.Sprintf("%s-%d", r.orderID, r.n), RequestHash: fmt.Sprintf("%s-%d", cmd, r.n)}

	// The model's verdict.
	to, legal := lcLegal[m.status][cmd]
	wantCode := domain.CodeIllegalTransition
	if cmd == domain.CmdResetStatus && resetTo != "" {
		fr, rr := lcRank(m.status), lcRank(resetTo)
		legal, to = fr > 0 && rr >= 0 && rr < fr, resetTo
	}
	if legal && cmd == domain.CmdReportFinished && m.q.Add(good).GreaterThan(d("100")) {
		legal, wantCode = false, domain.CodeRAFExceeds
	}
	if legal && cmd == domain.CmdEnd && m.status == domain.Started && !duty {
		legal, wantCode = false, domain.CodeZeroOutput
	}

	_, rowsBefore, err := accountNets(r.ctx, r.pool, r.orderID)
	if err != nil {
		f.Fatalf("%v", err)
	}
	got, _, err := r.s.Transition(r.ctx, tr)
	if !legal {
		r.counts["refused "+wantCode]++
		if _, code := problemStatus(err); code != wantCode {
			f.Fatalf("%s on %s (reset to %q, good %s, duty %v): want %s, got %v", cmd, m.status, resetTo, good, duty, wantCode, err)
		}
		nets, rowsAfter, err := accountNets(r.ctx, r.pool, r.orderID)
		if err != nil {
			f.Fatalf("%v", err)
		}
		cur, err := r.s.GetOrder(r.ctx, r.orderID)
		if err != nil {
			f.Fatalf("%v", err)
		}
		if rowsAfter != rowsBefore || !sameNets(m.nets, nets) || cur.Status != m.status || cur.Version != r.version || !cur.QtyRAF.Equal(m.q) {
			f.Fatalf("refused %s on %s changed the order: status %s v%d (want %s v%d), nets %v (want %v)",
				cmd, m.status, cur.Status, cur.Version, m.status, r.version, nets, m.nets)
		}
		return
	}
	if err != nil {
		f.Fatalf("legal %s on %s (reset to %q, good %s, duty %v) refused: %v", cmd, m.status, resetTo, good, duty, err)
	}
	r.counts[fmt.Sprintf("%s→%s", m.status, to)]++

	// Expected posting effect, from the hand-derived constants of lifecycleOrder.
	from := m.status
	switch {
	case cmd == domain.CmdStart:
		m.add(acctWIP, d("3150"))
		m.add(acctInv, d("-3150"))
	case cmd == domain.CmdReportFinished:
		m.add(acctWIP, good.Mul(d("2")).Sub(good.Mul(d("41.5"))))
		m.add(acctInv, good.Mul(d("-2")))
		m.add(acctFG, good.Mul(d("41.5")))
		m.q = m.q.Add(good)
	case cmd == domain.CmdEnd:
		wipBefore := d("3150").Sub(m.q.Mul(d("39.5")))
		if !m.nets[acctWIP].Equal(wipBefore) {
			f.Fatalf("model: WIP %s before End, hand formula %s", m.nets[acctWIP], wipBefore)
		}
		switch {
		case m.q.IsZero():
			r.counts["end zero"]++
		case m.q.LessThan(d("100")):
			r.counts["end partial"]++
		default:
			r.counts["end full"]++
		}
		m.add(acctWIP, wipBefore.Neg())
		m.add(acctVarQty, d("3150").Sub(m.q.Mul(d("31.5"))))
		m.add(acctVarSub, m.q.Mul(d("-8")))
	case cmd == domain.CmdResetStatus && from == domain.ReportedAsFinished && to == domain.Started:
		// The reports as finished are reversed: consumption and receipt come back.
		m.add(acctWIP, m.q.Mul(d("39.5")))
		m.add(acctInv, m.q.Mul(d("2")))
		m.add(acctFG, m.q.Mul(d("-41.5")))
		m.q = decimal.Zero
	case cmd == domain.CmdResetStatus && lcRank(from) >= lcRank(domain.Started) && lcRank(to) < lcRank(domain.Started):
		// Every journal is reversed: each account is back to zero.
		m.nets, m.q = map[string]decimal.Decimal{}, decimal.Zero
	}
	m.status = to

	if got.Status != to || !got.QtyRAF.Equal(m.q) {
		f.Fatalf("%s on %s: status %s qtyRAF %s, want %s %s", cmd, from, got.Status, got.QtyRAF, to, m.q)
	}
	r.version = got.Version
	nets, _, err := accountNets(r.ctx, r.pool, r.orderID)
	if err != nil {
		f.Fatalf("%v", err)
	}
	if !sameNets(m.nets, nets) {
		f.Fatalf("%s on %s posted %v, want %v", cmd, from, nets, m.nets)
	}
}

// reconcile checks that the events' ledger lines equal the subledger account by account.
func (r *lcRunner) reconcile() {
	ev, err := eventAccountNets(r.ctx, r.pool, r.orderID)
	if err != nil {
		r.f.Fatalf("%v", err)
	}
	if !sameNets(r.m.nets, ev) {
		r.f.Fatalf("the events' ledger lines %v do not reconcile to the subledger %v", ev, r.m.nets)
	}
}

type lcStep struct {
	cmd  domain.Command
	good string
	duty bool
	to   domain.Status
}

// lcPaths reach each status from Created with legal commands.
var lcPaths = map[domain.Status][]lcStep{
	domain.Created:            nil,
	domain.Estimated:          {{cmd: domain.CmdEstimate}},
	domain.Scheduled:          {{cmd: domain.CmdEstimate}, {cmd: domain.CmdSchedule}},
	domain.Released:           {{cmd: domain.CmdEstimate}, {cmd: domain.CmdSchedule}, {cmd: domain.CmdRelease}},
	domain.Started:            {{cmd: domain.CmdEstimate}, {cmd: domain.CmdSchedule}, {cmd: domain.CmdRelease}, {cmd: domain.CmdStart}},
	domain.ReportedAsFinished: {{cmd: domain.CmdEstimate}, {cmd: domain.CmdSchedule}, {cmd: domain.CmdRelease}, {cmd: domain.CmdStart}, {cmd: domain.CmdReportFinished, good: "40"}},
	domain.Ended: {{cmd: domain.CmdEstimate}, {cmd: domain.CmdSchedule}, {cmd: domain.CmdRelease}, {cmd: domain.CmdStart},
		{cmd: domain.CmdReportFinished, good: "40"}, {cmd: domain.CmdEnd}},
	domain.Cancelled: {{cmd: domain.CmdCancel}},
}

func (s lcStep) goodQty() decimal.Decimal {
	if s.good == "" {
		return decimal.Zero
	}
	return d(s.good)
}

// AC1 (exhaustive): every one of the 64 status × command pairs, through the
// database on a fresh order each. Report as finished is tried with 40, with 61
// (101 in all from ReportedAsFinished, beyond the order) and with the full 100;
// End with and without the zero-output duty; reset to the previous status and
// to each of the eight statuses. Every illegal command is refused with its
// code and changes nothing; every legal one posts exactly the hand-derived
// amounts of lifecycleOrder, and the events' ledger lines reconcile.
func TestLifecycle_EveryPairPostsTheExpectedEntries(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(98))
	counts := map[string]int{}
	n := 0
	for _, from := range lcStatuses {
		for _, cmd := range lcCommands {
			variants := []lcStep{{cmd: cmd}}
			switch cmd {
			case domain.CmdReportFinished:
				variants = []lcStep{{cmd: cmd, good: "40"}, {cmd: cmd, good: "61"}, {cmd: cmd, good: "100"}}
			case domain.CmdEnd:
				variants = []lcStep{{cmd: cmd}, {cmd: cmd, duty: true}}
			case domain.CmdResetStatus:
				variants = []lcStep{{cmd: cmd}}
				for _, to := range lcStatuses {
					variants = append(variants, lcStep{cmd: cmd, to: to})
				}
			}
			for _, v := range variants {
				n++
				t.Run(fmt.Sprintf("%s/%s%s%v/%s", from, cmd, v.good, v.duty, v.to), func(t *testing.T) {
					r := newLCRunner(t, s, pool, ctx, fmt.Sprintf("pair-%d", n), counts)
					for _, p := range lcPaths[from] {
						r.apply(p.cmd, p.goodQty(), p.duty, p.to)
					}
					if r.m.status != from {
						t.Fatalf("path reached %s, want %s", r.m.status, from)
					}
					r.apply(v.cmd, v.goodQty(), v.duty, v.to)
					r.reconcile()
				})
			}
		}
	}
	// Full output in two reports, a reset that reverses both, the full output
	// again in one report, End, and every command refused on the Ended order.
	t.Run("full output to End", func(t *testing.T) {
		r := newLCRunner(t, s, pool, ctx, "pair-full", counts)
		for _, p := range lcPaths[domain.Started] {
			r.apply(p.cmd, p.goodQty(), p.duty, p.to)
		}
		r.apply(domain.CmdReportFinished, d("60"), false, "")
		r.apply(domain.CmdReportFinished, d("40"), false, "")
		r.apply(domain.CmdReportFinished, d("1"), false, "") // 101 of 100
		r.apply(domain.CmdResetStatus, decimal.Zero, false, domain.Started)
		r.apply(domain.CmdReportFinished, d("100"), false, "")
		r.apply(domain.CmdEnd, decimal.Zero, false, "")
		for _, c := range lcCommands {
			r.apply(c, d("1"), true, domain.Created)
		}
		r.reconcile()
	})
	t.Logf("%d cases, outcomes %v", n+1, counts)
	for _, k := range []string{"refused " + domain.CodeRAFExceeds, "refused " + domain.CodeZeroOutput, "end partial", "end full", "end zero"} {
		if counts[k] == 0 {
			t.Errorf("no %q among the cases", k)
		}
	}
}

// AC1 (property): random command sequences against one order each, through
// the database, checked step by step by the same model, so the postings stay
// right however transitions, reports, resets and re-reports interleave.
func TestLifecycle_PostingsProperty(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(95))
	seq := 0
	counts := map[string]int{}
	rapid.Check(t, func(rt *rapid.T) {
		seq++
		r := newLCRunner(rt, s, pool, ctx, fmt.Sprintf("lc-%d", seq), counts)
		steps := rapid.IntRange(1, 40).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			// Uniform commands alone end most sequences at Cancelled before Started, so
			// six draws in ten take a legal command that keeps the order open, one a
			// legal End or Cancel, and three any of the eight commands.
			var cmd domain.Command
			var open, closing []domain.Command
			for _, c := range lcCommands {
				if _, ok := lcLegal[r.m.status][c]; ok {
					if c == domain.CmdCancel || c == domain.CmdEnd {
						closing = append(closing, c)
					} else {
						open = append(open, c)
					}
				}
			}
			switch k := rapid.IntRange(0, 9).Draw(rt, "kind"); {
			case k < 6 && len(open) > 0:
				cmd = rapid.SampledFrom(open).Draw(rt, "openCmd")
			case k == 6 && len(closing) > 0:
				cmd = rapid.SampledFrom(closing).Draw(rt, "closingCmd")
			default:
				cmd = rapid.SampledFrom(lcCommands).Draw(rt, "cmd")
			}
			good, duty, resetTo := decimal.Zero, false, domain.Status("")
			switch cmd {
			case domain.CmdReportFinished:
				good = decimal.NewFromInt(int64(rapid.SampledFrom([]int{7, 13, 25, 40, 60}).Draw(rt, "good")))
			case domain.CmdEnd:
				duty = rapid.Bool().Draw(rt, "zeroOutputDuty")
			case domain.CmdResetStatus:
				resetTo = rapid.SampledFrom(append([]domain.Status{""}, lcStatuses...)).Draw(rt, "resetTo")
			}
			r.apply(cmd, good, duty, resetTo)
		}
		r.reconcile()
		counts["final "+string(r.m.status)]++
	})
	t.Logf("sequences %d, outcomes %v", seq, counts)
}

// AC3 regression through the database: a RAF of 100 of an order of 150
// consumes the finish-flushed lines exactly. Hand-derived: line 10 is 2 per
// unit + 2% scrap → 306 required, RAF 100 consumes 2 × 100 × 1.02 = 204 (816.00
// at 4.00); line 20 is 2 per series of 3 + 20% scrap → 120 required, RAF 100
// consumes 2 × 100 ÷ 3 × 1.2 = 80. Before the fix the rounded share
// 100 ÷ 150 = 0.6666666666666667 consumed 204.000001 and 80.000001.
func TestJournals_BackflushRAF100Of150IsExact(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(96))
	in := sampleOrder(t, "key-raf-150", ids.New())
	bom := []domain.BOMComponent{
		{LineNo: "10", ItemID: ids.New(), QtyPer: d("2"), ScrapPct: d("2"), UnitCost: d("4"), Flushing: domain.FlushFinish},
		{LineNo: "20", ItemID: ids.New(), QtyPer: d("2"), SeriesSize: d("3"), ScrapPct: d("20"), UnitCost: d("1"), Flushing: domain.FlushFinish},
	}
	lines, err := domain.Explode(bom, d("150"))
	if err != nil {
		t.Fatal(err)
	}
	ops, err := domain.PlanOperations([]domain.Operation{{OperNumber: 10, ResourceID: ids.New(), RunHPer: d("0.1"), LaborRate: d("30")}}, d("150"))
	if err != nil {
		t.Fatal(err)
	}
	in.Quantity, in.Lines, in.Operations = d("150"), lines, ops
	st := startedOrder(t, s, ctx, in)
	if o := st.refresh(); !o.Lines[0].QtyRequired.Equal(d("306")) || !o.Lines[1].QtyRequired.Equal(d("120")) {
		t.Fatalf("required: %s %s", o.Lines[0].QtyRequired, o.Lines[1].QtyRequired)
	}

	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	o := st.refresh()
	if !o.Lines[0].QtyConsumed.Equal(d("204")) || !o.Lines[1].QtyConsumed.Equal(d("80")) {
		t.Fatalf("after RAF 100 of 150: consumed %s and %s, want 204 and 80", o.Lines[0].QtyConsumed, o.Lines[1].QtyConsumed)
	}
	js, err := s.ListJournals(ctx, st.orderID)
	if err != nil || len(js) != 1 {
		t.Fatalf("journals: %+v %v", js, err)
	}
	var issued decimal.Decimal
	for _, l := range js[0].Lines {
		if l.Kind == KindIssue {
			issued = issued.Add(l.Amount)
		}
	}
	if !issued.Equal(d("896")) { // 204 × 4.00 + 80 × 1.00
		t.Fatalf("issue value %s, want 896.00", issued)
	}
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("50") })
	if o = st.refresh(); !o.Lines[0].QtyConsumed.Equal(d("306")) || !o.Lines[1].QtyConsumed.Equal(d("120")) {
		t.Fatalf("after RAF 150 of 150: consumed %s and %s", o.Lines[0].QtyConsumed, o.Lines[1].QtyConsumed)
	}
}

// AC4: End after a partial RAF (60 of 100). Hand-derived on the BR-21.7 order
// (planned 3000 + 1500 + 500 = 5000.00, 50.00 a unit): picking 203 + 102 kg
// at 10.20 = 3111.00, 46 h = 1380.00 labor + 460.00 overhead, RAF 60 receives
// 3000.00, so 1951.00 stays in WIP. That balance is the same on the cost read,
// on the subledger's WIP account and in the ledger lines of the journal.posted
// events (what the GL books). End then clears exactly 1951.00 off the WIP
// account: quantity variances (305 − 180) × 10 + (46 − 30) × 30 + (46 − 30) × 10
// = 1890.00 and the price variance 3111 − 305 × 10 = 61.00, and the WIP account
// nets to zero in the subledger and in the events alike.
func TestJournals_PartialRAFEndReconcilesTheWIPAccount(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(97))
	st := startedOrder(t, s, ctx, sampleOrder(t, "key-ac4", ids.New()))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: qty("10.20")}, {LineNo: "20.1", Qty: d("102"), UnitCost: qty("10.20")}}}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("60")}}}).ID)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })

	before := map[string]decimal.Decimal{acctWIP: d("1951"), acctInv: d("-3111"), acctLabor: d("-1380"), acctOverhead: d("-460"), acctFG: d("3000")}
	if c := st.costs(); !c.WIPBalance.Equal(d("1951")) {
		t.Fatalf("cost read WIP %s, want 1951.00", c.WIPBalance)
	}
	sub, _, err := accountNets(ctx, pool, st.orderID)
	if err != nil || !sameNets(before, sub) {
		t.Fatalf("subledger after RAF 60: %v %v", sub, err)
	}
	ev, err := eventAccountNets(ctx, pool, st.orderID)
	if err != nil || !sameNets(before, ev) {
		t.Fatalf("event ledger lines after RAF 60: %v %v", ev, err)
	}

	if ended := st.must(domain.CmdEnd, nil); ended.Status != domain.Ended {
		t.Fatalf("end: %s", ended.Status)
	}
	after := map[string]decimal.Decimal{acctInv: d("-3111"), acctLabor: d("-1380"), acctOverhead: d("-460"), acctFG: d("3000"),
		acctVarQty: d("1890"), acctVarPrice: d("61")}
	if sub, _, err = accountNets(ctx, pool, st.orderID); err != nil || !sameNets(after, sub) {
		t.Fatalf("subledger after End: %v %v", sub, err)
	}
	if ev, err = eventAccountNets(ctx, pool, st.orderID); err != nil || !sameNets(after, ev) {
		t.Fatalf("event ledger lines after End: %v %v", ev, err)
	}
	var settled struct {
		Data prodevents.OrderSettledData `json:"data"`
	}
	var raw json.RawMessage
	if err := pool.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, st.orderID, prodevents.OrderSettled).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &settled); err != nil || !d(settled.Data.WIPCleared).Equal(d("1951")) {
		t.Fatalf("order.settled WIPCleared: %s %v", raw, err)
	}
	wipCredit := decimal.Zero
	for _, p := range settled.Data.Postings {
		if p.Account == acctWIP {
			wipCredit = wipCredit.Add(d(p.Credit)).Sub(d(p.Debit))
		}
	}
	if !wipCredit.Equal(d("1951")) {
		t.Fatalf("settlement credits the WIP account %s, want 1951.00", wipCredit)
	}
	_, err = st.step(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("1") })
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)
}
