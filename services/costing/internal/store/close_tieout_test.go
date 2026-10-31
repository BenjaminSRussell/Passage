package store

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

const million = "PASSAGE_COSTING_1M"

type closeRun struct {
	txns    int
	elapsed time.Duration
}

func settleAndTieOut(t *testing.T, products, txnsPerProduct int) closeRun {
	t.Helper()
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(20))
	le, site := ids.New(), ids.New()
	rng := rand.New(rand.NewSource(20))
	start := time.Now()
	var gl []domain.GLBalance
	var rows []CostTransaction
	flush := func() {
		for len(rows) > 0 {
			n := min(len(rows), MaxCostTransactionBatch)
			if _, err := s.RecordCostTransactions(ctx, rows[:n]); err != nil {
				t.Fatal(err)
			}
			rows = rows[n:]
		}
	}
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for p := 0; p < products; p++ {
		product := ids.New()
		txns, onHand := make([]domain.Txn, 0, txnsPerProduct), 0
		for k := 0; k < txnsPerProduct; k++ {
			id := fmt.Sprintf("T%07d", k)
			date := day.AddDate(0, 0, k)
			if onHand == 0 || rng.Intn(100) < 55 {
				q := 1 + rng.Intn(100)
				value := decimal.New(int64(q*(100+rng.Intn(9900))+rng.Intn(100)), -2)
				txns = append(txns, domain.Txn{ID: id, Kind: domain.Receipt, Qty: decimal.NewFromInt(int64(q)), Value: value, FinancialDate: date})
				onHand += q
				continue
			}
			q := 1 + rng.Intn(onHand)
			txns = append(txns, domain.Txn{ID: id, Kind: domain.Issue, Qty: decimal.NewFromInt(int64(q)), FinancialDate: date})
			onHand -= q
		}
		res, err := domain.Settle(txns, domain.Options{Method: domain.FIFO, Scale: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Exceptions) != 0 {
			t.Fatalf("product %d: unexpected exceptions %+v", p, res.Exceptions)
		}
		ledger := decimal.Zero
		for _, x := range txns {
			qty, amount := x.Qty, x.Value
			if x.Kind == domain.Issue {
				qty, amount = x.Qty.Neg(), res.IssueCosts[x.ID].Neg()
			}
			ledger = ledger.Add(amount)
			rows = append(rows, costRow(product+"/"+x.ID, le, product, site, domain.KindFinancial, qty.String(), amount.String(), x.FinancialDate.Format("2006-01-02")))
		}
		if !ledger.Equal(res.RemainingValue) {
			t.Fatalf("product %d: ledger %s differs from the settled remaining value %s", p, ledger, res.RemainingValue)
		}
		gl = append(gl, domain.GLBalance{Dims: map[string]string{"product": product}, Balance: ledger})
		if len(rows) >= MaxCostTransactionBatch*20 {
			flush()
		}
	}
	flush()
	bal, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2099-12-31"})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := domain.BuildValueReport(bal, []string{"product"})
	if err != nil {
		t.Fatal(err)
	}
	tie, err := domain.TieOutReport(rep, gl)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if !tie.Ties() || len(tie.Rows) != products {
		t.Fatalf("tie-out: %d groups, %d differences, total diff %s; want %d groups and none", len(tie.Rows), tie.Differences, tie.TotalDiff, products)
	}
	return closeRun{txns: products * txnsPerProduct, elapsed: elapsed}
}

func TestPostgres_SettledPostingsTieToTheLedger(t *testing.T) {
	run := settleAndTieOut(t, 20, 60)
	if run.txns != 1200 {
		t.Fatalf("settled %d transactions, want 1200", run.txns)
	}
}

func TestPostgres_CloseOfOneMillionTransactions(t *testing.T) {
	if os.Getenv(million) == "" {
		t.Skip("set " + million + "=1 to run the 1M-transaction close")
	}
	run := settleAndTieOut(t, 1000, 1000)
	t.Logf("settled, posted and tied out %d transactions in %s", run.txns, run.elapsed)
	if run.elapsed >= 20*time.Minute {
		t.Fatalf("close took %s, want under 20m", run.elapsed)
	}
}
