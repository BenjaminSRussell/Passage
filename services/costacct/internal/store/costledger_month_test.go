package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
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

	gl, usd, eur := ledgerclient.NewFake(), ids.New(), ids.New()
	gl.AddLedger(usd, f.le)
	gl.AddLedger(eur, f.le)
	post := func(ledger, date, currency, account, amount string) {
		t.Helper()
		if _, err := gl.Post(f.ctx, ledgerclient.Entry{LegalEntityID: f.le, LedgerID: ledger, DocumentID: ids.New(), PostingDate: date, Currency: currency,
			IdempotencyKey: ids.New(), Lines: []ledgerclient.Line{{Account: account, Amount: dec(amount)}, {Account: "400000", Credit: true, Amount: dec(amount)}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range [][2]string{{"600100", "12000.00"}, {"600200", "8000.00"}, {"600100", "30000.00"}, {"610100", "5000.00"}, {"620000", "2500.50"}, {"620000", "1200.00"}, {"600300", "799.50"}} {
		post(usd, "2026-09-10", "USD", p[0], p[1])
	}
	post(usd, "2026-08-10", "USD", "600100", "999.00")
	post(eur, "2026-09-10", "EUR", "600100", "444.00")

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

	// 1. The reconciliation against the journal balances the general ledger holds is exact.
	recon, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: usd, Period: "2026-09", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	wantElement := map[string]string{payroll: "50799.50", freight: "5000.00", rent: "3700.50"}
	if !recon.Reconciled || len(recon.Lines) != 3 || len(recon.UnmappedAccounts) != 0 {
		t.Fatalf("reconciliation %+v", recon)
	}
	for _, l := range recon.Lines {
		if want := dec(wantElement[l.ElementID]); !l.Journal.Equal(want) || !l.CostLedger.Equal(want) || !l.Difference.IsZero() {
			t.Errorf("element %s: journal %s ledger %s difference %s, want %s / %s / 0", l.ElementID, l.Journal, l.CostLedger, l.Difference, want, want)
		}
	}
	if entities, err := f.st.LegalEntities(f.ctx); err != nil || len(entities) != 1 || entities[0] != f.le {
		t.Fatalf("legal entities %v (%v), want [%s]", entities, err, f.le)
	}
	if inferred, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: usd, Period: "2026-09"}); err != nil || !inferred.Reconciled {
		t.Fatalf("a reconciliation that takes the ledger's currency: %+v (%v)", inferred, err)
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
	post(usd, "2026-09-15", "USD", "600100", "150.00")
	if recon, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: usd, Period: "2026-09", Currency: "USD"}); err != nil || recon.Reconciled {
		t.Fatalf("unmirrored 150.00 not reported: %+v (%v)", recon, err)
	} else {
		for _, l := range recon.Lines {
			if want := map[string]string{payroll: "150.00", freight: "0", rent: "0"}[l.ElementID]; !l.Difference.Equal(dec(want)) {
				t.Errorf("element %s difference %s, want %s", l.ElementID, l.Difference, want)
			}
		}
	}
	// 6. The August and EUR lines reconcile on their own period and ledger only.
	for _, c := range []struct{ ledger, period, currency string }{{usd, "2026-08", "USD"}, {eur, "2026-09", "EUR"}} {
		recon, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: c.ledger, Period: c.period, Currency: c.currency})
		if err != nil || !recon.Reconciled || len(recon.Lines) != 1 {
			t.Errorf("%s %s reconciliation %+v (%v)", c.period, c.currency, recon, err)
		}
	}
	// 7. A ledger in another currency than the one asked for is refused, and a ledger that fails surfaces as a bad gateway.
	if _, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: eur, Period: "2026-09", Currency: "USD"}); !hasCode(err, CodeLedgerCurrency) {
		t.Errorf("currency mismatch: %v", err)
	}
	if _, err := f.st.ReconcileToGL(f.ctx, failingGL{gl}, GLReconcileRequest{LegalEntityID: f.le, LedgerID: usd, Period: "2026-09", Currency: "USD"}); !hasCode(err, CodeLedgerRefused) {
		t.Errorf("ledger failure: %v", err)
	}
}

