package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// pricing500File is testdata/pricing-500.json (PRD 5.26.9 fixture pricing-500, Wave 26 AC1).
// Its expected values come from the independent Python reference testdata/gen_pricing500.py,
// never from this engine.
type pricing500File struct {
	Cases []struct {
		Name   string `json:"name"`
		Family string `json:"family"`
		Header struct {
			CustomerID      string `json:"customerId"`
			CustomerGroupID string `json:"customerGroupId"`
			Currency        string `json:"currency"`
			PricingDate     string `json:"pricingDate"`
		} `json:"header"`
		Lines   []scenarioLine `json:"lines"`
		Records []struct {
			scenarioRecord
			Status     string `json:"status"`
			CampaignID string `json:"campaignId"`
		} `json:"records"`
		SmallOrderThreshold string `json:"smallOrderThreshold"`
		Expect              struct {
			Subtotal      string           `json:"subtotal"`
			TotalDiscount string           `json:"totalDiscount"`
			Surcharge     string           `json:"surcharge"`
			OrderNet      string           `json:"orderNet"`
			Applied       []string         `json:"applied"`
			Trace         []pricing500Step `json:"trace"`
			Lines         []struct {
				LineNo       int              `json:"lineNo"`
				ErrorCode    string           `json:"errorCode"`
				UnitPrice    string           `json:"unitPrice"`
				NetUnitPrice string           `json:"netUnitPrice"`
				LineNet      string           `json:"lineNet"`
				Exclusive    bool             `json:"exclusive"`
				Applied      []string         `json:"applied"`
				Trace        []pricing500Step `json:"trace"`
			} `json:"lines"`
		} `json:"expect"`
	} `json:"cases"`
}

// pricing500Step is one condition's explain trace in the reference's compact form:
// each access is "seq:x" (not evaluated), "seq:-" (no candidate) or "seq:id,id,..."
// (the candidates best first; the first one won the access).
type pricing500Step struct {
	Condition string   `json:"condition"`
	Selected  string   `json:"selected"`
	Accesses  []string `json:"accesses"`
}

func compactTrace(steps []TraceStep) []pricing500Step {
	out := []pricing500Step{}
	for _, s := range steps {
		ps := pricing500Step{Condition: string(s.Condition), Selected: s.Selected}
		for _, a := range s.Accesses {
			switch {
			case !a.Evaluated:
				ps.Accesses = append(ps.Accesses, fmt.Sprintf("%d:x", a.Seq))
			case len(a.Candidates) == 0:
				ps.Accesses = append(ps.Accesses, fmt.Sprintf("%d:-", a.Seq))
			default:
				ps.Accesses = append(ps.Accesses, fmt.Sprintf("%d:%s", a.Seq, strings.Join(a.Candidates, ",")))
			}
		}
		out = append(out, ps)
	}
	return out
}

// hasOverlap reports whether any step saw two or more distinct candidate records.
func hasOverlap(steps []pricing500Step) bool {
	for _, s := range steps {
		ids := map[string]bool{}
		for _, a := range s.Accesses {
			if _, list, ok := strings.Cut(a, ":"); ok && list != "x" && list != "-" {
				for _, id := range strings.Split(list, ",") {
					ids[id] = true
				}
			}
		}
		if len(ids) >= 2 {
			return true
		}
	}
	return false
}

func sameStrings(a, b []string) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func sameTrace(got []TraceStep, want []pricing500Step) bool {
	if want == nil {
		want = []pricing500Step{}
	}
	g, _ := json.Marshal(compactTrace(got))
	w, _ := json.Marshal(want)
	return string(g) == string(w)
}

