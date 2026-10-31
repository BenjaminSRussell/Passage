package store_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	tmsevents "github.com/BenjaminSRussell/passage/services/transport/internal/events"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// Wave 34 proof (issue #34): "Freight reconciliation matches/mismatches as expected;
// landed cost allocation sums to invoice exactly." Both fixtures come from
// independent Python references (testdata/gen_*.py, python3 -I).

type invoiceCase struct {
	ID   string `json:"id"`
	Bill *struct {
		Carrier  string     `json:"carrier"`
		Currency string     `json:"currency"`
		Lines    [][]string `json:"lines"`
	} `json:"bill"`
	BillOf  *string `json:"billOf"`
	Invoice struct {
		Carrier   string     `json:"carrier"`
		InvoiceNo string     `json:"invoiceNo"`
		Currency  string     `json:"currency"`
		Source    string     `json:"source"`
		Lines     [][]string `json:"lines"`
	} `json:"invoice"`
	Expected struct {
		Error         string `json:"error"`
		Status        string `json:"status"`
		VarianceTotal string `json:"varianceTotal"`
		Lines         []struct {
			ChargeCode string `json:"chargeCode"`
			Expected   string `json:"expected"`
			Invoiced   string `json:"invoiced"`
			Variance   string `json:"variance"`
			Within     bool   `json:"within"`
		} `json:"lines"`
	} `json:"expected"`
}

func fixtureLines(t *testing.T, raw [][]string) []domain.Line {
	t.Helper()
	out := make([]domain.Line, len(raw))
	for i, l := range raw {
		if len(l) != 2 {
			t.Fatalf("fixture line %v", l)
		}
		out[i] = domain.Line{ChargeCode: l[0], Amount: dec(l[1])}
	}
	return out
}

