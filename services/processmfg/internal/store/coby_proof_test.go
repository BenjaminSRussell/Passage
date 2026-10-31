package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

type cobyProduct struct {
	StandardCost string `json:"standardCost"`
	RafQty       string `json:"rafQty"`
	Percent      string `json:"percent"`
}

type cobyShare struct {
	Amount  string `json:"amount"`
	Percent string `json:"percent"`
}

// Wave 24 proof, end to end: every case of pmf-coby-adv-23 (expected shares
// from the independent testdata/gen_pmf_coby_adv.py) is run as a batch order
// that is released, started, reported with the case's output quantities and
// ended with its cost and method. The stored shares must match the reference
// and add up to the batch cost exactly in the database; a refused case writes
// no share and leaves the order reported.
func TestEndBatchOrder_CobyAdversarialFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/pmf-coby-adv-23.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string        `json:"name"`
		Currency   string        `json:"currency"`
		BatchCost  string        `json:"batchCost"`
		Method     string        `json:"method"`
		Main       cobyProduct   `json:"main"`
		CoProducts []cobyProduct `json:"coProducts"`
		ByProducts []struct {
			CostType string `json:"costType"`
			Qty      string `json:"qty"`
			Rate     string `json:"rate"`
		} `json:"byProducts"`
		ExpectedCode string `json:"expectedCode"`
		Expected     *struct {
			Main       cobyShare   `json:"main"`
			CoProducts []cobyShare `json:"coProducts"`
			Burdens    []string    `json:"burdens"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) != 23 {
		t.Fatalf("pmf-coby-adv-23: %d cases, %v", len(cases), err)
	}
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	for n, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			f := store.Formula{Code: fmt.Sprintf("COBY-%02d", n), Name: c.Name, ItemID: ids.New(), Size: dec("1"), Unit: "kg",
				Lines: []store.FormulaLine{{LineNumber: 1, ItemID: ids.New(), Type: domain.LineIngredient, Qty: dec("1"), Scalable: true}}}
			outputs := map[int]decimal.Decimal{}
			end := store.EndInput{Method: domain.AllocationMethod(c.Method), Currency: c.Currency, BatchCost: dec(c.BatchCost),
				MainBasis: domain.CostBasis{StandardCost: dec(c.Main.StandardCost), Percent: dec(c.Main.Percent)},
				CoBasis:   map[int]domain.CostBasis{}, ByRates: map[int]domain.ByRate{}}
			for i, p := range c.CoProducts {
				ln := 10 + i
				f.Lines = append(f.Lines, store.FormulaLine{LineNumber: ln, ItemID: ids.New(), Type: domain.LineCoProduct, Qty: dec("1"), Scalable: true})
				outputs[ln] = dec(p.RafQty)
				end.CoBasis[ln] = domain.CostBasis{StandardCost: dec(p.StandardCost), Percent: dec(p.Percent)}
			}
			for i, b := range c.ByProducts {
				ln := 20 + i
				f.Lines = append(f.Lines, store.FormulaLine{LineNumber: ln, ItemID: ids.New(), Type: domain.LineByProduct, Qty: dec("1"), Scalable: true})
				outputs[ln] = dec(b.Qty)
				end.ByRates[ln] = domain.ByRate{CostType: b.CostType, Rate: dec(b.Rate)}
			}
			formula, _, err := s.CreateFormula(ctx, f, store.Idem{})
			if err != nil {
				t.Fatal(err)
			}
			o, _, err := s.CreateBatchOrder(ctx, store.NewBatchOrder{LegalEntityID: entityA, FormulaID: formula.ID, BatchSize: dec("1")}, store.Idem{})
			if err != nil {
				t.Fatal(err)
			}
			o = advance(t, s, o, domain.CmdRelease)
			o = advance(t, s, o, domain.CmdStart)
			rep, _, err := s.ReportBatchOrder(ctx, o.ID, store.ReportInput{MainQty: dec(c.Main.RafQty), Outputs: outputs}, o.Version, store.Idem{})
			if err != nil {
				t.Fatal(err)
			}
			ended, _, err := s.EndBatchOrder(ctx, o.ID, end, rep.Version, store.Idem{})
			var stored int
			if qerr := admin.QueryRow(context.Background(), `select count(*) from batch_order_allocation where order_id = $1`, o.ID).Scan(&stored); qerr != nil {
				t.Fatal(qerr)
			}
			if c.ExpectedCode != "" {
				got, _ := s.GetBatchOrder(ctx, o.ID)
				if problemCode(err) != c.ExpectedCode || stored != 0 || got.Status != "reported" {
					t.Fatalf("err %v, %d shares stored, status %s; want %s, none, reported", err, stored, got.Status, c.ExpectedCode)
				}
				return
			}
			if err != nil || ended.Allocation == nil {
				t.Fatalf("end: %v", err)
			}
			want := c.Expected
			sh := ended.Allocation.Shares
			if len(sh) != 1+len(want.CoProducts)+len(want.Burdens) || stored != len(sh) {
				t.Fatalf("%d shares (%d stored), want %d", len(sh), stored, 1+len(want.CoProducts)+len(want.Burdens))
			}
			check := func(i int, role, amount, percent string) {
				if sh[i].Role != role || !sh[i].Amount.Equal(dec(amount)) || (percent != "" && !sh[i].Percent.Equal(dec(percent))) {
					t.Errorf("share %d = %s %s / %s%%, want %s %s / %s%%", i, sh[i].Role, sh[i].Amount, sh[i].Percent, role, amount, percent)
				}
			}
			check(0, "main", want.Main.Amount, want.Main.Percent)
			for i, w := range want.CoProducts {
				check(1+i, "co-product", w.Amount, w.Percent)
			}
			for i, w := range want.Burdens {
				check(1+len(want.CoProducts)+i, "by-product-burden", w, "")
			}
			var sum string
			if err := admin.QueryRow(context.Background(), `select sum(amount)::text from batch_order_allocation where order_id = $1`, o.ID).Scan(&sum); err != nil || !dec(sum).Equal(dec(c.BatchCost)) {
				t.Fatalf("stored shares sum to %s, batch cost %s (%v)", sum, c.BatchCost, err)
			}
		})
	}
}
