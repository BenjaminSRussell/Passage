package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// bankRunWith reconciles a bank run over the lines, treating only the accepted ones as held.
func bankRunWith(t *testing.T, lines []BankBalanceLine, accounts map[string]struct{}) PackRun {
	t.Helper()
	r := PackRun{Pack: PackBankBal, SourceCompany: "1000", Status: StatusValidated}
	accepted, _ := ValidateBankBalances(lines, accounts)
	held := map[string]BankBalanceLine{}
	for _, b := range accepted {
		held[b.LegacyID] = b
	}
	if err := ReconcileBankBalances(&r, lines, accepted, len(accepted), held); err != nil {
		t.Fatal(err)
	}
	r.RecordsQuarantined = len(lines) - len(accepted)
	return r
}

func TestReleaseBankBalance_MovesTheLegacyBalanceToTheCorrectedAccount(t *testing.T) {
	accounts := map[string]struct{}{"OPER": {}, "SAV": {}}
	lines := []BankBalanceLine{{"B1", "OPER", "USD", dec("10")}, {"B2", "TYPO", "USD", dec("-4.50")}}
	r := bankRunWith(t, lines, accounts)
	if r.Status != StatusLoaded || !r.HasMismatch() {
		t.Fatalf("run %s mismatch %v", r.Status, r.HasMismatch())
	}
	line, err := ReleaseBankBalance(&r, lines[1], BankCorrection{BankAccount: " SAV "}, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if line.BankAccount != "SAV" || r.Status != StatusReconciled || r.RecordsLoaded != 2 || r.RecordsQuarantined != 0 {
		t.Fatalf("line %+v run %s loaded %d quarantined %d", line, r.Status, r.RecordsLoaded, r.RecordsQuarantined)
	}
	for _, tot := range r.Totals {
		if tot.Status != StatusMatch {
			t.Fatalf("total %+v", tot)
		}
	}
}

func TestReleaseBankBalance_Refusals(t *testing.T) {
	accounts := map[string]struct{}{"OPER": {}}
	lines := []BankBalanceLine{{"B1", "OPER", "USD", dec("10")}, {"B2", "TYPO", "USD", dec("1")}}
	r := bankRunWith(t, lines, accounts)
	if _, err := ReleaseBankBalance(&r, lines[1], BankCorrection{BankAccount: "STILL-WRONG"}, accounts); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an account outside the master: %v", err)
	}
	if r.RecordsQuarantined != 1 || r.Status != StatusLoaded {
		t.Fatalf("a refused release must not change the run: %+v", r)
	}
	other := PackRun{Pack: PackOnhand, Status: StatusLoaded}
	if _, err := ReleaseBankBalance(&other, lines[1], BankCorrection{}, accounts); !errors.Is(err, ErrUnknownPack) {
		t.Fatalf("wrong pack: %v", err)
	}
	r.Status = StatusReconciled
	if _, err := ReleaseBankBalance(&r, lines[1], BankCorrection{BankAccount: "OPER"}, accounts); !errors.Is(err, ErrTransition) {
		t.Fatalf("run not LOADED: %v", err)
	}
}

func TestReleaseOnhand_MovesQuantityAndValueToTheCorrectedKeys(t *testing.T) {
	items := map[string]struct{}{"BOLT": {}, "PUMP": {}}
	mk := func(id, item, site string, qty, val string) OnhandLine {
		return OnhandLine{LegacyID: id, Item: item, Site: site, Warehouse: "W1", Unit: "EA", Currency: "USD", Quantity: dec(qty), Value: dec(val)}
	}
	lines := []OnhandLine{mk("O1", "BOLT", "S1", "10", "25"), mk("O2", "BLOT", "S1", "4", "8")}
	r := PackRun{Pack: PackOnhand, SourceCompany: "USMF", Status: StatusValidated}
	accepted, _ := ValidateOnhand(lines, items)
	held := map[string]OnhandLine{"O1": lines[0]}
	if err := ReconcileOnhand(&r, lines, accepted, 1, held, "", nil); err != nil {
		t.Fatal(err)
	}
	r.RecordsQuarantined = 1
	if !r.HasMismatch() {
		t.Fatal("the held-back line must show before the release")
	}
	line, err := ReleaseOnhand(&r, lines[1], OnhandCorrection{Item: "BOLT"}, items)
	if err != nil {
		t.Fatal(err)
	}
	if line.Item != "BOLT" || r.Status != StatusReconciled {
		t.Fatalf("line %+v run %s", line, r.Status)
	}
	for _, tot := range r.Totals {
		if tot.Status != StatusMatch {
			t.Fatalf("total %+v", tot)
		}
	}
	if (OnhandCorrection{}).IsZero() != true || (OnhandCorrection{Unit: "KG"}).IsZero() {
		t.Fatal("IsZero")
	}
}