func TestReconcileToGL_YearEndMonthLeavesOutOnlyTheCloseServicesEntries(t *testing.T) {
	f := newLedger(t, 38)
	cc := f.object(t, "CostCenter", "CC-DEC")
	if _, _, err := f.st.MirrorJournalLines(f.ctx, ids.New(), "h", MirrorRequest{LegalEntityID: f.le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "610100", CostObjectID: cc, Period: "2026-12", Currency: "USD", Amount: dec("1000.00")},
	}}); err != nil {
		t.Fatal(err)
	}
	gl, ledger := ledgerclient.NewFake(), ids.New()
	gl.AddLedger(ledger, f.le)
	post := func(service, date, sourceType, debit, credit string) string {
		t.Helper()
		gl.PostAs(service)
		id, err := gl.Post(f.ctx, ledgerclient.Entry{LegalEntityID: f.le, LedgerID: ledger, DocumentID: ids.New(), PostingDate: date, Currency: "USD",
			IdempotencyKey: ids.New(), SourceType: sourceType, PostClosed: sourceType == ledgerclient.YearEndClosingSourceType,
			Lines: []ledgerclient.Line{{Account: debit, Amount: dec("1000.00")}, {Account: credit, Credit: true, Amount: dec("1000.00")}}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	reconcile := func() domain.Reconciliation {
		t.Helper()
		recon, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: ledger, Period: "2026-12", Currency: "USD"})
		if err != nil {
			t.Fatal(err)
		}
		return recon
	}
	post("payables", "2026-12-10", "cost_booking", "610100", "400000")
	closing := post(ledgerclient.CloseService, "2026-12-31", ledgerclient.YearEndClosingSourceType, "300000", "610100")
	if recon := reconcile(); !recon.Reconciled || len(recon.Lines) != 1 || !recon.Lines[0].Journal.Equal(dec("1000.00")) || !recon.Lines[0].CostLedger.Equal(dec("1000.00")) {
		t.Fatalf("year-end month %+v, want one element at 1000.00 on both sides", recon)
	}

	gl.PostAs(ledgerclient.CloseService)
	if _, err := gl.Reverse(f.ctx, ledgerclient.Reversal{LegalEntityID: f.le, EntryID: closing, IdempotencyKey: ids.New(), PostClosed: true}); err != nil {
		t.Fatal(err)
	}
	post(ledgerclient.CloseService, "2026-12-31", ledgerclient.YearEndClosingSourceType, "300000", "610100")
	if recon := reconcile(); !recon.Reconciled {
		t.Fatalf("a year-end re-run (close, reverse, close) must net to nothing: %+v", recon)
	}

	post("payables", "2026-12-20", "cost_booking", "610100", "400000")
	if recon := reconcile(); recon.Reconciled || !recon.Lines[0].Difference.Equal(dec("1000.00")) {
		t.Fatalf("an unmirrored booking next to the closing entry: %+v", recon)
	}
}

func TestReconcileToGL_AnEntryTaggedYearEndClosingByAnotherServiceStaysInTheJournal(t *testing.T) {
	f := newLedger(t, 38)
	gl, ledger := ledgerclient.NewFake(), ids.New()
	gl.AddLedger(ledger, f.le)
	gl.PostAs("payables")
	if _, err := gl.Post(f.ctx, ledgerclient.Entry{LegalEntityID: f.le, LedgerID: ledger, DocumentID: ids.New(), PostingDate: "2026-12-10", Currency: "USD",
		IdempotencyKey: ids.New(), SourceType: ledgerclient.YearEndClosingSourceType,
		Lines: []ledgerclient.Line{{Account: "610100", Amount: dec("1000.00")}, {Account: "400000", Credit: true, Amount: dec("1000.00")}}}); err != nil {
		t.Fatal(err)
	}
	recon, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: ledger, Period: "2026-12"})
	if err != nil || recon.Reconciled || len(recon.Lines) != 1 || !recon.Lines[0].Journal.Equal(dec("1000.00")) || !recon.Lines[0].CostLedger.IsZero() {
		t.Fatalf("the unmirrored cost tagged as a closing entry by another service must show as a difference; the spoofed closing entry must not hide the cost: %+v (%v)", recon, err)
	}
}

