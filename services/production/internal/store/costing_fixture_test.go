package store

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

type costingFixture struct {
	Summary map[string]int `json:"summary"`
	Orders  []struct {
		Name     string `json:"name"`
		OrderQty string `json:"orderQty"`
		RAFQty   string `json:"rafQty"`
		Lines    []struct {
			LineNo           string `json:"lineNo"`
			Item             string `json:"item"`
			QtyPer           string `json:"qtyPer"`
			Series           string `json:"series"`
			Scrap            string `json:"scrap"`
			RoundUp          bool   `json:"roundUp"`
			UnitCost         string `json:"unitCost"`
			ExpectedRequired string `json:"expectedRequired"`
		} `json:"lines"`
		Operations []struct {
			Oper               int    `json:"oper"`
			Resource           string `json:"resource"`
			Setup              string `json:"setup"`
			Run                string `json:"run"`
			Scrap              string `json:"scrap"`
			Labor              string `json:"labor"`
			Overhead           string `json:"overhead"`
			ExpectedProcessQty string `json:"expectedProcessQty"`
		} `json:"operations"`
		Picks []struct {
			LineNo         string  `json:"lineNo"`
			Qty            string  `json:"qty"`
			UnitCost       *string `json:"unitCost"`
			ExpectedAmount string  `json:"expectedAmount"`
		} `json:"picks"`
		Hours []struct {
			Oper             int    `json:"oper"`
			Hours            string `json:"hours"`
			ExpectedLabor    string `json:"expectedLabor"`
			ExpectedOverhead string `json:"expectedOverhead"`
		} `json:"hours"`
		Expected struct {
			PlannedByGroup map[string]string `json:"plannedByGroup"`
			Receipt        string            `json:"receipt"`
			WIPBefore      string            `json:"wipBefore"`
			Accounts       map[string]string `json:"accounts"`
			VariancesExact map[string]string `json:"variancesExact"`
		} `json:"expected"`
	} `json:"orders"`
}