func readFixture(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

// TestCarrierInvoices50_ReconcileAsExpected registers the 50 fixture invoices in
// order through Postgres: each is refused with the reference's code (leaving its
// bill untouched), stored as UNEXPECTED_INVOICE, or matched to its bill with the
// reference's status, variance total and per-charge comparison (in the bill, the
// invoice and the freightbill.matched event).
func TestCarrierInvoices50_ReconcileAsExpected(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	var f struct {
		Counts map[string]int `json:"counts"`
		Cases  []invoiceCase  `json:"cases"`
	}
	readFixture(t, "carrier-invoices-50.json", &f)
	if len(f.Cases) != 50 {
		t.Fatalf("fixture has %d invoices, want 50", len(f.Cases))
	}
	carriers := map[string]string{"A": ids.New(), "B": ids.New(), "C": ids.New()}
	bills := map[string]domain.FreightBill{}
	matched, unexpected := map[string]invoiceCase{}, 0
	for _, c := range f.Cases {
		var billID *string
		switch {
		case c.Bill != nil:
			b, err := s.CreateFreightBill(ctx, domain.FreightBill{LoadID: seedLoad(t, s, ctx), CarrierID: carriers[c.Bill.Carrier],
				Currency: c.Bill.Currency, ExpectedLines: fixtureLines(t, c.Bill.Lines)}, store.Idem{})
			if err != nil {
				t.Fatalf("%s: create bill: %v", c.ID, err)
			}
			bills[c.ID] = b
			billID = &b.ID
		case c.BillOf != nil:
			b := bills[*c.BillOf]
			billID = &b.ID
		}
		before := domain.FreightBill{}
		if billID != nil {
			var err error
			if before, err = s.GetFreightBill(ctx, *billID); err != nil {
				t.Fatal(err)
			}
		}
		inv, bill, err := s.RegisterCarrierInvoice(ctx, domain.CarrierInvoice{CarrierID: carriers[c.Invoice.Carrier], InvoiceNo: c.Invoice.InvoiceNo,
			InvoiceDate: "2026-06-15", Source: c.Invoice.Source, FreightBillID: billID, Currency: c.Invoice.Currency, Lines: fixtureLines(t, c.Invoice.Lines)})
		want := c.Expected
		if want.Error != "" {
			if err == nil {
				t.Errorf("%s: registered %+v, want %s", c.ID, inv, want.Error)
				continue
			}
			if code := problemCode(t, err); code != want.Error {
				t.Errorf("%s: error %s, want %s", c.ID, code, want.Error)
			}
			if billID != nil {
				if after, _ := s.GetFreightBill(ctx, *billID); after.Version != before.Version || after.MatchStatus != before.MatchStatus {
					t.Errorf("%s: refused invoice changed the bill from %+v to %+v", c.ID, before, after)
				}
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v, want %s", c.ID, err, want.Status)
			continue
		}
		if inv.MatchStatus != want.Status {
			t.Errorf("%s: invoice %s, want %s", c.ID, inv.MatchStatus, want.Status)
			continue
		}
		if want.Status == domain.MatchUnexpected {
			unexpected++
			if bill != nil || inv.VarianceTotal != nil {
				t.Errorf("%s: unexpected invoice matched something: %+v %+v", c.ID, inv, bill)
			}
			continue
		}
		if inv.VarianceTotal == nil || !inv.VarianceTotal.Equal(dec(want.VarianceTotal)) {
			t.Errorf("%s: invoice variance %v, want %s", c.ID, inv.VarianceTotal, want.VarianceTotal)
		}
		stored, err := s.GetFreightBill(ctx, *billID)
		if err != nil || stored.MatchStatus != want.Status || stored.VarianceTotal == nil || !stored.VarianceTotal.Equal(dec(want.VarianceTotal)) ||
			stored.CarrierInvoiceID == nil || *stored.CarrierInvoiceID != inv.ID || bill.Version != before.Version+1 {
			t.Errorf("%s: stored bill %+v (%v), want %s %s", c.ID, stored, err, want.Status, want.VarianceTotal)
		}
		// The per-charge comparison, recomputed from the stored documents.
		res := domain.MatchBill(stored, inv)
		if len(res.Lines) != len(want.Lines) {
			t.Errorf("%s: %d compared charges, want %d", c.ID, len(res.Lines), len(want.Lines))
		} else {
			for i, wl := range want.Lines {
				gl := res.Lines[i]
				if gl.ChargeCode != wl.ChargeCode || !gl.Expected.Equal(dec(wl.Expected)) || !gl.Invoiced.Equal(dec(wl.Invoiced)) ||
					!gl.Variance.Equal(dec(wl.Variance)) || gl.Within != wl.Within {
					t.Errorf("%s: charge %d = %+v, want %+v", c.ID, i, gl, wl)
				}
			}
		}
		matched[*billID] = c
	}
	// Every matched invoice published one freightbill.matched event with the same comparison.
	events := eventsOf(t, admin, tmsevents.FreightBillMatched)
	if len(events) != len(matched) {
		t.Fatalf("%d matched events, want %d", len(events), len(matched))
	}
	for _, e := range events {
		c, ok := matched[e.Subject]
		if !ok {
			t.Fatalf("event for bill %s that the fixture did not match", e.Subject)
		}
		var p tmsevents.FreightBillMatchedPayload
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p.MatchStatus != c.Expected.Status || p.Variance != dec(c.Expected.VarianceTotal).StringFixed(2) || len(p.Lines) != len(c.Expected.Lines) {
			t.Errorf("%s: event %+v", c.ID, p)
			continue
		}
		for i, wl := range c.Expected.Lines {
			if p.Lines[i].ChargeCode != wl.ChargeCode || p.Lines[i].WithinTolerance != wl.Within || p.Lines[i].Variance != dec(wl.Variance).StringFixed(2) {
				t.Errorf("%s: event line %d = %+v, want %+v", c.ID, i, p.Lines[i], wl)
			}
		}
	}
	queue, err := s.ListCarrierInvoices(ctx, store.InvoiceFilter{MatchStatus: domain.MatchUnexpected})
	if err != nil || len(queue) != unexpected {
		t.Fatalf("exception queue = %d (%v), want %d", len(queue), err, unexpected)
	}
	t.Logf("outcomes %v", f.Counts)
}

type voyageCase struct {
	Voyage   domain.Voyage `json:"voyage"`
	Expected struct {
		CostTotal       string `json:"costTotal"`
		CostAllocations []struct {
			CostType     string   `json:"costType"`
			Shares       []string `json:"shares"`
			Residual     string   `json:"residual"`
			ResidualLine int      `json:"residualLine"`
		} `json:"costAllocations"`
		LineTotals []string `json:"lineTotals"`
	} `json:"expected"`
}

// TestVoyage3_AllocationSumsToInvoicesExactly posts the three fixture voyages
// through Postgres: every share, residual and residual line equals the reference's
// (BR-34.6), each cost line's shares sum to that invoice amount exactly, and the
// landedcost.posted receipt lines sum to the voyage cost total to the cent.
func TestVoyage3_AllocationSumsToInvoicesExactly(t *testing.T) {
	s, admin := setup(t)
	ctx := tenantCtx(1)
	var f struct {
		Voyages []voyageCase `json:"voyages"`
	}
	readFixture(t, "voyage-3.json", &f)
	if len(f.Voyages) != 3 {
		t.Fatalf("fixture has %d voyages, want 3", len(f.Voyages))
	}
	for _, vc := range f.Voyages {
		in := domain.NormalizeVoyage(vc.Voyage)
		if errs := domain.ValidateVoyage(in); len(errs) > 0 {
			t.Fatalf("%s: fixture voyage invalid: %v", in.Reference, errs)
		}
		if errs := domain.ContainerErrors(in); len(errs) > 0 {
			t.Fatalf("%s: fixture containers fail ISO 6346: %v", in.Reference, errs)
		}
		created, err := s.CreateVoyage(ctx, in, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		posted, err := s.AllocateVoyage(ctx, created.ID, t0)
		if err != nil {
			t.Fatal(err)
		}
		if posted.Status != domain.VoyagePosted || len(posted.CostAllocations) != len(vc.Expected.CostAllocations) {
			t.Fatalf("%s: posted %s with %d cost allocations", in.Reference, posted.Status, len(posted.CostAllocations))
		}
		for k, want := range vc.Expected.CostAllocations {
			got := posted.CostAllocations[k]
			sum := decimal.Zero
			for i, sh := range got.Shares {
				sum = sum.Add(sh.Amount)
				if sh.Line != in.ReceiptLines[i].Line || !sh.Amount.Equal(dec(want.Shares[i])) {
					t.Errorf("%s %s: line %d share %s, want %s", in.Reference, want.CostType, sh.Line, sh.Amount, want.Shares[i])
				}
			}
			if !sum.Equal(in.CostLines[k].Amount) {
				t.Errorf("%s %s: shares sum to %s, invoice is %s", in.Reference, want.CostType, sum, in.CostLines[k].Amount)
			}
			if got.CostType != want.CostType || !got.Residual.Equal(dec(want.Residual)) || got.ResidualLine != want.ResidualLine {
				t.Errorf("%s %s: residual %s on line %d, want %s on line %d", in.Reference, got.CostType, got.Residual, got.ResidualLine, want.Residual, want.ResidualLine)
			}
		}
		total := decimal.Zero
		for i, a := range posted.Allocations {
			total = total.Add(a.Amount)
			if !a.Amount.Equal(dec(vc.Expected.LineTotals[i])) {
				t.Errorf("%s: receipt line %d landed cost %s, want %s", in.Reference, a.Line, a.Amount, vc.Expected.LineTotals[i])
			}
		}
		if !total.Equal(dec(vc.Expected.CostTotal)) {
			t.Errorf("%s: receipt lines sum to %s, cost total %s", in.Reference, total, vc.Expected.CostTotal)
		}
		var p tmsevents.LandedCostPostedPayload
		var found bool
		for _, e := range eventsOf(t, admin, tmsevents.LandedCostPosted) {
			if e.Subject == created.ID {
				found = json.Unmarshal(e.Data, &p) == nil
			}
		}
		eventSum := decimal.Zero
		for _, l := range p.ReceiptLines {
			eventSum = eventSum.Add(dec(l.Amount))
		}
		if !found || p.Total != vc.Expected.CostTotal || !eventSum.Equal(dec(vc.Expected.CostTotal)) || len(p.ReceiptLines) != len(in.ReceiptLines) {
			t.Errorf("%s: landedcost.posted total %s, lines sum %s, want %s", in.Reference, p.Total, eventSum, vc.Expected.CostTotal)
		}
	}
}

// TestReconciliationFixtures_MatchReference: both committed fixtures are exactly
// what their independent references write after their hand-worked self-checks.
func TestReconciliationFixtures_MatchReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	for _, name := range []string{"carrier-invoices-50", "voyage-3"} {
		gen := map[string]string{"carrier-invoices-50": "gen_carrier_invoices_50.py", "voyage-3": "gen_voyage_3.py"}[name]
		out := filepath.Join(t.TempDir(), name+".json")
		if msg, err := exec.Command(py, "-I", filepath.Join("testdata", gen), out).CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", gen, err, msg)
		}
		got, _ := os.ReadFile(out)
		want, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("testdata/%s.json differs from the reference; regenerate it with python3 -I testdata/%s", name, gen)
		}
	}
}
