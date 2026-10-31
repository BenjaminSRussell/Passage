package store

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
)

// Wave 38 proof, AC "cost accounting ledger reconciles to GL for primary costs":
// the costledger-month fixture. One legal entity books a September of journal
// lines on three primary elements, then runs every secondary process the
// service has (reciprocal allocation, top-down distribution, internal order
// settlement, template allocation). The expected figures are hand-summed from
// the journal lines below:
//
//	600100  12,000.00 (CC-ADMIN) + 30,000.00 (CC-PROD) = 42,000.00
//	600200   8,000.00 (CC-IT)
//	600300     799.50 (IO-1)
//	610100   5,000.00 (ENTITY)
//	620000   2,500.50 (CC-ADMIN) + 1,200.00 (IO-1)     =  3,700.50
//	PAYROLL 50,799.50, FREIGHT 5,000.00, RENT 3,700.50; month total 59,500.00
//
// An August line and a EUR line on the same accounts must stay out of the
// September USD figures.
func TestCostLedgerMonth_ReconcilesToGLForPrimaryCosts(t *testing.T) {
	f := newLedger(t, 38) // FREIGHT primary 610000-619999 and SETTLE secondary
	elem := func(kind, code, from, to string) string {
		_, raw, err := f.st.CreateCostElement(f.ctx, ids.New(), "h", CostElement{LegalEntityID: f.le, Kind: kind, Code: code, Name: code,
			MainAccountFrom: from, MainAccountTo: to})
		if err != nil {
			t.Fatalf("element %s: %v", code, err)
		}
		return idOf(t, raw)
	}
	payroll := elem("Primary", "PAYROLL", "600000", "609999")
	rent := elem("Primary", "RENT", "620000", "629999")
	alloc := elem("Secondary", "ALLOC", "", "")
	freight, settle := f.primary, f.secondary

	admin, it, prod := f.object(t, "CostCenter", "CC-ADMIN"), f.object(t, "CostCenter", "CC-IT"), f.object(t, "CostCenter", "CC-PROD")
	entity, north, south := f.object(t, "CostCenter", "ENTITY"), f.object(t, "Product", "NORTH"), f.object(t, "Product", "SOUTH")
	order := f.object(t, "InternalOrder", "IO-1")

	line := func(account, object, period, currency, amount string) MirrorLine {
		return MirrorLine{JournalLineID: ids.New(), MainAccount: account, CostObjectID: object, Period: period, Currency: currency, Amount: dec(amount)}
	}
	if _, _, err := f.st.MirrorJournalLines(f.ctx, ids.New(), "h", MirrorRequest{LegalEntityID: f.le, Lines: []MirrorLine{
		line("600100", admin, "2026-09", "USD", "12000.00"),
		line("600200", it, "2026-09", "USD", "8000.00"),
		line("600100", prod, "2026-09", "USD", "30000.00"),
		line("610100", entity, "2026-09", "USD", "5000.00"),
		line("620000", admin, "2026-09", "USD", "2500.50"),
		line("620000", order, "2026-09", "USD", "1200.00"),
		line("600300", order, "2026-09", "USD", "799.50"),
		line("600100", admin, "2026-08", "USD", "999.00"),
		line("600100", it, "2026-09", "EUR", "444.00"),
	}}); err != nil {
		t.Fatal(err)
	}

	// Reciprocal allocation: ADMIN (14,500.50) and IT (8,000.00) serve each
	// other and production; PROD is the final receiver of all 22,500.50.
	_, raw, err := f.st.RunAllocation(f.ctx, ids.New(), "h", RunRequest{LegalEntityID: f.le, Period: "2026-09", Currency: "USD", SecondaryElementID: alloc,
		CostObjectIDs: []string{admin, it, prod},
		Basis:         [][]decimal.Decimal{{dec("0"), dec("1"), dec("3")}, {dec("1"), dec("0"), dec("1")}, {dec("0"), dec("0"), dec("0")}}})
	if err != nil {
		t.Fatal(err)
	}
	var run RunResponse
	if err := json.Unmarshal(raw, &run); err != nil || !run.Result.TotalIn.Equal(dec("22500.50")) || !run.Result.TotalOut.Equal(dec("22500.50")) {
		t.Fatalf("allocation run %+v (%v), want 22,500.50 in and out", run.Result, err)
	}
	// Top-down: ENTITY's 5,000.00 freight split 3:1 to NORTH and SOUTH on FREIGHT.
	if _, _, err := f.st.RunTopDown(f.ctx, ids.New(), "h", TopDownRequest{LegalEntityID: f.le, Period: "2026-09", ReferencePeriod: "2026-08", Currency: "USD",
		SourceCostObjectID: entity, CostElementID: freight, Amount: dec("5000.00"),
		Segments: []TopDownSegment{{CostObjectID: north, Basis: dec("3")}, {CostObjectID: south, Basis: dec("1")}}}); err != nil {
		t.Fatal(err)
	}
	// Settlement: IO-1's collected 1,999.50 to PROD on SETTLE.
	if _, _, err := f.st.SettleInternalOrder(f.ctx, ids.New(), "h", SettlementRequest{OrderID: order, Period: "2026-09", Currency: "USD",
		ReceiverID: prod, SecondaryElementID: settle, Amount: dec("1999.50")}); err != nil {
		t.Fatal(err)
	}
	// Template: a flat 100.00 from PROD to NORTH on ALLOC.
	_, raw, err = f.st.SaveTemplate(f.ctx, ids.New(), "h", AllocationTemplate{LegalEntityID: f.le, Code: "TPL-FLAT", Name: "Flat", Currency: "USD",
		SenderCostObjectID: prod, Lines: []TemplateLine{{ReceiverCostObjectID: north, SecondaryElementID: alloc, Formula: "100.00"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.RunTemplate(f.ctx, ids.New(), "h", TemplateRunRequest{TemplateID: idOf(t, raw), Period: "2026-09"}); err != nil {
		t.Fatal(err)
	}

	// 1. The reconciliation against the hand-summed journal balances is exact.
	journal := []domain.JournalBalance{
		{MainAccount: "600100", Amount: dec("42000.00")}, {MainAccount: "600200", Amount: dec("8000.00")},
		{MainAccount: "600300", Amount: dec("799.50")}, {MainAccount: "610100", Amount: dec("5000.00")},
		{MainAccount: "620000", Amount: dec("3700.50")}, {MainAccount: "400000", Amount: dec("-80000.00")},
	}
	recon, err := f.st.Reconcile(f.ctx, ReconcileRequest{LegalEntityID: f.le, Period: "2026-09", Currency: "USD", Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	wantElement := map[string]string{payroll: "50799.50", freight: "5000.00", rent: "3700.50"}
	if !recon.Reconciled || len(recon.Lines) != 3 || len(recon.UnmappedAccounts) != 1 || recon.UnmappedAccounts[0].MainAccount != "400000" {
		t.Fatalf("reconciliation %+v", recon)
	}
	for _, l := range recon.Lines {
		if want := dec(wantElement[l.ElementID]); !l.Journal.Equal(want) || !l.CostLedger.Equal(want) || !l.Difference.IsZero() {
			t.Errorf("element %s: journal %s ledger %s difference %s, want %s / %s / 0", l.ElementID, l.Journal, l.CostLedger, l.Difference, want, want)
		}
	}

	// 2. Every entry on a primary element, whatever its origin, still sums to
	// the journal: top-down moved freight between objects, not out of FREIGHT.
	month := "period_key = '2026-09' and currency = 'USD'"
	for id, want := range wantElement {
		if got := sumEntries(t, f.ctx, f.st.pool, month+" and cost_element_id = $1", id); !got.Equal(dec(want)) {
			t.Errorf("primary element %s holds %s after the month's runs, want %s", id, got, want)
		}
	}
	// 3. Secondary elements net to zero and the month still totals 59,500.00.
	for _, id := range []string{alloc, settle} {
		if got := sumEntries(t, f.ctx, f.st.pool, month+" and cost_element_id = $1", id); !got.IsZero() {
			t.Errorf("secondary element %s nets to %s, want 0", id, got)
		}
	}
	if got := sumEntries(t, f.ctx, f.st.pool, month); !got.Equal(dec("59500.00")) {
		t.Fatalf("month total %s, want 59500.00", got)
	}
	// 4. Where the cost ended up, by hand: PROD 30,000.00 + 22,500.50 + 1,999.50
	// − 100.00; NORTH 3,750.00 + 100.00; SOUTH 1,250.00; the rest emptied.
	for obj, want := range map[string]string{admin: "0", it: "0", prod: "54400.00", entity: "0", north: "3850.00", south: "1250.00", order: "0"} {
		if got := sumEntries(t, f.ctx, f.st.pool, month+" and cost_object_id = $1", obj); !got.Equal(dec(want)) {
			t.Errorf("object %s holds %s, want %s", obj, got, want)
		}
	}

	// 5. A journal line the ledger never mirrored shows as exactly its amount.
	journal[0].Amount = dec("42150.00")
	if recon, err := f.st.Reconcile(f.ctx, ReconcileRequest{LegalEntityID: f.le, Period: "2026-09", Currency: "USD", Journal: journal}); err != nil || recon.Reconciled {
		t.Fatalf("unmirrored 150.00 not reported: %+v (%v)", recon, err)
	} else {
		for _, l := range recon.Lines {
			if want := map[string]string{payroll: "150.00", freight: "0", rent: "0"}[l.ElementID]; !l.Difference.Equal(dec(want)) {
				t.Errorf("element %s difference %s, want %s", l.ElementID, l.Difference, want)
			}
		}
	}
	// 6. The August and EUR lines reconcile on their own period and currency only.
	for _, c := range []struct{ period, currency, amount string }{{"2026-08", "USD", "999.00"}, {"2026-09", "EUR", "444.00"}} {
		recon, err := f.st.Reconcile(f.ctx, ReconcileRequest{LegalEntityID: f.le, Period: c.period, Currency: c.currency,
			Journal: []domain.JournalBalance{{MainAccount: "600100", Amount: dec(c.amount)}}})
		if err != nil || !recon.Reconciled || len(recon.Lines) != 1 {
			t.Errorf("%s %s reconciliation %+v (%v)", c.period, c.currency, recon, err)
		}
	}
}
