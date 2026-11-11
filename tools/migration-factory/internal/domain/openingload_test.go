package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestBuildStockItemMapsTheItemAndRoundsTheUnitCost(t *testing.T) {
	line := OnhandLine{LegacyID: "OH-1", Item: "BOLT", Site: "S1", Warehouse: "W1", Location: "A-01", Unit: "EA", Currency: "USD",
		Quantity: decimal.RequireFromString("3"), Value: decimal.RequireFromString("10")}
	prod := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	it, code, why := BuildStockItem(line, map[string]string{"BOLT": prod})
	if code != "" || why != "" || it.ProductID != prod || it.CostPrice != "3.333333" || it.Qty != "3" || it.Dimensions["location"] != "A-01" || it.Dimensions["batch"] != "" {
		t.Fatalf("item %+v %s %s", it, code, why)
	}
	// 3 x 3.333333 = 9.999999: the target books this, StockValue reports it, the legacy 10 is not.
	if got := StockValue(line); !got.Equal(decimal.RequireFromString("9.999999")) {
		t.Fatalf("stock value %s", got)
	}
	if _, code, _ := BuildStockItem(line, nil); code != CodeRefUnresolved {
		t.Fatalf("an item without a product id: %q", code)
	}
	line.Item = prod
	if it, code, _ := BuildStockItem(line, nil); code != "" || it.ProductID != prod {
		t.Fatalf("an item that is a UUID needs no entry: %+v %q", it, code)
	}
}

func TestBuildBankItemKeepsTheSignInMinorUnits(t *testing.T) {
	acct := "99999999-9999-4999-8999-999999999991"
	b := BankBalanceLine{LegacyID: "BK-1", BankAccount: "OPER", Currency: "USD", Balance: decimal.RequireFromString("-1250.50")}
	it, code, _ := BuildBankItem(b, map[string]string{"OPER": acct})
	if code != "" || it.BalanceMinor != -125050 || it.BankAccountID != acct {
		t.Fatalf("item %+v %q", it, code)
	}
	b.Currency, b.Balance = "JPY", decimal.RequireFromString("100.5")
	if _, code, _ := BuildBankItem(b, map[string]string{"OPER": acct}); code != CodeBankPrecision {
		t.Fatalf("a fraction of a yen: %q", code)
	}
	if _, code, _ := BuildBankItem(b, nil); code != CodeRefUnresolved {
		t.Fatalf("an account without a treasury id: %q", code)
	}
}

func TestOpeningBatchKeysAreStableAndTellPacksAndCompaniesApart(t *testing.T) {
	k := OpeningBatchKey(PackOnhand, "AX2012", "USMF", "USD", []string{"a", "b"})
	if k != OpeningBatchKey(PackOnhand, "AX2012", "USMF", "USD", []string{"a", "b"}) || k == OpeningBatchKey(PackOnhand, "AX2012", "USMF", "USD", []string{"a", "c"}) {
		t.Fatal("a key follows the rows")
	}
	if !IsOpeningBatchKey(PackOnhand, "AX2012", "USMF", k) || IsOpeningBatchKey(PackOnhand, "AX2012", "DEMF", k) || IsOpeningBatchKey(PackBankBal, "AX2012", "USMF", k) {
		t.Fatalf("key %q", k)
	}
	if bk := OpeningBatchKey(PackBankBal, "AX2012", "USMF", "", []string{"a"}); !IsOpeningBatchKey(PackBankBal, "AX2012", "USMF", bk) || len(bk) > 200 || strings.ContainsAny(bk, " /") {
		t.Fatalf("treasury key %q", bk)
	}
	if IsOpeningBatchKey(PackOnhand, "AX2012", "USMF", "mf-onhand-AX2012-whatever") || IsOpeningBatchKey(PackOnhand, "AX2012", "USMF", "") {
		t.Fatal("a foreign key is not the factory's")
	}
}

func TestAReconciledTargetLoadCountsTheStagedPackAsPosted(t *testing.T) {
	for _, p := range []Pack{PackOnhand, PackBankBal} {
		if NotPostedRun(PackFact{Pack: p}) == "" {
			t.Fatalf("%s is staged until it is loaded", p)
		}
		if why := NotPostedRun(PackFact{Pack: p, TargetLoaded: true}); why != "" {
			t.Fatalf("%s loaded: %q", p, why)
		}
	}
	if NotPostedRun(PackFact{Pack: PackFixedAssets, TargetLoaded: true}) == "" {
		t.Fatal("a pack without a target stays staged")
	}
}

func TestReconcileStockComparesQuantityAndValueExactly(t *testing.T) {
	exp, act := NewOpeningTotals(), NewOpeningTotals()
	exp.Items, act.Items = 2, 2
	exp.Qty["USD"], act.Qty["USD"] = decimal.NewFromInt(5), decimal.NewFromInt(5)
	exp.Value["USD"], act.Value["USD"] = decimal.RequireFromString("9.999999"), decimal.RequireFromString("10")
	totals, err := ReconcileStock("le", exp, act)
	if err != nil {
		t.Fatal(err)
	}
	match, mismatch := CountTotals(totals)
	if match != 2 || mismatch != 1 {
		t.Fatalf("match %d mismatch %d: %+v", match, mismatch, totals)
	}
}
