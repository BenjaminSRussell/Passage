package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

func costRow(key, le, product, site, kind, qty, amount, date string) CostTransaction {
	return CostTransaction{SourceKey: key, LegalEntityID: le, ProductID: product, SiteID: site, Kind: kind,
		Qty: qty, Amount: amount, Currency: "EUR", FinancialDate: date}
}

func balanceOf(t *testing.T, bal []domain.Balance, product, site string) domain.Balance {
	t.Helper()
	for _, b := range bal {
		if b.ProductID == product && b.SiteID == site {
			return b
		}
	}
	t.Fatalf("no balance for %s/%s in %+v", product, site, bal)
	return domain.Balance{}
}

func TestPostgres_CostTransactionsReplayAndConflict(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(2))
	le, p, site := ids.New(), ids.New(), ids.New()
	batch := []CostTransaction{
		costRow("gr-1", le, p, site, "PHYSICAL", "10", "100.00", "2026-03-10"),
		costRow("gr-2", le, p, site, "FINANCIAL", "10", "25.5000", "2026-03-20"),
	}
	if n, err := s.RecordCostTransactions(ctx, batch); err != nil || n != 2 {
		t.Fatalf("first record inserted %d, err %v; want 2", n, err)
	}
	// The same content (amount written with another scale) is a replay.
	replay := append([]CostTransaction(nil), batch...)
	replay[1].Amount = "25.50"
	if n, err := s.RecordCostTransactions(ctx, replay); err != nil || n != 0 {
		t.Fatalf("replay inserted %d, err %v; want 0", n, err)
	}
	// Other content under a recorded key refuses the whole batch, new rows included.
	changed := []CostTransaction{costRow("gr-3", le, p, site, "FINANCIAL", "1", "1.00", "2026-03-21"), batch[0]}
	changed[1].Amount = "101.00"
	if _, err := s.RecordCostTransactions(ctx, changed); problemCode(err) != CodeCostTxnConflict {
		t.Fatalf("changed replay: %v, want %s", err, CodeCostTxnConflict)
	}
	page, err := s.ListCostTransactions(ctx, CostTxnFilter{LegalEntityID: le}, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("after the refused batch: %d rows, err %v; want 2", len(page.Items), err)
	}
}

func TestPostgres_ValueBalancesSumsAsOfAndLeavesOutPPV(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(3))
	le, p1, p2, s1, s2 := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	// AC-20.4: a STANDARD item received at 10.50 against 10.00 stays at 1,000.00;
	// the 50.00 PPV goes to the PPV account, not to stock.
	rows := []CostTransaction{
		costRow("rcpt", le, p1, s1, "FINANCIAL", "100", "1000.00", "2026-03-05"),
		costRow("ppv-rcpt", le, p1, s1, "PPV", "100", "50.00", "2026-03-05"),
		costRow("ppv-inv", le, p1, s1, "PPV", "0", "30.00", "2026-03-06"),
		costRow("gr-p2", le, p2, s2, "PHYSICAL", "10", "40.00", "2026-03-10"),
		costRow("adj-p2", le, p2, s2, "SETTLEMENT_ADJ", "0", "-2.5", "2026-03-31"),
		costRow("late", le, p1, s1, "FINANCIAL", "5", "50.00", "2026-04-02"),
		// Fully issued: zero quantity and value, left out of the report.
		costRow("in-p3", le, ids.New(), s1, "FINANCIAL", "3", "9.00", "2026-03-01"),
	}
	rows = append(rows, costRow("out-p3", le, rows[6].ProductID, s1, "FINANCIAL", "-3", "-9.00", "2026-03-02"))
	if _, err := s.RecordCostTransactions(ctx, rows); err != nil {
		t.Fatal(err)
	}
	bal, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2026-03-31"})
	if err != nil {
		t.Fatal(err)
	}
	if len(bal) != 2 {
		t.Fatalf("balances = %+v, want p1/s1 and p2/s2 only", bal)
	}
	if b := balanceOf(t, bal, p1, s1); b.Qty.String() != "100" || b.FinancialValue.String() != "1000" || !b.PhysicalValue.IsZero() || b.Currency != "EUR" {
		t.Errorf("p1/s1 = %+v, want qty 100 financial 1000 (no PPV)", b)
	}
	if b := balanceOf(t, bal, p2, s2); b.Qty.String() != "10" || b.PhysicalValue.String() != "40" || b.FinancialValue.String() != "-2.5" {
		t.Errorf("p2/s2 = %+v, want qty 10 physical 40 financial -2.5", b)
	}
	onSite, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2026-04-30", SiteID: s1})
	if err != nil || len(onSite) != 1 || onSite[0].FinancialValue.String() != "1050" {
		t.Fatalf("site s1 as of 2026-04-30 = %+v, err %v; want one balance of 1050", onSite, err)
	}
	other, err := s.ValueBalances(scopedCtx(testkit.TenantID(4)), BalanceQuery{LegalEntityID: le, AsOf: "2026-04-30"})
	if err != nil || len(other) != 0 {
		t.Fatalf("another tenant sees %+v, err %v; want nothing", other, err)
	}
}