// AC2 through the database: 50 orders with known consumptions
// (testdata/costing-50.json) are created from their BOM and route, started,
// picked, fed back, reported as finished (25 fully, 16 partly, 9 not at all)
// and ended. testdata/gen_costing_50.py, an exact-fraction reference that
// shares no code with the service, states the requirement of every line, the
// process quantity of every operation, the planned cost by group, the value of
// every issue and hours line, the RAF receipt, the WIP balance before End and
// the BR-21.6 variances. Every one is checked against what the service posted:
// all amounts exactly, except each variance row, which the service rounds to
// the cent and whose rounding residual it places on the largest row, so a row
// may sit up to half a cent per variance row away from the exact value; the
// variances must still clear the WIP balance to the cent and leave the WIP
// account at zero.
func TestSettle_Costing50ThroughTheDatabase(t *testing.T) {
	raw, err := os.ReadFile("testdata/costing-50.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx costingFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Orders) != 50 {
		t.Fatalf("want 50 fixture orders, got %d", len(fx.Orders))
	}
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(99))
	maxDev := decimal.Zero
	exactRows, rows := 0, 0
	for n, fo := range fx.Orders {
		t.Run(fo.Name, func(t *testing.T) {
			orderQty := d(fo.OrderQty)
			names := map[string]string{}
			id := func(name string) string {
				if names[name] == "" {
					names[name] = ids.New()
				}
				return names[name]
			}
			var bom []domain.BOMComponent
			for _, l := range fo.Lines {
				bom = append(bom, domain.BOMComponent{LineNo: l.LineNo, ItemID: id(l.Item), QtyPer: d(l.QtyPer), SeriesSize: d(l.Series),
					ScrapPct: d(l.Scrap), RoundUp: l.RoundUp, UnitCost: d(l.UnitCost)})
			}
			lines, err := domain.Explode(bom, orderQty)
			if err != nil {
				t.Fatal(err)
			}
			for i, l := range fo.Lines {
				if !lines[i].QtyRequired.Equal(d(l.ExpectedRequired)) {
					t.Errorf("line %s required %s, reference %s", l.LineNo, lines[i].QtyRequired, l.ExpectedRequired)
				}
			}
			var route []domain.Operation
			for _, o := range fo.Operations {
				route = append(route, domain.Operation{OperNumber: o.Oper, ResourceID: id(o.Resource), SetupH: d(o.Setup), RunHPer: d(o.Run),
					ScrapPct: d(o.Scrap), LaborRate: d(o.Labor), OverheadRate: d(o.Overhead)})
			}
			ops, err := domain.PlanOperations(route, orderQty)
			if err != nil {
				t.Fatal(err)
			}
			for i, o := range fo.Operations {
				if !ops[i].ProcessQty.Equal(d(o.ExpectedProcessQty)) {
					t.Errorf("operation %d process qty %s, reference %s", o.Oper, ops[i].ProcessQty, o.ExpectedProcessQty)
				}
			}
			in := sampleOrder(t, fmt.Sprintf("costing-%d", n), ids.New())
			in.Quantity, in.Lines, in.Operations = orderQty, lines, ops
			st := startedOrder(t, s, ctx, in)

			c := st.costs()
			for g, want := range fo.Expected.PlannedByGroup {
				if got := groupSum(c.Planned, domain.CostGroup(g)); !got.Equal(d(want)) {
					t.Errorf("planned %s %s, reference %s", g, got, want)
				}
			}

			if len(fo.Picks) > 0 {
				var issues []IssueInput
				for _, p := range fo.Picks {
					in := IssueInput{LineNo: p.LineNo, Qty: d(p.Qty)}
					if p.UnitCost != nil {
						in.UnitCost = qty(*p.UnitCost)
					}
					issues = append(issues, in)
				}
				pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: issues})
				posted, err := st.post(pick.ID, func(r *PostRequest) { r.OverConsumptionDuty = true })
				if err != nil {
					t.Fatal(err)
				}
				for i, p := range fo.Picks {
					if !posted.Lines[i].Amount.Equal(d(p.ExpectedAmount)) {
						t.Errorf("issue line %s amount %s, reference %s", p.LineNo, posted.Lines[i].Amount, p.ExpectedAmount)
					}
				}
			}
			if len(fo.Hours) > 0 {
				var hours []HoursInput
				for _, h := range fo.Hours {
					hours = append(hours, HoursInput{OperNumber: h.Oper, Hours: d(h.Hours)})
				}
				posted := st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: hours}).ID)
				for i, h := range fo.Hours {
					if !posted.Lines[i].Amount.Equal(d(h.ExpectedLabor)) || !posted.Lines[i].OverheadAmount.Equal(d(h.ExpectedOverhead)) {
						t.Errorf("operation %d labor %s overhead %s, reference %s %s", h.Oper, posted.Lines[i].Amount, posted.Lines[i].OverheadAmount,
							h.ExpectedLabor, h.ExpectedOverhead)
					}
				}
			}
			raf := d(fo.RAFQty)
			if raf.IsPositive() {
				st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = raf })
			}
			if c = st.costs(); !c.WIPBalance.Equal(d(fo.Expected.WIPBefore)) {
				t.Errorf("WIP before End %s, reference %s", c.WIPBalance, fo.Expected.WIPBefore)
			}
			st.must(domain.CmdEnd, func(tr *Transition) { tr.ZeroOutputDuty = raf.IsZero() })

			nets, _, err := accountNets(ctx, pool, st.orderID)
			if err != nil {
				t.Fatal(err)
			}
			for acct, want := range fo.Expected.Accounts {
				if !nets[acct].Equal(d(want)) {
					t.Errorf("account %s nets %s, reference %s", acct, nets[acct], want)
				}
			}
			if !nets[acctWIP].IsZero() {
				t.Errorf("WIP account %s after End", nets[acctWIP])
			}
			c = st.costs()
			slack := decimal.New(int64(len(fo.Expected.VariancesExact)+1), -2).Div(decimal.NewFromInt(2))
			got, total := map[string]decimal.Decimal{}, decimal.Zero
			for _, v := range c.Variances {
				got[string(v.Type)+"/"+string(v.Group)] = v.Amount
				total = total.Add(v.Amount)
			}
			if !total.Equal(d(fo.Expected.WIPBefore)) {
				t.Errorf("variances total %s, reference WIP %s", total, fo.Expected.WIPBefore)
			}
			// The variance accounts carry exactly the variance rows.
			byAcct := map[string]decimal.Decimal{}
			p := testProfile(domain.AllKey, domain.AllKey, acctWIP)
			for _, v := range c.Variances {
				acct := map[domain.VarianceType]string{domain.VarLotSize: p.VarLotSize, domain.VarQuantity: p.VarQuantity,
					domain.VarPrice: p.VarPrice, domain.VarSubstitution: p.VarSubstitution}[v.Type]
				byAcct[acct] = byAcct[acct].Add(v.Amount)
			}
			for _, acct := range []string{p.VarLotSize, p.VarQuantity, p.VarPrice, p.VarSubstitution} {
				if !nets[acct].Equal(byAcct[acct]) {
					t.Errorf("account %s nets %s, variance rows %s", acct, nets[acct], byAcct[acct])
				}
			}
			for key, exact := range fo.Expected.VariancesExact {
				rows++
				dev := got[key].Sub(d(exact)).Abs()
				if dev.GreaterThan(maxDev) {
					maxDev = dev
				}
				if dev.LessThan(d("0.005")) || dev.Equal(d("0.005")) {
					exactRows++
				}
				if dev.GreaterThan(slack) {
					t.Errorf("%s: %s, reference %s", key, got[key], exact)
				}
				delete(got, key)
			}
			for key, v := range got {
				if v.Abs().GreaterThan(slack) {
					t.Errorf("unexpected variance %s = %s", key, v)
				}
			}
		})
	}
	t.Logf("fixture mix %v; %d variance rows, %d within half a cent of the exact value, largest deviation %s", fx.Summary, rows, exactRows, maxDev)
}