// TestPricing500_ResolveToExpectedPriceAndExplain (Wave 26 AC1, PRD 5.26.9 "Resolver
// scenarios: 500/500 price and trace"): every scenario resolves to the reference's prices,
// totals, applied conditions and access-by-access explain trace (candidates in rank order,
// evaluated / not evaluated, selected record) for every line and the order.
func TestPricing500_ResolveToExpectedPriceAndExplain(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/pricing-500.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f pricing500File
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(f.Cases) != 500 {
		t.Fatalf("pricing-500 has %d cases, want 500", len(f.Cases))
	}
	overlapping := 0
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			in := Input{Header: Header{CustomerID: c.Header.CustomerID, CustomerGroupID: c.Header.CustomerGroupID,
				Currency: c.Header.Currency, PricingDate: scenarioDay(c.Header.PricingDate)},
				SmallOrderThreshold: scenarioDec(c.SmallOrderThreshold)}
			for i, l := range c.Lines {
				in.Lines = append(in.Lines, Line{No: i + 1, ProductID: l.ProductID, ProductGroupID: l.ProductGroupID,
					Variant: l.Variant, Unit: l.Unit, Quantity: decimal.RequireFromString(l.Quantity)})
			}
			for _, r := range c.Records {
				rec := r.toRecord()
				rec.CampaignID = r.CampaignID
				if r.Status != "" {
					rec.Status = r.Status
				}
				in.Records = append(in.Records, rec)
			}
			res := Resolve(in)
			e := c.Expect
			for name, pair := range map[string][2]decimal.Decimal{
				"subtotal": {res.Subtotal, scenarioDec(e.Subtotal)}, "totalDiscount": {res.TotalDiscount, scenarioDec(e.TotalDiscount)},
				"surcharge": {res.Surcharge, scenarioDec(e.Surcharge)}, "orderNet": {res.OrderNet, scenarioDec(e.OrderNet)},
			} {
				if !pair[0].Equal(pair[1]) {
					t.Errorf("%s = %s, want %s", name, pair[0], pair[1])
				}
			}
			if got := appliedKeys(res.Applied); !sameStrings(got, e.Applied) {
				t.Errorf("order applied = %v, want %v", got, e.Applied)
			}
			if !sameTrace(res.Trace, e.Trace) {
				t.Errorf("order trace = %+v, want %+v", compactTrace(res.Trace), e.Trace)
			}
			if len(res.Lines) != len(e.Lines) {
				t.Fatalf("got %d lines, want %d", len(res.Lines), len(e.Lines))
			}
			for i, want := range e.Lines {
				got := res.Lines[i]
				if got.Error != want.ErrorCode || got.Exclusive != want.Exclusive {
					t.Errorf("line %d error/exclusive = %q/%v, want %q/%v", want.LineNo, got.Error, got.Exclusive, want.ErrorCode, want.Exclusive)
				}
				for name, pair := range map[string][2]decimal.Decimal{
					"unitPrice": {got.UnitPrice, scenarioDec(want.UnitPrice)}, "netUnitPrice": {got.NetUnit, scenarioDec(want.NetUnitPrice)},
					"lineNet": {got.LineNet, scenarioDec(want.LineNet)},
				} {
					if !pair[0].Equal(pair[1]) {
						t.Errorf("line %d %s = %s, want %s", want.LineNo, name, pair[0], pair[1])
					}
				}
				if applied := appliedKeys(got.Applied); !sameStrings(applied, want.Applied) {
					t.Errorf("line %d applied = %v, want %v", want.LineNo, applied, want.Applied)
				}
				if !sameTrace(got.Trace, want.Trace) {
					t.Errorf("line %d trace = %+v, want %+v", want.LineNo, compactTrace(got.Trace), want.Trace)
				}
			}
		})
		if hasOverlap(c.Expect.Trace) {
			overlapping++
			continue
		}
		for _, l := range c.Expect.Lines {
			if hasOverlap(l.Trace) {
				overlapping++
				break
			}
		}
	}
	// "including overlapping agreements": count the scenarios whose expected trace has two or
	// more records competing for one condition (at one access or across accesses).
	t.Logf("%d of %d scenarios have overlapping agreements", overlapping, len(f.Cases))
	if overlapping < 250 {
		t.Errorf("only %d of 500 scenarios have overlapping agreements; the fixture lost its overlap cases", overlapping)
	}
}

// TestPricing500_FixtureMatchesReference: the committed fixture is exactly what the
// independent reference generates (after its own self-checks against the PRD examples and
// the hand-worked cases pass), so expected values are never edited by hand or from the engine.
func TestPricing500_FixtureMatchesReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	out := filepath.Join(t.TempDir(), "pricing-500.json")
	if msg, err := exec.Command(py, "-I", "../../testdata/gen_pricing500.py", out).CombinedOutput(); err != nil {
		t.Fatalf("reference generator failed: %v\n%s", err, msg)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../testdata/pricing-500.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("testdata/pricing-500.json differs from the reference output; regenerate it with python3 -I testdata/gen_pricing500.py")
	}
}