func TestPostgres_CostTransactionsFollowTheValuationLedger(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(5))
	le, p, site := ids.New(), ids.New(), ids.New()
	ifrs, err := s.CreateValuationLedger(ctx, ValuationLedger{LegalEntityID: le, LedgerGroupID: ids.New(), Code: "IFRS",
		Method: "WEIGHTED_AVG_PERIODIC", Currency: "EUR", RateType: "AVERAGE", Primary: true})
	if err != nil {
		t.Fatal(err)
	}
	gaap, err := s.CreateValuationLedger(ctx, ValuationLedger{LegalEntityID: le, LedgerGroupID: ids.New(), Code: "USGAAP",
		Method: "FIFO", Currency: "USD", RateType: "SPOT"})
	if err != nil {
		t.Fatal(err)
	}
	// AC-20.11: the same issue valued in each ledger.
	primaryRow := costRow("iss-ifrs", le, p, site, "FINANCIAL", "50", "550.00", "2026-01-31")
	primaryRow.ValuationLedgerID = ifrs.ID
	groupRow := costRow("iss-gaap", le, p, site, "FINANCIAL", "50", "660.00", "2026-01-31")
	groupRow.ValuationLedgerID, groupRow.Currency = gaap.ID, "USD"
	defaultRow := costRow("rcpt-default", le, p, site, "FINANCIAL", "1", "10.00", "2026-01-31")
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{primaryRow, groupRow, defaultRow}); err != nil {
		t.Fatal(err)
	}
	// The primary ledger's rows are the default valuation.
	primary, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, AsOf: "2026-01-31"})
	if err != nil || len(primary) != 1 || primary[0].FinancialValue.String() != "560" || primary[0].Currency != "EUR" {
		t.Fatalf("primary balances = %+v, err %v; want 560 EUR", primary, err)
	}
	if n, err := s.RecordCostTransactions(ctx, []CostTransaction{primaryRow}); err != nil || n != 0 {
		t.Fatalf("replay under the primary ledger id inserted %d, err %v; want a replay", n, err)
	}
	group, err := s.ValueBalances(ctx, BalanceQuery{LegalEntityID: le, ValuationLedgerID: gaap.ID, AsOf: "2026-01-31"})
	if err != nil || len(group) != 1 || group[0].FinancialValue.String() != "660" || group[0].Currency != "USD" {
		t.Fatalf("group balances = %+v, err %v; want 660 USD", group, err)
	}
	wrongCurrency := costRow("bad-cur", le, p, site, "FINANCIAL", "1", "1.00", "2026-01-31")
	wrongCurrency.ValuationLedgerID = gaap.ID
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{wrongCurrency}); problemCode(err) != CodeCostTxnCurrency {
		t.Errorf("EUR row in the USD ledger: %v, want %s", err, CodeCostTxnCurrency)
	}
	usdDefault := costRow("bad-default", le, p, site, "FINANCIAL", "1", "1.00", "2026-01-31")
	usdDefault.Currency = "USD"
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{usdDefault}); problemCode(err) != CodeCostTxnCurrency {
		t.Errorf("USD row in the EUR primary valuation: %v, want %s", err, CodeCostTxnCurrency)
	}
	foreign := costRow("foreign-ledger", ids.New(), p, site, "FINANCIAL", "1", "1.00", "2026-01-31")
	foreign.ValuationLedgerID = gaap.ID
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{foreign}); problemCode(err) != CodeLedgerNotFound {
		t.Errorf("ledger of another legal entity: %v, want %s", err, CodeLedgerNotFound)
	}
}

func TestPostgres_DefaultValuationKeepsItsFirstCurrency(t *testing.T) {
	s, _ := setup(t)
	ctx := scopedCtx(testkit.TenantID(6))
	le, p, site := ids.New(), ids.New(), ids.New()
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{costRow("a", le, p, site, "FINANCIAL", "1", "1", "2026-01-01")}); err != nil {
		t.Fatal(err)
	}
	usd := costRow("b", le, p, site, "FINANCIAL", "1", "1", "2026-01-02")
	usd.Currency = "USD"
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{usd}); problemCode(err) != CodeCostTxnCurrency {
		t.Fatalf("second currency: %v, want %s", err, CodeCostTxnCurrency)
	}
	mixed := []CostTransaction{costRow("c", ids.New(), p, site, "FINANCIAL", "1", "1", "2026-01-01"), usd}
	mixed[1].LegalEntityID = mixed[0].LegalEntityID
	if _, err := s.RecordCostTransactions(ctx, mixed); problemCode(err) != CodeCostTxnCurrency {
		t.Fatalf("two currencies in a new entity's first batch: %v, want %s", err, CodeCostTxnCurrency)
	}
}

func TestPostgres_CostTransactionsAreAppendOnlyAndPaged(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(7))
	le, p, site := ids.New(), ids.New(), ids.New()
	var rows []CostTransaction
	for i, date := range []string{"2026-01-01", "2026-01-02", "2026-01-03", "2026-01-03", "2026-01-05"} {
		rows = append(rows, costRow("k"+string(rune('a'+i)), le, p, site, "FINANCIAL", "1", "1", date))
	}
	if _, err := s.RecordCostTransactions(ctx, rows); err != nil {
		t.Fatal(err)
	}
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update cost_transaction set amount_acct = 0 where source_key = 'ka'`)
		return err
	})
	if err == nil {
		t.Fatal("an update of a cost transaction succeeded; the table must be append-only")
	}
	var seen []string
	after := ""
	for pages := 0; pages < 5; pages++ {
		page, err := s.ListCostTransactions(ctx, CostTxnFilter{LegalEntityID: le, AsOf: "2026-01-04"}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Items {
			seen = append(seen, c.FinancialDate)
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	if len(seen) != 4 || seen[0] != "2026-01-03" || seen[3] != "2026-01-01" {
		t.Fatalf("paged dates = %v, want the four up to 2026-01-04, newest first", seen)
	}
}
