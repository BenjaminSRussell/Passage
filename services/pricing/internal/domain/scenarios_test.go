package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// scenarioFile is testdata/pricing-scenarios.json: hand-built resolver cases with
// expected net prices, line nets, error codes and the applied conditions per line.
type scenarioFile struct {
	Cases []scenarioCase `json:"cases"`
}

type scenarioCase struct {
	Name   string `json:"name"`
	Header struct {
		CustomerID      string `json:"customerId"`
		CustomerGroupID string `json:"customerGroupId"`
		Currency        string `json:"currency"`
		PricingDate     string `json:"pricingDate"`
	} `json:"header"`
	Lines   []scenarioLine   `json:"lines"`
	Records []scenarioRecord `json:"records"`
	Expect  struct {
		OrderNet      string               `json:"orderNet"`
		TotalDiscount string               `json:"totalDiscount"`
		Applied       []string             `json:"applied"`
		Lines         []scenarioExpectLine `json:"lines"`
	} `json:"expect"`
}

type scenarioLine struct {
	ProductID      string `json:"productId"`
	ProductGroupID string `json:"productGroupId"`
	Variant        string `json:"variant"`
	Unit           string `json:"unit"`
	Quantity       string `json:"quantity"`
}

type scenarioExpectLine struct {
	LineNo       int      `json:"lineNo"`
	NetUnitPrice string   `json:"netUnitPrice"`
	LineNet      string   `json:"lineNet"`
	ErrorCode    string   `json:"errorCode"`
	Exclusive    bool     `json:"exclusive"`
	Applied      []string `json:"applied"`
}

type scenarioRecord struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	CustomerKind string `json:"customerKind"`
	CustomerKey  string `json:"customerKey"`
	ProductKind  string `json:"productKind"`
	ProductKey   string `json:"productKey"`
	Variant      string `json:"variant"`
	Currency     string `json:"currency"`
	Unit         string `json:"unit"`
	FromQuantity string `json:"fromQuantity"`
	Amount       string `json:"amount"`
	DiscPct1     string `json:"discPct1"`
	DiscPct2     string `json:"discPct2"`
	ValidFrom    string `json:"validFrom"`
	ValidTo      string `json:"validTo"`
	SearchNext   bool   `json:"searchNext"`
	Exclusive    bool   `json:"exclusive"`
}

func scenarioDec(raw string) decimal.Decimal {
	if raw == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(raw)
}

func scenarioDay(raw string) time.Time {
	d, err := time.Parse("2006-01-02", raw)
	if err != nil {
		panic(fmt.Sprintf("bad fixture date %q: %v", raw, err))
	}
	return d
}

func (s scenarioRecord) toRecord() Record {
	return Record{
		ID: s.ID, Status: StatusActive, Type: ConditionType(s.Type),
		CustomerKind: CustomerKind(s.CustomerKind), CustomerKey: s.CustomerKey,
		ProductKind: ProductKind(s.ProductKind), ProductKey: s.ProductKey, Variant: s.Variant,
		Currency: s.Currency, Unit: s.Unit,
		FromQuantity: scenarioDec(s.FromQuantity), Amount: scenarioDec(s.Amount),
		DiscPct1: scenarioDec(s.DiscPct1), DiscPct2: scenarioDec(s.DiscPct2),
		ValidFrom: scenarioDay(s.ValidFrom), ValidTo: scenarioDay(s.ValidTo),
		SearchNext: s.SearchNext, Exclusive: s.Exclusive,
	}
}

func loadScenarioFile(t *testing.T) scenarioFile {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/pricing-scenarios.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var f scenarioFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	return f
}

