package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	eamevents "github.com/BenjaminSRussell/passage/services/eam/internal/events"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// TestWorkOrderCost50_ReconcilesAssetKPIAndEvents covers the asset side of
// AC-44.3 / ticket Proof 2 with testdata/eam-wo-cost-50 (gen_wo_cost_50.py,
// python3 -I, decimal): 50 work orders on 5 assets with 94 labor lines and
// parts values are booked and completed through the store. Every order's
// parts and labor, every asset's cost history (asset detail), the cost KPI
// over a completion window, and the sum of the workorder.completed events (the
// feed a ledger posting would consume) must equal the reference to the cent and
// reconcile with each other. Order 1 is AC-44.3: 350.00 + 8 h x 45.00 = 710.00.
// The same 50 orders are then booked in a fake general ledger (FR-44.11): the
// maintenance expense accounts must net to the asset cost history and to the
// reference, and the GL reconciliation must show no difference. Parts are
// written on the order as the wave-16 issue would, since part issue is not built.
func TestWorkOrderCost50_ReconcilesAssetKPIAndEvents(t *testing.T) {
	var fx struct {
		Assets []string `json:"assets"`
		Orders []struct {
			Asset       string `json:"asset"`
			CompletedOn string `json:"completedOn"`
			Parts       string `json:"parts"`
			Labor       []struct {
				Hours string `json:"hours"`
				Rate  string `json:"rate"`
			} `json:"labor"`
		} `json:"orders"`
	}
	var want struct {
		Window struct {
			From time.Time `json:"from"`
			To   time.Time `json:"to"`
		} `json:"window"`
		Orders []struct {
			Parts, Labor, Total string
		} `json:"orders"`
		Assets map[string]struct {
			Parts        string `json:"parts"`
			Labor        string `json:"labor"`
			WindowParts  string `json:"windowParts"`
			WindowLabor  string `json:"windowLabor"`
			WindowOrders int    `json:"windowOrders"`
		} `json:"assets"`
		GrandTotal string `json:"grandTotal"`
	}
	readJSON(t, "../../testdata/eam-wo-cost-50/fixture.json", &fx)
	readJSON(t, "../../testdata/eam-wo-cost-50/expected.json", &want)

	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "eam.work.plan", "eam.work-order.execute")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(pool, func() time.Time { return time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC) })
	dec := func(v string) decimal.Decimal {
		d, err := decimal.NewFromString(v)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	assets := map[string]string{}
	names := map[string]string{}
	for _, name := range fx.Assets {
		a, err := s.CreateAsset(ctx, Asset{Name: name, AssetType: strings.SplitN(name, "-", 2)[0], SiteID: testkit.TenantID(9), Criticality: 3}, "asset-"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
			t.Fatal(err)
		}
		assets[name], names[a.ID] = a.ID, name
	}

	for i, o := range fx.Orders {
		k := fmt.Sprint(i + 1)
		wo, err := s.CreateWorkOrder(ctx, WorkOrder{AssetID: assets[o.Asset], Type: domain.TypeInspection, Priority: 3, Currency: "USD"}, "wo-"+k)
		if err != nil {
			t.Fatal(err)
		}
		for j, l := range o.Labor {
			if wo, err = s.RecordLabor(ctx, wo.ID, dec(l.Hours), dec(l.Rate), nil, fmt.Sprintf("labor-%s-%d", k, j)); err != nil {
				t.Fatal(err)
			}
		}
		// Parts issue is wave 16 (not built): write the issued value on the order.
		if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `update work_order set parts_cost = $2::numeric where id = $1`, wo.ID, o.Parts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		v := wo.Version
		for _, st := range []domain.Status{domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress} {
			got, err := s.TransitionWorkOrder(ctx, wo.ID, Transition{To: st, Version: v}, "")
			if err != nil {
				t.Fatal(err)
			}
			v = got.Version
		}
		day, err := time.Parse("2006-01-02", o.CompletedOn)
		if err != nil {
			t.Fatal(err)
		}
		begin, end := day.Add(8*time.Hour), day.Add(16*time.Hour)
		done, err := s.TransitionWorkOrder(ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v, ActualStart: &begin, ActualEnd: &end}, "")
		if err != nil {
			t.Fatal(err)
		}
		e := want.Orders[i]
		if !dec(done.PartsCost).Equal(dec(e.Parts)) || !dec(done.LaborCost).Equal(dec(e.Labor)) {
			t.Errorf("order %d: parts %s labor %s, want %s and %s", i+1, done.PartsCost, done.LaborCost, e.Parts, e.Labor)
		}
		if i == 0 && !dec(done.PartsCost).Add(dec(done.LaborCost)).Equal(dec("710.00")) {
			t.Errorf("AC-44.3: order total = %s + %s, want 710.00", done.PartsCost, done.LaborCost)
		}
	}

	// The workorder.completed events, summed per asset.
	events := map[string]decimal.Decimal{}
	eventCount := 0
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select payload->'data'->>'assetId', payload->'data'->>'partsCost', payload->'data'->>'laborCost'
		  from outbox where type = $1`, eamevents.WorkOrderCompleted)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var asset, parts, labor string
			if err := rows.Scan(&asset, &parts, &labor); err != nil {
				return err
			}
			events[asset] = events[asset].Add(dec(parts)).Add(dec(labor))
			eventCount++
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if eventCount != len(fx.Orders) {
		t.Fatalf("workorder.completed events = %d, want %d", eventCount, len(fx.Orders))
	}

	kpi, err := s.CostByAsset(ctx, CostFilter{From: want.Window.From, To: want.Window.To})
	if err != nil {
		t.Fatal(err)
	}
	byAsset := map[string]AssetCost{}
	for _, c := range kpi {
		byAsset[names[c.AssetID]] = c
	}
	grand := decimal.Zero
	for _, name := range fx.Assets {
		e := want.Assets[name]
		d, err := s.GetAsset(ctx, assets[name])
		if err != nil {
			t.Fatal(err)
		}
		history := dec(d.PartsCost).Add(dec(d.LaborCost))
		grand = grand.Add(history)
		if !dec(d.PartsCost).Equal(dec(e.Parts)) || !dec(d.LaborCost).Equal(dec(e.Labor)) {
			t.Errorf("%s asset cost history: parts %s labor %s, want %s and %s", name, d.PartsCost, d.LaborCost, e.Parts, e.Labor)
		}
		if !events[assets[name]].Equal(history) {
			t.Errorf("%s: workorder.completed events total %s, asset cost history %s", name, events[assets[name]], history)
		}
		c := byAsset[name]
		if c.WorkOrders != e.WindowOrders || c.PartsCost != e.WindowParts || c.LaborCost != e.WindowLabor ||
			c.Total != dec(e.WindowParts).Add(dec(e.WindowLabor)).StringFixed(2) {
			t.Errorf("%s cost KPI: %d orders parts %s labor %s total %s, want %d %s %s", name, c.WorkOrders, c.PartsCost, c.LaborCost, c.Total,
				e.WindowOrders, e.WindowParts, e.WindowLabor)
		}
	}
	if !grand.Equal(dec(want.GrandTotal)) {
		t.Fatalf("grand total %s, want %s", grand, want.GrandTotal)
	}

	fake, ledgerID := ledgerclient.NewFake(), ids.New()
	le := "00000000-0000-0000-0000-0000000000e1"
	if _, _, err := s.SavePostingProfile(ctx, PostingProfile{LegalEntityID: le, WorkOrderType: "ALL", PartsExpense: "6100", LaborExpense: "6110", PartsOffset: "1400", LaborOffset: "5900"}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	wantParts, wantLabor, costed := decimal.Zero, decimal.Zero, 0
	for _, o := range want.Orders {
		wantParts, wantLabor = wantParts.Add(dec(o.Parts)), wantLabor.Add(dec(o.Labor))
		if !dec(o.Parts).Add(dec(o.Labor)).IsZero() {
			costed++
		}
	}
	booked, err := s.PostWorkOrderCosts(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"})
	if err != nil || len(booked.Failed) != 0 || len(booked.Booked) != costed || booked.More || costed != len(fx.Orders)-1 {
		t.Fatalf("booking: %d of %d costed orders, failed %v, more %v, %v", len(booked.Booked), costed, booked.Failed, booked.More, err)
	}
	for acct, net := range map[string]decimal.Decimal{"6100": wantParts, "6110": wantLabor, "1400": wantParts.Neg(), "5900": wantLabor.Neg()} {
		if b, _ := fake.Balance(ctx, ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: acct}); !b.Net().Equal(net) {
			t.Errorf("GL account %s nets %s, want %s", acct, b.Net(), net)
		}
	}
	if gl := wantParts.Add(wantLabor); !gl.Equal(grand) {
		t.Errorf("GL maintenance expense %s, asset cost history %s", gl, grand)
	}
	rec, err := s.ReconcileGL(ctx, fake, GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"})
	if err != nil || !rec.Reconciled || len(rec.Accounts) != 4 {
		t.Fatalf("GL reconciliation: %+v %v", rec, err)
	}
	if again, err := s.PostWorkOrderCosts(ctx, fake, GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, Currency: "USD"}); err != nil || len(again.Booked) != 0 || len(fake.Entries()) != costed {
		t.Fatalf("a second run books nothing: %+v %v", again, err)
	}

	t.Logf("eam-wo-cost-50: 50 orders, %d events; asset histories, KPI window and events reconcile to %s", eventCount, grand.StringFixed(2))
}