func TestReleaseOnhand_Refusals(t *testing.T) {
	items := map[string]struct{}{"BOLT": {}}
	ok := OnhandLine{LegacyID: "O1", Item: "BOLT", Site: "S1", Warehouse: "W1", Unit: "EA", Currency: "USD", Quantity: dec("2"), Value: dec("4")}
	bad := ok
	bad.LegacyID, bad.Item = "O2", "BLOT"
	r := PackRun{Pack: PackOnhand, SourceCompany: "USMF", Status: StatusValidated}
	if err := ReconcileOnhand(&r, []OnhandLine{ok, bad}, []OnhandLine{ok}, 1, map[string]OnhandLine{"O1": ok}, "", nil); err != nil {
		t.Fatal(err)
	}
	r.RecordsQuarantined = 1
	totals := append([]ControlTotal(nil), r.Totals...)
	// A dimension fix that leaves the item off the master still fails, and changes nothing.
	if _, err := ReleaseOnhand(&r, bad, OnhandCorrection{Site: "S2"}, items); !errors.Is(err, ErrInvalid) {
		t.Fatalf("item still off the master: %v", err)
	}
	if r.Status != StatusLoaded || r.RecordsQuarantined != 1 || len(r.Totals) != len(totals) {
		t.Fatalf("a refused release must not change the run: %+v", r)
	}
	// The unit is correctable but never to blank: a blank field keeps the extracted value.
	noUnit := bad
	noUnit.Unit = ""
	if _, err := ReleaseOnhand(&r, noUnit, OnhandCorrection{Item: "BOLT", Unit: "  "}, items); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank unit: %v", err)
	}
	if _, err := ReleaseOnhand(&PackRun{Pack: PackBankBal, Status: StatusLoaded}, bad, OnhandCorrection{Item: "BOLT"}, items); !errors.Is(err, ErrUnknownPack) {
		t.Fatalf("wrong pack: %v", err)
	}
	r.Status = StatusReconciled
	if _, err := ReleaseOnhand(&r, bad, OnhandCorrection{Item: "BOLT"}, items); !errors.Is(err, ErrTransition) {
		t.Fatalf("run not LOADED: %v", err)
	}
}

func TestCorrections_WhitespaceIsNoCorrection(t *testing.T) {
	if !(Correction{Party: " ", Account: "\t"}).IsZero() || (Correction{Account: "2100"}).IsZero() {
		t.Fatal("Correction.IsZero")
	}
	if !(BankCorrection{BankAccount: "  "}).IsZero() || (BankCorrection{BankAccount: "SAV"}).IsZero() {
		t.Fatal("BankCorrection.IsZero")
	}
	if !(OnhandCorrection{Site: " ", Batch: "\t"}).IsZero() || (OnhandCorrection{Location: "A-01"}).IsZero() {
		t.Fatal("OnhandCorrection.IsZero")
	}
}

func TestReleaseOpenOrder_MovesQuantityAndCountsTheOrderOnce(t *testing.T) {
	masters := OrderMasters{Items: map[string]struct{}{"BOLT": {}, "NUT": {}}, Customers: map[string]struct{}{"C1": {}}, Vendors: map[string]struct{}{}}
	lines := []OpenOrderLine{
		orderLine("L1", "SALES", "SO1", "1", "BOLT", "C1", "10", "4", "2.5"),
		orderLine("L2", "SALES", "SO1", "2", "TYPO", "C1", "4", "0", "1.5"),
		orderLine("L3", "SALES", "SO2", "1", "TYPO", "C1", "2", "0", "1"),
	}
	r := PackRun{Pack: PackOpenOrders, SourceCompany: "USMF", Status: StatusValidated}
	accepted, _ := ValidateOpenOrders(lines, masters)
	held := map[string]OpenOrderLine{}
	for _, l := range accepted {
		held[l.HeldKey()] = l
	}
	if err := ReconcileOpenOrders(&r, lines, accepted, len(accepted), held); err != nil {
		t.Fatal(err)
	}
	r.RecordsQuarantined = 2
	if r.Status != StatusLoaded || !r.HasMismatch() {
		t.Fatalf("run %s", r.Status)
	}
	// SO1 is on the loaded side already: its order count does not move; SO2 is new.
	if _, err := ReleaseOpenOrder(&r, lines[1], OrderCorrection{Item: " NUT "}, masters, false); err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusLoaded || r.RecordsLoaded != 2 {
		t.Fatalf("after first release %s loaded %d", r.Status, r.RecordsLoaded)
	}
	line, err := ReleaseOpenOrder(&r, lines[2], OrderCorrection{Item: "BOLT", DeliveryDate: "2027-03-01"}, masters, true)
	if err != nil {
		t.Fatal(err)
	}
	if line.DeliveryDate != "2027-03-01" || r.Status != StatusReconciled || r.RecordsQuarantined != 0 {
		t.Fatalf("line %+v run %s", line, r.Status)
	}
	for _, c := range r.Totals {
		if c.Status != "MATCH" {
			t.Errorf("total %s %v: source %s target %s", c.Name, c.Key, c.Source, c.Target)
		}
	}
}

func TestReleaseOpenOrder_RefusesWhileTheSuiteStillFailsOrWrongRun(t *testing.T) {
	masters := OrderMasters{Items: map[string]struct{}{"BOLT": {}}, Customers: map[string]struct{}{"C1": {}}}
	bad := orderLine("L1", "SALES", "SO1", "1", "TYPO", "C1", "4", "0", "1")
	r := PackRun{Pack: PackOpenOrders, SourceCompany: "USMF", Status: StatusLoaded}
	if _, err := ReleaseOpenOrder(&r, bad, OrderCorrection{}, masters, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncorrected: %v", err)
	}
	if _, err := ReleaseOpenOrder(&r, bad, OrderCorrection{Item: "BOLT", Party: "C9"}, masters, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown customer: %v", err)
	}
	r.Status = StatusReconciled
	if _, err := ReleaseOpenOrder(&r, bad, OrderCorrection{Item: "BOLT"}, masters, true); !errors.Is(err, ErrTransition) {
		t.Fatalf("reconciled run: %v", err)
	}
	r.Pack = PackOnhand
	if _, err := ReleaseOpenOrder(&r, bad, OrderCorrection{Item: "BOLT"}, masters, true); !errors.Is(err, ErrUnknownPack) {
		t.Fatalf("other pack: %v", err)
	}
	if !(OrderCorrection{Item: " "}).IsZero() || (OrderCorrection{DeliveryDate: "2027-01-01"}).IsZero() {
		t.Fatal("IsZero")
	}
}