func TestScenarioFixtures_ResolveToExpectedPriceAndExplain(t *testing.T) {
	f := loadScenarioFile(t)
	if len(f.Cases) == 0 {
		t.Fatal("no scenarios in fixture")
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			in := Input{Header: Header{
				CustomerID: c.Header.CustomerID, CustomerGroupID: c.Header.CustomerGroupID,
				Currency: c.Header.Currency, PricingDate: scenarioDay(c.Header.PricingDate),
			}}
			for i, l := range c.Lines {
				in.Lines = append(in.Lines, Line{No: i + 1, ProductID: l.ProductID, ProductGroupID: l.ProductGroupID, Variant: l.Variant,
					Unit: l.Unit, Quantity: decimal.RequireFromString(l.Quantity)})
			}
			for _, r := range c.Records {
				in.Records = append(in.Records, r.toRecord())
			}
			res := Resolve(in)

			if want := decimal.RequireFromString(c.Expect.OrderNet); !res.OrderNet.Equal(want) {
				t.Errorf("orderNet = %s, want %s", res.OrderNet, want)
			}
			if c.Expect.TotalDiscount != "" && !res.TotalDiscount.Equal(decimal.RequireFromString(c.Expect.TotalDiscount)) {
				t.Errorf("totalDiscount = %s, want %s", res.TotalDiscount, c.Expect.TotalDiscount)
			}
			if got := appliedKeys(res.Applied); fmt.Sprint(got) != fmt.Sprint(c.Expect.Applied) {
				t.Errorf("order applied = %v, want %v", got, c.Expect.Applied)
			}
			if len(res.Lines) != len(c.Expect.Lines) {
				t.Fatalf("got %d lines, want %d", len(res.Lines), len(c.Expect.Lines))
			}
			for i, want := range c.Expect.Lines {
				got := res.Lines[i]
				if got.Error != want.ErrorCode {
					t.Errorf("line %d errorCode = %q, want %q", want.LineNo, got.Error, want.ErrorCode)
				}
				if want.NetUnitPrice != "" && !got.NetUnit.Equal(decimal.RequireFromString(want.NetUnitPrice)) {
					t.Errorf("line %d netUnit = %s, want %s", want.LineNo, got.NetUnit, want.NetUnitPrice)
				}
				if !got.LineNet.Equal(decimal.RequireFromString(want.LineNet)) {
					t.Errorf("line %d lineNet = %s, want %s", want.LineNo, got.LineNet, want.LineNet)
				}
				if got.Exclusive != want.Exclusive {
					t.Errorf("line %d exclusive = %v, want %v", want.LineNo, got.Exclusive, want.Exclusive)
				}
				if applied := appliedKeys(got.Applied); fmt.Sprint(applied) != fmt.Sprint(want.Applied) {
					t.Errorf("line %d applied = %v, want %v", want.LineNo, applied, want.Applied)
				}
				if len(got.Trace) == 0 {
					t.Errorf("line %d has no explain trace", want.LineNo)
				}
			}
		})
	}
}

func appliedKeys(in []Applied) []string {
	var out []string
	for _, a := range in {
		out = append(out, fmt.Sprintf("%s@%d", a.RecordID, a.Access))
	}
	return out
}

func TestScenarioFixtures_AccessNotEvaluatedAfterWinner(t *testing.T) {
	// AC-26.1: the winner at access 2 stops the search; access 4 is shown, not evaluated.
	f := loadScenarioFile(t)
	c := f.Cases[0]
	in := Input{Header: Header{CustomerID: c.Header.CustomerID, CustomerGroupID: c.Header.CustomerGroupID,
		Currency: c.Header.Currency, PricingDate: scenarioDay(c.Header.PricingDate)},
		Lines: []Line{{No: 1, ProductID: "P1", Unit: "EA", Quantity: decimal.NewFromInt(1)}}}
	for _, r := range c.Records {
		in.Records = append(in.Records, r.toRecord())
	}
	res := Resolve(in)
	var access4 *TraceAccess
	for i := range res.Lines[0].Trace[0].Accesses {
		if res.Lines[0].Trace[0].Accesses[i].Seq == 4 {
			access4 = &res.Lines[0].Trace[0].Accesses[i]
		}
	}
	if access4 == nil || access4.Evaluated {
		t.Fatalf("access 4 should be listed as not evaluated, got %+v", access4)
	}
}