type ignoringGL struct{ *ledgerclient.Fake }

func (g ignoringGL) Balances(ctx context.Context, q ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error) {
	q.ExcludeYearEndClosing = false
	return g.Fake.Balances(ctx, q)
}

func TestReconcileToGL_FailsClosedWhenTheLedgerDoesNotConfirmTheExclusion(t *testing.T) {
	f := newLedger(t, 38)
	cc := f.object(t, "CostCenter", "CC-DEC")
	if _, _, err := f.st.MirrorJournalLines(f.ctx, ids.New(), "h", MirrorRequest{LegalEntityID: f.le, Lines: []MirrorLine{
		{JournalLineID: ids.New(), MainAccount: "610100", CostObjectID: cc, Period: "2026-12", Currency: "USD", Amount: dec("1000.00")},
	}}); err != nil {
		t.Fatal(err)
	}
	gl, ledger := ledgerclient.NewFake(), ids.New()
	gl.AddLedger(ledger, f.le)
	if _, err := f.st.ReconcileToGL(f.ctx, ignoringGL{gl}, GLReconcileRequest{LegalEntityID: f.le, LedgerID: ledger, Period: "2026-12"}); !hasCode(err, CodeLedgerRefused) {
		t.Fatalf("a ledger that ignores the option must not be reconciled against: %v", err)
	}
}

func TestReconcileToGL_ValidatesThePeriodBeforeCallingTheLedger(t *testing.T) {
	f := newLedger(t, 38)
	gl := &countingGL{Fake: ledgerclient.NewFake()}
	gl.AddLedger(f.le, f.le)
	if _, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: f.le, Period: "2026-13"}); !hasCode(err, "COM-0001") || gl.calls != 0 {
		t.Fatalf("a bad period must be refused before any ledger call: %v (%d calls)", err, gl.calls)
	}
}

type countingGL struct {
	*ledgerclient.Fake
	calls int
}

func (g *countingGL) LegalEntity(ctx context.Context, le, ledger string) (string, error) {
	g.calls++
	return g.Fake.LegalEntity(ctx, le, ledger)
}

func TestReconcileToGL_RefusesALedgerOfAnotherLegalEntity(t *testing.T) {
	f := newLedger(t, 38)
	gl, foreign, unknown := ledgerclient.NewFake(), ids.New(), ids.New()
	gl.AddLedger(foreign, ids.New())
	if _, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: foreign, Period: "2026-09"}); !hasCode(err, CodeLedgerForeign) {
		t.Errorf("foreign ledger: %v", err)
	}
	if _, err := f.st.ReconcileToGL(f.ctx, gl, GLReconcileRequest{LegalEntityID: f.le, LedgerID: unknown, Period: "2026-09"}); !hasCode(err, CodeLedgerForeign) {
		t.Errorf("unknown ledger: %v", err)
	}
}

type failingGL struct{ *ledgerclient.Fake }

func (failingGL) Balances(context.Context, ledgerclient.BalanceQuery) (ledgerclient.AccountBalances, error) {
	return ledgerclient.AccountBalances{}, errors.New("ledger down")
}

// Cost control reads the cost ledger per cost object: the journal part and the allocated part stay apart, a period outside the
// window and another currency's row stay out of a USD September, and another legal entity's objects are never read.
func TestActuals_SplitJournalFromAllocatedPerCostObject(t *testing.T) {
	f := newLedger(t, 39)
	admin, it := f.object(t, "CostCenter", "CC-ADMIN"), f.object(t, "CostCenter", "CC-IT")
	order := f.object(t, "InternalOrder", "IO-9")
	line := func(account, object, period, currency, amount string) MirrorLine {
		return MirrorLine{JournalLineID: ids.New(), MainAccount: account, CostObjectID: object, Period: period, Currency: currency, Amount: dec(amount)}
	}
	if _, _, err := f.st.MirrorJournalLines(f.ctx, ids.New(), "h", MirrorRequest{LegalEntityID: f.le, Lines: []MirrorLine{
		line("610100", admin, "2026-09", "USD", "1000.00"),
		line("610100", admin, "2026-09", "USD", "250.50"),
		line("610100", admin, "2026-10", "USD", "70.00"),
		line("610100", admin, "2026-09", "EUR", "5.00"),
		line("610100", it, "2026-09", "USD", "400.00"),
		line("610100", order, "2026-09", "USD", "99.00"),
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.Actuals(f.ctx, f.le, "2026-09", "2026-10", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ActualRow{}
	for _, r := range rows {
		got[r.Code+"|"+r.Period+"|"+r.Currency] = r
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %+v; want five (admin Sep USD, admin Oct USD, admin Sep EUR, it Sep USD, order Sep USD)", rows)
	}
	a := got["CC-ADMIN|2026-09|USD"]
	if !a.Primary.Equal(decimal.RequireFromString("1250.50")) || !a.Allocated.IsZero() || !a.Total.Equal(decimal.RequireFromString("1250.50")) || a.Kind != "CostCenter" || a.Name == "" {
		t.Fatalf("CC-ADMIN September = %+v; want 1250.50 primary, nothing allocated", a)
	}
	if only, err := f.st.Actuals(f.ctx, f.le, "2026-09", "2026-09", "CostCenter"); err != nil || len(only) != 3 {
		t.Fatalf("cost centers of September = %+v, %v; want admin USD, admin EUR and it USD", only, err)
	}
	if rows, err := f.st.Actuals(f.ctx, ids.New(), "2026-09", "2026-10", ""); err != nil || len(rows) != 0 {
		t.Fatalf("another legal entity = %+v, %v; want none", rows, err)
	}
}

func TestActuals_ShowAllocatedCostAfterATopDownRun(t *testing.T) {
	f := newLedger(t, 40)
	source := f.object(t, "CostCenter", "ENTITY")
	north, south := f.object(t, "Product", "NORTH"), f.object(t, "Product", "SOUTH")
	f.book(t, source, "1000.00")
	if _, _, err := f.st.RunTopDown(f.ctx, ids.New(), "h", TopDownRequest{LegalEntityID: f.le, Period: "2026-09", ReferencePeriod: "2026-08", Currency: "USD",
		SourceCostObjectID: source, CostElementID: f.primary, Amount: dec("1000.00"), Segments: []TopDownSegment{
			{CostObjectID: north, Basis: dec("3")}, {CostObjectID: south, Basis: dec("1")}}}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.st.Actuals(f.ctx, f.le, "2026-09", "2026-09", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ActualRow{}
	for _, r := range rows {
		got[r.Code] = r
	}
	if e := got["ENTITY"]; !e.Primary.Equal(decimal.RequireFromString("1000")) || !e.Allocated.Equal(decimal.RequireFromString("-1000")) || !e.Total.IsZero() {
		t.Fatalf("ENTITY = %+v; want 1000 booked, 1000 allocated away, nothing left", e)
	}
	if n := got["NORTH"]; !n.Primary.IsZero() || !n.Allocated.Equal(decimal.RequireFromString("750")) || !n.Total.Equal(decimal.RequireFromString("750")) {
		t.Fatalf("NORTH = %+v; want 750 allocated and nothing booked", n)
	}
}
